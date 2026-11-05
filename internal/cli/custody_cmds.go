package cli

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/manifest"
	"github.com/gates-brightly/swimlane/internal/sign"
)

// cmdKeys shows the key this machine signs the chain (and signoffs) with.
func cmdKeys(args []string) error {
	if len(args) > 0 {
		return usagef("usage: swim keys")
	}
	_, cfg, err := repoNoMigrate()
	if err != nil {
		return err
	}
	if k, err := sign.LoadKey(cfg.SigningKey); err == nil {
		fmt.Printf("this machine signs with %s %s (%s); signing: %s\n", sign.Fingerprint(k.Public), k.Path, or(keyComment(k.Public), "no comment"), cfg.SigningMode())
	} else {
		fmt.Printf("this machine has no signing key (%v); signing: %s\n", err, cfg.SigningMode())
	}
	return nil
}

func keyComment(k string) string {
	if f := strings.Fields(k); len(f) >= 3 {
		return strings.Join(f[2:], " ")
	}
	return ""
}

// who names a key: its fingerprint and comment.
func who(key string) string {
	if key == "" {
		return "unsigned"
	}
	if c := keyComment(key); c != "" {
		return sign.Fingerprint(key) + " (" + c + ")"
	}
	return sign.Fingerprint(key)
}

func short12(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return or(s, "-")
}

// checkEntries checks what each chain entry recorded against what's on
// disk: every round's kept script, pinned copy, inputs and log section,
// and every signed approval's signatures. A mismatch is a break (the
// consistent level); something gone is a warning.
func (v *verifier) checkEntries(links []chain.Link) {
	root := v.root
	var logsGone []int // rounds whose log was deleted, not archived
	defer func() {
		if len(logsGone) > 0 {
			v.warn = append(v.warn, fmt.Sprintf("the logs of %s are gone (deleted, not archived), so their contents can't be rechecked: #%s", plural(len(logsGone), "round"), joinNums(logsGone)))
		}
	}()
	runs, unattested := attestations(links)
	// Runs swim run started (their rounds ran pinned copies), and those
	// that were cut short (a launcher killed before it could attest).
	launchedRun, cutShort := map[string]bool{}, map[string]bool{}
	// Round log sections swim prune deleted (their archived logs), and when.
	prunedAt := map[string]string{}
	for _, l := range links {
		if l.Type == "prune" {
			for _, h := range l.Pruned {
				prunedAt[h] = l.At
			}
		}
	}
	for _, l := range links {
		if l.Type == "round" && l.Run != "" {
			launchedRun[l.Run] = launchedRun[l.Run] || l.Manifest != ""
			cutShort[l.Run] = cutShort[l.Run] || l.Result == "interrupted"
		}
	}
	if unattested > 0 {
		v.notes = append(v.notes, fmt.Sprintf("%d runs have approvals or rounds but no run attestation (lanes run directly with bash, runs from before attestations, or a swim run that didn't finish): their approvals can't be checked against what swim run recorded", unattested))
	}
	for _, l := range links {
		att, attested := runs[l.Run]
		switch l.Type {
		case "round":
			v.rounds++
			// What swim run attests its lane ran. Runs that share an id
			// (--run-id) each attest their own lanes: one of them must
			// list this lane, as it ran.
			if attested {
				key := strconv.Itoa(l.Lane)
				var listed []chain.Link
				for _, a := range att {
					if a.Manifests[key] != "" || a.Scripts[key] != "" {
						listed = append(listed, a)
					}
				}
				ran := func(a chain.Link) bool {
					return (l.Script == "" || a.Scripts[key] == "" || l.Script == a.Scripts[key]) &&
						(l.Manifest == "" || a.Manifests[key] == "" || l.Manifest == a.Manifests[key])
				}
				switch {
				case len(listed) == 0:
					v.bad = append(v.bad, fmt.Sprintf("entry #%d: a round of lane %d in run %s, which the run's attestation (entry %s) doesn't list", l.Seq, l.Lane, l.Run, seqs(att)))
				case !slices.ContainsFunc(listed, ran):
					a := listed[0]
					if l.Script != "" && a.Scripts[key] != "" && l.Script != a.Scripts[key] {
						v.bad = append(v.bad, fmt.Sprintf("entry #%d: lane %d's round in run %s names script %s, but the run ran %s (entry #%d)", l.Seq, l.Lane, l.Run, short12(l.Script), short12(a.Scripts[key]), a.Seq))
					} else {
						v.bad = append(v.bad, fmt.Sprintf("entry #%d: lane %d's round in run %s names manifest %s, but the run ran %s (entry #%d)", l.Seq, l.Lane, l.Run, short12(l.Manifest), short12(a.Manifests[key]), a.Seq))
					}
				}
			}
			if l.Script != "" {
				if got, err := chain.HashFile(filepath.Join(scriptsDir(root), l.Script+".sh")); err != nil {
					v.warn = append(v.warn, fmt.Sprintf("entry #%d: the script copy %s.sh is missing", l.Seq, short12(l.Script)))
				} else if got != l.Script {
					v.bad = append(v.bad, fmt.Sprintf("entry #%d: the kept script %s.sh was changed", l.Seq, short12(l.Script)))
				}
			}
			// The round's pinned copy, while it's kept (swim doctor --fix
			// prunes old stages): its manifest and files as they ran.
			if l.Manifest != "" {
				if m, err := manifest.Load(manifest.Dir(root, l.Run, l.Lane)); err == nil {
					switch {
					case m.SHA256 != l.Manifest && restaged(links, l, m.SHA256):
						// The lane ran again under the same run id
						// (--run-id): the stage is the later round's.
					case m.SHA256 != l.Manifest:
						v.bad = append(v.bad, fmt.Sprintf("entry #%d: the staged manifest of lane %d in run %s is %s, not the %s that ran", l.Seq, l.Lane, l.Run, short12(m.SHA256), short12(l.Manifest)))
					default:
						if err := m.Verify(); err != nil {
							v.bad = append(v.bad, fmt.Sprintf("entry #%d: lane %d's pinned copy in run %s: %v", l.Seq, l.Lane, l.Run, err))
						}
					}
				}
			}
			if l.Log != "" {
				// A lane run twice under one run id (--run-id) has a section
				// for each round: this one's must be among them.
				switch sums := roundLogHashes(root, l.Lane, l.Run); {
				case !sums[l.Log] && prunedAt[l.Log] != "":
					v.notes = append(v.notes, fmt.Sprintf("entry #%d: lane %d's log for run %s was pruned on %s (swim prune): its contents can't be rechecked", l.Seq, l.Lane, l.Run, prunedAt[l.Log]))
				case len(sums) == 0:
					logsGone = append(logsGone, l.Seq) // one line for them all, below
				case !sums[l.Log]:
					v.bad = append(v.bad, fmt.Sprintf("entry #%d: lane %d's log for run %s was changed after the round ended", l.Seq, l.Lane, l.Run))
				}
			}
		case "secret-released":
			// A guarded secret goes out only on an approval swim run
			// recorded: one its attestation doesn't list wasn't.
			for _, h := range l.Approvals {
				listed := slices.ContainsFunc(att, func(a chain.Link) bool { return slices.Contains(a.Approvals, h) })
				if attested && !listed {
					v.bad = append(v.bad, fmt.Sprintf("entry #%d: secret %s released to lane %d in run %s on approval %s, which swim run didn't record (its attestation, entry %s, doesn't list it)", l.Seq, l.Secret, l.Lane, l.Run, short12(h), seqs(att)))
				}
			}
		case "repair":
			// Not a break: the removed bytes were never a complete entry.
			v.notes = append(v.notes, fmt.Sprintf("entry #%d: swim chain repair %s", l.Seq, l.Action))
		case "decide":
			// Written by a swim that had decisions (decide), which this
			// one no longer has; they still verify. Under swim run only the
			// launcher recorded one, as an approval: one its run's
			// attestation doesn't list was written by someone else.
			if !attested && launchedRun[l.Run] && !cutShort[l.Run] {
				v.bad = append(v.bad, fmt.Sprintf("entry #%d: decision %s for lane %d in run %s, which swim run started but never attested: nothing vouches that swim run recorded it", l.Seq, l.Guard, l.Lane, l.Run))
			}
			if attested {
				var by []chain.Link
				for _, a := range att {
					if slices.Contains(a.Decisions, l.Hash) {
						by = append(by, a)
					}
				}
				switch {
				case len(by) == 0:
					v.bad = append(v.bad, fmt.Sprintf("entry #%d: decision %s for lane %d in run %s that swim run didn't record (its attestation, entry %s, doesn't list it)", l.Seq, l.Guard, l.Lane, l.Run, seqs(att)))
				case len(by) > 1:
					v.bad = append(v.bad, fmt.Sprintf("entry #%d: decision %s for lane %d in run %s that two attestations claim (entries %s)", l.Seq, l.Guard, l.Lane, l.Run, seqs(by)))
				}
			}
			if l.Choice < 1 || l.Choice > len(l.Choices) {
				v.bad = append(v.bad, fmt.Sprintf("entry #%d: decision %s for lane %d records choice %d of %d", l.Seq, l.Guard, l.Lane, l.Choice, len(l.Choices)))
			}
		case "approve":
			v.approvals++
			if !attested && launchedRun[l.Run] && !cutShort[l.Run] {
				v.bad = append(v.bad, fmt.Sprintf("entry #%d: an approval of %s for lane %d in run %s, which swim run started but never attested: nothing vouches that swim run recorded it", l.Seq, or(l.Guard, "a confirm"), l.Lane, l.Run))
			}
			// Under swim run only the launcher records approvals: one its
			// run's attestation doesn't list was written by someone else (a
			// lane recording its own approval).
			if attested {
				var by []chain.Link
				for _, a := range att {
					if slices.Contains(a.Approvals, l.Hash) {
						by = append(by, a)
					}
				}
				switch {
				case len(by) == 0:
					v.bad = append(v.bad, fmt.Sprintf("entry #%d: an approval of %s for lane %d in run %s that swim run didn't record (its attestation, entry %s, doesn't list it)", l.Seq, or(l.Guard, "a confirm"), l.Lane, l.Run, seqs(att)))
				case len(by) > 1:
					v.bad = append(v.bad, fmt.Sprintf("entry #%d: an approval of %s for lane %d in run %s that two attestations claim (entries %s): only the swim run that recorded it lists it", l.Seq, or(l.Guard, "a confirm"), l.Lane, l.Run, seqs(by)))
				}
			}
		}
	}
}

// attestations are the chain's run attestations by run id: one per swim
// run, so several when runs share an id (--run-id, e.g. a CI pipeline's).
// unattested counts the runs that have approve or round entries and no
// attestation.
func attestations(links []chain.Link) (runs map[string][]chain.Link, unattested int) {
	runs = map[string][]chain.Link{}
	for _, l := range links {
		if l.Type == "run" {
			runs[l.Run] = append(runs[l.Run], l)
		}
	}
	seen := map[string]bool{}
	for _, l := range links {
		if (l.Type == "approve" || l.Type == "decide" || l.Type == "round") && !seen[l.Run] {
			seen[l.Run] = true
			if _, ok := runs[l.Run]; !ok {
				unattested++
			}
		}
	}
	return runs, unattested
}

// restaged reports whether a round after l, of the same lane and run,
// ran manifest man: the lane ran again under l's run id and staged over
// l's copy.
func restaged(links []chain.Link, l chain.Link, man string) bool {
	for _, o := range links {
		if o.Seq > l.Seq && o.Type == "round" && o.Lane == l.Lane && o.Run == l.Run && o.Manifest == man {
			return true
		}
	}
	return false
}

// seqs lists entries' numbers: "#4" or "#4, #9".
func seqs(links []chain.Link) string {
	var s []string
	for _, l := range links {
		s = append(s, "#"+strconv.Itoa(l.Seq))
	}
	return strings.Join(s, ", ")
}

// roundLogHash is the hash of lane n's round in run, from its current or
// archived log (ok false if no log holds it).
func roundLogHashes(root string, n int, run string) map[string]bool {
	sums := map[string]bool{}
	for _, f := range append(archives(root, n), lane.Log(root, n)) {
		rounds, _ := roundsOf(f, run)
		for _, r := range rounds {
			sums[chain.Hash([]byte(roundSection(r)))] = true
		}
	}
	return sums
}
