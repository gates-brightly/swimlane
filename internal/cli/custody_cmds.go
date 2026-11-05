package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/manifest"
	"github.com/gates-brightly/swimlane/internal/sign"
	"github.com/gates-brightly/swimlane/internal/ui"
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

// cmdHistory shows the chain of custody like git log: newest first, each
// entry with its hash, signer, date and what happened (--oneline: one line
// each). swim history [RUN|N|JOB] [--oneline] [-n N] [--reverse] [--yaml].
func cmdHistory(args []string) error {
	var asYAML, oneline, reverse, noPager bool
	var limit string
	// git log's -N and -n N.
	var rest0 []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-n" && i+1 < len(args):
			limit = args[i+1]
			i++
		case strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && len(a) > 1:
			if _, err := strconv.Atoi(a[1:]); err == nil {
				limit = a[1:]
				continue
			}
			rest0 = append(rest0, a)
		default:
			rest0 = append(rest0, a)
		}
	}
	rest, err := flags{bools: map[string]*bool{"yaml": &asYAML, "oneline": &oneline, "reverse": &reverse, "no-pager": &noPager},
		strs: map[string]*string{"max-count": &limit}}.parse(rest0)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return usagef("usage: swim history [RUN|N|JOB] [--oneline] [-n N] [--reverse] [--yaml]")
	}
	count := 0
	if limit != "" {
		if count, err = strconv.Atoi(limit); err != nil || count < 0 {
			return usagef("-n takes a number of entries, got %q", limit)
		}
	}
	root, cfg, err := repoNoMigrate()
	if err != nil {
		return err
	}
	links, err := chain.Read(root)
	if err != nil {
		return err
	}
	match := func(l chain.Link) bool { return true }
	if len(rest) == 1 {
		ref, err := taskRef(root, cfg, rest[0])
		if err != nil {
			return err
		}
		if n, err := strconv.Atoi(ref); err == nil {
			if _, err := laneArg(cfg, ref); err != nil {
				return err
			}
			match = func(l chain.Link) bool { return l.Lane == n }
		} else {
			match = func(l chain.Link) bool { return l.Run == ref || lane.MatchJob(l.Job, ref) }
		}
	}
	// Newest first, limited, then (--reverse) flipped: as git log does.
	var shown []chain.Link
	for i := len(links) - 1; i >= 0; i-- {
		if match(links[i]) && (count == 0 || len(shown) < count) {
			shown = append(shown, links[i])
		}
	}
	if reverse {
		slices.Reverse(shown)
	}
	if asYAML {
		// Data stays oldest first, as swim.history/v1 always was.
		slices.Reverse(shown)
		type row struct {
			chain.Entry `yaml:",inline"`
			Hash        string `yaml:"hash"`
			SignedBy    string `yaml:"signed_by"`
		}
		var rows []row
		for _, l := range shown {
			rows = append(rows, row{l.Entry, l.Hash, who(l.SignedBy())})
		}
		return yaml.NewEncoder(os.Stdout).Encode(map[string]any{"schema": "swim.history/v1", "entries": rows})
	}
	if len(shown) == 0 {
		fmt.Println("no chain of custody yet: swim records it as rounds run (" + rel(root, chain.Path(root)) + ")")
		return nil
	}
	p := ui.Painter{On: ui.ColorEnabled(os.Stdout)}
	return paged(noPager, func(w io.Writer) {
		for i, l := range shown {
			// The chain keeps the lane's text as given (evidence); it's made
			// safe only here, where it's shown.
			subject, body := historyEntry(l)
			subject = ui.Safe(subject)
			for j := range body {
				body[j] = ui.Safe(body[j])
			}
			if oneline {
				fmt.Fprintf(w, "%s %s\n", p.Paint(ui.Yellow, short12(l.Hash)), subject)
				continue
			}
			if i > 0 {
				fmt.Fprintln(w)
			}
			fmt.Fprintln(w, p.Paint(ui.Yellow, "entry "+l.Hash)+historyRefs(p, l))
			fmt.Fprintln(w, "Signer: "+who(l.SignedBy()))
			fmt.Fprintln(w, "Date:   "+gitDate(l.At))
			fmt.Fprintln(w)
			fmt.Fprintln(w, "    "+subject)
			if len(body) > 0 {
				fmt.Fprintln(w)
				for _, b := range body {
					fmt.Fprintln(w, "    "+b)
				}
			}
		}
	})
}

// historyEntry is an entry's one-line subject and its detail lines.
func historyEntry(l chain.Link) (string, []string) {
	var names []string
	for _, a := range l.Approvers {
		names = append(names, or(a.Name, who(a.Key)))
	}
	by := strings.Join(names, ", ")
	var body []string
	field := func(k, v string) {
		if v != "" {
			body = append(body, fmt.Sprintf("%-10s %s", k+":", v))
		}
	}
	var subject string
	switch l.Type {
	case "approve":
		subject = fmt.Sprintf("approve %s for swim %d by %s via %s", l.Guard, l.Lane, by, l.Method)
		field("Action", l.Action)
		field("Round", l.Round)
		field("Script", l.Script)
	case "decide":
		picked := ""
		if l.Choice >= 1 && l.Choice <= len(l.Choices) {
			picked = l.Choices[l.Choice-1]
		}
		subject = fmt.Sprintf("decide %s=%d for swim %d by %s via %s", l.Guard, l.Choice, l.Lane, by, l.Method)
		field("Question", l.Action)
		field("Chosen", picked)
		for i, c := range l.Choices {
			field(fmt.Sprintf("Choice %d", i+1), c)
		}
		field("Script", l.Script)
	case "round":
		exit := ""
		if l.Exit != nil {
			exit = fmt.Sprintf(" (exit %d)", *l.Exit)
		}
		subject = fmt.Sprintf("round swim %d %s%s", l.Lane, l.Result, exit)
		field("Round", l.Round)
		field("Job", l.Job)
		field("Script", l.Script)
		field("Log", l.Log)
		for _, a := range l.Approvals {
			field("Approval", a)
		}
		if len(l.AutoApproved) > 0 {
			field("Auto", strings.Join(l.AutoApproved, " ")+" (approved by -y: no one was asked)")
		}
	case "grant", "revoke":
		what := l.Action
		if what == "" {
			what = fmt.Sprintf("swim %d script %s", l.Lane, short12(l.Script))
		}
		verb := "approved"
		if l.Type == "revoke" {
			verb = "revoked"
		}
		subject = fmt.Sprintf("%s %s by %s", verb, what, by)
		field("Script", l.Script)
	case "hub":
		// A command from the hub, as the worker recorded it (refusals too).
		subject = l.Action
		field("Run", l.Run)
		field("Script", l.Script)
		field("Manifest", l.Manifest)
	case "run":
		var lanes []string
		for k := range l.Manifests {
			lanes = append(lanes, k)
		}
		sort.Strings(lanes)
		subject = fmt.Sprintf("run %s attested: swim %s, %s", l.Run, strings.Join(lanes, " "), plural(len(l.Approvals), "approval"))
		if len(l.Decisions) > 0 {
			subject += ", " + plural(len(l.Decisions), "decision")
		}
		for _, k := range lanes {
			field("Lane "+k, l.Manifests[k])
		}
		for _, a := range l.Approvals {
			field("Approval", a)
		}
		for _, d := range l.Decisions {
			field("Decision", d)
		}
		field("Flags", strings.Join(l.Flags, " "))
		field("Approvals unavailable", l.AuthorityUnavailable)
	case "secret-released":
		subject = fmt.Sprintf("secret %s released to swim %d (guard %s, approved by %s)", l.Secret, l.Lane, l.Guard, l.Method)
		field("Job", l.Job)
		for _, a := range l.Approvals {
			field("Approval", a)
		}
	case "secret-refused":
		subject = fmt.Sprintf("secret %s refused to swim %d", l.Secret, l.Lane)
		field("Job", l.Job)
		field("Why", l.Action)
	case "repair":
		subject = "repair: " + l.Action
		for name := range l.Outputs {
			field("Kept", ".swim/"+name)
		}
	case "amend":
		signer := who(l.Signer)
		subject = fmt.Sprintf("amend: %s acknowledged by %s (not the original signers)", plural(len(l.Amended), "gap"), signer)
		for _, a := range l.Amended {
			field("Amended", a)
		}
	default:
		subject = l.Type
	}
	return subject, body
}

// historyRefs decorates an entry's header like git log's refs: its seq,
// run and lane, the round's result in its colour.
func historyRefs(p ui.Painter, l chain.Link) string {
	refs := []string{fmt.Sprintf("#%d", l.Seq)}
	if l.Run != "" {
		refs = append(refs, "run "+l.Run)
	}
	if l.Lane > 0 {
		refs = append(refs, fmt.Sprintf("swim %d", l.Lane))
	}
	if l.Result != "" {
		refs = append(refs, p.Paint(ui.StateColor(l.Result)+ui.Bold, l.Result))
	}
	return p.Paint(ui.Yellow, " (") + strings.Join(refs, p.Paint(ui.Yellow, ", ")) + p.Paint(ui.Yellow, ")")
}

// gitDate renders an RFC 3339 time as git log does, in local time.
func gitDate(at string) string {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return at
	}
	return t.Local().Format("Mon Jan 2 15:04:05 2006 -0700")
}

// paged writes through $PAGER (default less -FRX, as git) when stdout is a
// terminal, and straight to stdout otherwise or if the pager won't start.
func paged(off bool, write func(w io.Writer)) error {
	pager := or(os.Getenv("SWIM_PAGER"), os.Getenv("PAGER"))
	if off || !ui.IsTTY(os.Stdout) || pager == "cat" {
		write(os.Stdout)
		return nil
	}
	if pager == "" {
		pager = "less"
	}
	c := exec.Command("sh", "-c", pager)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	if os.Getenv("LESS") == "" {
		c.Env = append(os.Environ(), "LESS=FRX")
	}
	in, err := c.StdinPipe()
	if err != nil {
		write(os.Stdout)
		return nil
	}
	if err := c.Start(); err != nil {
		write(os.Stdout)
		return nil
	}
	write(in)
	in.Close()
	c.Wait() // quitting the pager early is not an error
	return nil
}

func short12(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return or(s, "-")
}

// checkEntries checks what each chain entry recorded against what's on
// disk: every round's kept script, staged copy, inputs and log section,
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
	// Runs swim run started (their rounds ran staged copies), and those
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
					v.bad = append(v.bad, fmt.Sprintf("entry #%d: a round of swim %d in run %s, which the run's attestation (entry %s) doesn't list", l.Seq, l.Lane, l.Run, seqs(att)))
				case !slices.ContainsFunc(listed, ran):
					a := listed[0]
					if l.Script != "" && a.Scripts[key] != "" && l.Script != a.Scripts[key] {
						v.bad = append(v.bad, fmt.Sprintf("entry #%d: swim %d's round in run %s names script %s, but the run ran %s (entry #%d)", l.Seq, l.Lane, l.Run, short12(l.Script), short12(a.Scripts[key]), a.Seq))
					} else {
						v.bad = append(v.bad, fmt.Sprintf("entry #%d: swim %d's round in run %s names manifest %s, but the run ran %s (entry #%d)", l.Seq, l.Lane, l.Run, short12(l.Manifest), short12(a.Manifests[key]), a.Seq))
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
			// The round's staged copy, while it's kept (swim doctor --fix
			// prunes old stages): its manifest and files as they ran.
			if l.Manifest != "" {
				if m, err := manifest.Load(manifest.Dir(root, l.Run, l.Lane)); err == nil {
					switch {
					case m.SHA256 != l.Manifest && restaged(links, l, m.SHA256):
						// The lane ran again under the same run id
						// (--run-id): the stage is the later round's.
					case m.SHA256 != l.Manifest:
						v.bad = append(v.bad, fmt.Sprintf("entry #%d: the staged manifest of swim %d in run %s is %s, not the %s that ran", l.Seq, l.Lane, l.Run, short12(m.SHA256), short12(l.Manifest)))
					default:
						if err := m.Verify(); err != nil {
							v.bad = append(v.bad, fmt.Sprintf("entry #%d: swim %d's staged copy in run %s: %v", l.Seq, l.Lane, l.Run, err))
						}
					}
				}
			}
			if l.Log != "" {
				// A lane run twice under one run id (--run-id) has a section
				// for each round: this one's must be among them.
				switch sums := roundLogHashes(root, l.Lane, l.Run); {
				case !sums[l.Log] && prunedAt[l.Log] != "":
					v.notes = append(v.notes, fmt.Sprintf("entry #%d: swim %d's log for run %s was pruned on %s (swim prune): its contents can't be rechecked", l.Seq, l.Lane, l.Run, prunedAt[l.Log]))
				case len(sums) == 0:
					logsGone = append(logsGone, l.Seq) // one line for them all, below
				case !sums[l.Log]:
					v.bad = append(v.bad, fmt.Sprintf("entry #%d: swim %d's log for run %s was changed after the round ended", l.Seq, l.Lane, l.Run))
				}
			}
		case "secret-released":
			// A guarded secret goes out only on an approval swim run
			// recorded: one its attestation doesn't list wasn't.
			for _, h := range l.Approvals {
				listed := slices.ContainsFunc(att, func(a chain.Link) bool { return slices.Contains(a.Approvals, h) })
				if attested && !listed {
					v.bad = append(v.bad, fmt.Sprintf("entry #%d: secret %s released to swim %d in run %s on approval %s, which swim run didn't record (its attestation, entry %s, doesn't list it)", l.Seq, l.Secret, l.Lane, l.Run, short12(h), seqs(att)))
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
				v.bad = append(v.bad, fmt.Sprintf("entry #%d: decision %s for swim %d in run %s, which swim run started but never attested: nothing vouches that swim run recorded it", l.Seq, l.Guard, l.Lane, l.Run))
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
					v.bad = append(v.bad, fmt.Sprintf("entry #%d: decision %s for swim %d in run %s that swim run didn't record (its attestation, entry %s, doesn't list it)", l.Seq, l.Guard, l.Lane, l.Run, seqs(att)))
				case len(by) > 1:
					v.bad = append(v.bad, fmt.Sprintf("entry #%d: decision %s for swim %d in run %s that two attestations claim (entries %s)", l.Seq, l.Guard, l.Lane, l.Run, seqs(by)))
				}
			}
			if l.Choice < 1 || l.Choice > len(l.Choices) {
				v.bad = append(v.bad, fmt.Sprintf("entry #%d: decision %s for swim %d records choice %d of %d", l.Seq, l.Guard, l.Lane, l.Choice, len(l.Choices)))
			}
		case "approve":
			v.approvals++
			if !attested && launchedRun[l.Run] && !cutShort[l.Run] {
				v.bad = append(v.bad, fmt.Sprintf("entry #%d: an approval of %s for swim %d in run %s, which swim run started but never attested: nothing vouches that swim run recorded it", l.Seq, or(l.Guard, "a confirm"), l.Lane, l.Run))
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
					v.bad = append(v.bad, fmt.Sprintf("entry #%d: an approval of %s for swim %d in run %s that swim run didn't record (its attestation, entry %s, doesn't list it)", l.Seq, or(l.Guard, "a confirm"), l.Lane, l.Run, seqs(att)))
				case len(by) > 1:
					v.bad = append(v.bad, fmt.Sprintf("entry #%d: an approval of %s for swim %d in run %s that two attestations claim (entries %s): only the swim run that recorded it lists it", l.Seq, or(l.Guard, "a confirm"), l.Lane, l.Run, seqs(by)))
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
