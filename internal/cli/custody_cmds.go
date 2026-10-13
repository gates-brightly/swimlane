package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/sigfile"
	"github.com/gates-brightly/swimlane/internal/sign"
	"github.com/gates-brightly/swimlane/internal/ui"
)

var laneFileRE = regexp.MustCompile(`(?:^|/)lane\.([0-9]+)\.sh$`)

// cmdSign makes or co-signs a remote approval for one lane script:
//
//	swim sign N|JOB|lane.N.sh [--guards A,B | --all] [--expires D] [--key K] [--note T] [-o FILE]
func cmdSign(args []string) error {
	var guards, expires, keyPath, note, out string
	var all bool
	args = append([]string(nil), args...)
	for i, a := range args {
		if a == "-o" {
			args[i] = "--out"
		}
	}
	rest, err := flags{bools: map[string]*bool{"all": &all},
		strs: map[string]*string{"guards": &guards, "expires": &expires, "key": &keyPath, "note": &note, "out": &out}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return usagef("usage: swim sign N|JOB|lane.N.sh [--guards A,B | --all] [--expires D] [--key K] [--note T] [-o FILE]")
	}
	root, cfg, err := repoNoMigrate()
	if err != nil {
		return err
	}
	var n int
	if m := laneFileRE.FindStringSubmatch(rest[0]); m != nil {
		n, _ = strconv.Atoi(m[1])
	} else if n, err = laneRef(root, cfg, rest[0], false); err != nil {
		return err
	}
	script := lane.Script(root, n)
	data, err := os.ReadFile(script)
	if err != nil {
		return fmt.Errorf("swim %d has no lane script (%s)", n, filepath.Base(script))
	}
	info, err := lane.ReadScript(root, n)
	if err != nil {
		return err
	}
	sha := chain.Hash(data)
	if out == "" {
		out = script + ".signature"
	}
	lk, err := lookup.Load(root)
	if err != nil {
		return err
	}
	var approves []string
	for _, g := range strings.Split(guards, ",") {
		if g = strings.TrimSpace(g); g != "" {
			approves = append(approves, g)
		}
	}
	sort.Strings(approves)

	f, err := sigfile.Load(out)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// A new approval: it must say what it approves.
		if len(approves) == 0 && !all {
			return usagef("say what this approves: --guards FLAG[,FLAG] or --all")
		}
		known := map[string]bool{}
		for _, g := range info.Guards {
			known[g.Flag] = true
		}
		for _, g := range approves {
			if !known[g] {
				return fmt.Errorf("--guards %s: lane.%d.sh has no guard %s (its guards: %s)", g, n, g, or(strings.Join(info.GuardFlags(), ", "), "none"))
			}
		}
		f = &sigfile.File{Path: out, Script: filepath.Base(script), SHA256: sha, Lane: n, Job: info.Job, Round: info.Round,
			Approves: approves, All: all, Note: note}
		if expires != "" {
			d, err := time.ParseDuration(expires)
			if err != nil || d <= 0 {
				return usagef("--expires takes a duration like 24h, got %q", expires)
			}
			f.Expires = time.Now().Add(d).UTC().Format(time.RFC3339)
		}
	case err != nil:
		return err
	default:
		// Co-signing: the statement must stay exactly what others signed.
		if f.SHA256 != sha {
			return fmt.Errorf("%s approves a different version of lane.%d.sh (signed %s, now %s): the script changed since; remove the file to approve the new one", filepath.Base(out), n, f.SHA256[:12], sha[:12])
		}
		if (len(approves) > 0 && strings.Join(approves, ",") != strings.Join(f.Approves, ",")) || (all && !f.All) || expires != "" {
			return fmt.Errorf("%s already states what it approves (%s); co-signing signs that statement as is (drop --guards/--all/--expires)", filepath.Base(out), approvesText(f))
		}
	}
	key, err := sign.LoadKey(or(keyPath, cfg.SigningKey))
	if err != nil {
		return err
	}
	name, known := lk.Who(key.Public)
	if err := f.Sign(key, name); err != nil {
		return err
	}
	if err := f.Save(); err != nil {
		return err
	}
	who := name
	if !known {
		who = "your key " + sign.Fingerprint(key.Public) + " (not in lookup.yml: it won't count until it's listed)"
	}
	fmt.Printf("signed %s as %s: lane.%d.sh %s, approves %s\n", rel(root, out), who, n, sha[:12], approvesText(f))
	printDecisions(root, lk, f, info.GuardFlags())
	return nil
}

func approvesText(f *sigfile.File) string {
	if f.All {
		return "every guard"
	}
	return strings.Join(f.Approves, ", ")
}

// printDecisions says, per guard, whether the file approves it yet.
func printDecisions(root string, lk *lookup.Lookup, f *sigfile.File, scriptGuards []string) {
	p := ui.Painter{On: ui.ColorEnabled(os.Stdout)}
	flags := f.Approves
	if f.All {
		flags = scriptGuards
	}
	for _, g := range flags {
		d := f.Decide(lk, g, time.Now())
		if d.OK {
			fmt.Println("  " + p.Paint(ui.Green, "approved: "+g+" (signed by "+strings.Join(d.Users, ", ")+")"))
		} else {
			fmt.Println("  " + p.Paint(ui.Yellow, "not yet:  "+g+": "+d.Why))
		}
	}
}

// cmdKeys shows who's who: lookup.yml's users, keys and groups, and the
// key this machine signs with.
func cmdKeys(args []string) error {
	root, cfg, err := repoNoMigrate()
	if err != nil {
		return err
	}
	lk, err := lookup.Load(root)
	if err != nil {
		return err
	}
	if lk.Path == "" {
		fmt.Println("no lookup.yml in this repo: add one to name keys and group them (swim keys --help)")
	} else {
		var users []string
		for u := range lk.Users {
			users = append(users, u)
		}
		sort.Strings(users)
		fmt.Println("users (" + rel(root, lk.Path) + "):")
		for _, u := range users {
			fmt.Printf("  %s\n", lk.Describe(u))
			for _, k := range lk.Users[u].Keys {
				fmt.Printf("    %s  %s\n", sign.Fingerprint(k), keyComment(k))
			}
		}
		var groups []string
		for g := range lk.Groups {
			groups = append(groups, g)
		}
		sort.Strings(groups)
		if len(groups) > 0 {
			fmt.Println("groups:")
			for _, g := range groups {
				fmt.Printf("  %s: %s\n", g, strings.Join(lk.Members(g), ", "))
			}
		}
		if len(lk.Require) > 0 {
			fmt.Println("require (signed approvals):")
			for _, r := range lk.Require {
				who := "users in lookup.yml"
				if r.Group != "" {
					who = r.Group
				}
				gs := "every guard"
				if len(r.Guards) > 0 {
					gs = strings.Join(r.Guards, ", ")
				}
				fmt.Printf("  %s: %d from %s\n", gs, max(r.Count, 1), who)
			}
		}
	}
	if k, err := sign.LoadKey(cfg.SigningKey); err == nil {
		who := "not in lookup.yml"
		if u, ok := lk.Who(k.Public); ok {
			who = lk.Describe(u)
		}
		fmt.Printf("this machine signs with %s %s (%s); signing: %s\n", sign.Fingerprint(k.Public), k.Path, who, cfg.SigningMode())
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

// who names a key: "zach (platform-admin)", or its fingerprint.
func who(lk *lookup.Lookup, key string) string {
	if key == "" {
		return "unsigned"
	}
	if u, ok := lk.Who(key); ok {
		return lk.Describe(u)
	}
	return "unknown key " + sign.Fingerprint(key)
}

// cmdHistory shows the chain of custody, oriented around who approved and
// who ran what: swim history [RUN|N|JOB] [--yaml].
func cmdHistory(args []string) error {
	var asYAML bool
	rest, err := flags{bools: map[string]*bool{"yaml": &asYAML}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return usagef("usage: swim history [RUN|N|JOB] [--yaml]")
	}
	root, cfg, err := repoNoMigrate()
	if err != nil {
		return err
	}
	lk, err := lookup.Load(root)
	if err != nil {
		return err
	}
	links, err := chain.Read(root)
	if err != nil {
		return err
	}
	match := func(l chain.Link) bool { return true }
	if len(rest) == 1 {
		ref := rest[0]
		if n, err := strconv.Atoi(ref); err == nil {
			if _, err := laneArg(cfg, ref); err != nil {
				return err
			}
			match = func(l chain.Link) bool { return l.Lane == n }
		} else {
			match = func(l chain.Link) bool { return l.Run == ref || lane.MatchJob(l.Job, ref) }
		}
	}
	var shown []chain.Link
	for _, l := range links {
		if match(l) {
			shown = append(shown, l)
		}
	}
	if asYAML {
		type row struct {
			chain.Entry `yaml:",inline"`
			Hash        string `yaml:"hash"`
			SignedBy    string `yaml:"signed_by"`
		}
		var rows []row
		for _, l := range shown {
			rows = append(rows, row{l.Entry, l.Hash, who(lk, l.Signer)})
		}
		return yaml.NewEncoder(os.Stdout).Encode(map[string]any{"schema": "swim.history/v1", "entries": rows})
	}
	if len(shown) == 0 {
		fmt.Println("no chain of custody yet: swim records it as rounds run (" + rel(root, chain.Path(root)) + ")")
		return nil
	}
	p := ui.Painter{On: ui.ColorEnabled(os.Stdout)}
	for _, l := range shown {
		switch l.Type {
		case "approve":
			var names []string
			for _, a := range l.Approvers {
				names = append(names, or(a.Name, who(lk, a.Key)))
			}
			fmt.Printf("#%-4d %s  %s  swim %d  %s  %s  by %s via %s\n", l.Seq, l.At, p.Paint(ui.Green, "approve"), l.Lane, l.Guard, p.Paint(ui.Dim, l.Action), strings.Join(names, ", "), l.Method)
		case "round":
			exit := ""
			if l.Exit != nil {
				exit = fmt.Sprintf(" exit %d", *l.Exit)
			}
			fmt.Printf("#%-4d %s  %s  swim %d  %s%s  script %s  log %s  %d approval(s)\n", l.Seq, l.At, p.Paint(ui.StateColor(l.Result)+ui.Bold, "round"), l.Lane, l.Result, exit, short12(l.Script), short12(l.Log), len(l.Approvals))
		default:
			fmt.Printf("#%-4d %s  %s\n", l.Seq, l.At, l.Type)
		}
		fmt.Println(p.Paint(ui.Dim, fmt.Sprintf("      %s · run %s · signed by %s", short12(l.Hash), or(l.Run, "-"), who(lk, l.Signer))))
	}
	return nil
}

func short12(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return or(s, "-")
}

// cmdVerify proves (or disproves) the chain of custody: every link follows
// the one before it, every signature is valid, every round's log section
// and script are what was recorded, and every signed approval's signatures
// check out. Exits 1 on a break; --strict also on unsigned entries and
// signers not in lookup.yml.
func cmdVerify(args []string) error {
	var strict bool
	rest, err := flags{bools: map[string]*bool{"strict": &strict}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return usagef("usage: swim verify [--strict]")
	}
	root, _, err := repoNoMigrate()
	if err != nil {
		return err
	}
	lk, err := lookup.Load(root)
	if err != nil {
		return err
	}
	if !sign.Available() {
		return errors.New("swim verify needs ssh-keygen (OpenSSH 8.0 or later)")
	}
	links, rerr := chain.Read(root)
	p := ui.Painter{On: ui.ColorEnabled(os.Stdout)}
	var bad, warn []string
	if rerr != nil {
		bad = append(bad, rerr.Error())
	}
	problems, unsigned := chain.Check(links)
	for _, pr := range problems {
		bad = append(bad, fmt.Sprintf("entry #%d (line %d): %s", pr.Seq, pr.Line, pr.What))
	}
	if len(unsigned) > 0 {
		warn = append(warn, fmt.Sprintf("%d unsigned entries (linked, but no key vouches for them): #%s", len(unsigned), joinNums(unsigned)))
	}
	signers := map[string]bool{}
	rounds, approvals := 0, 0
	for _, l := range links {
		if l.Signer != "" {
			if _, ok := lk.Who(l.Signer); !ok {
				signers[sign.Fingerprint(l.Signer)] = true
			}
		}
		switch l.Type {
		case "round":
			rounds++
			if l.Script != "" {
				if got, err := chain.HashFile(filepath.Join(scriptsDir(root), l.Script+".sh")); err != nil {
					warn = append(warn, fmt.Sprintf("entry #%d: the script copy %s.sh is missing", l.Seq, short12(l.Script)))
				} else if got != l.Script {
					bad = append(bad, fmt.Sprintf("entry #%d: the kept script %s.sh was changed", l.Seq, short12(l.Script)))
				}
			}
			if l.Log != "" {
				switch sum, ok := roundLogHash(root, l.Lane, l.Run); {
				case !ok:
					warn = append(warn, fmt.Sprintf("entry #%d: swim %d's log for run %s is gone (deleted, not archived)", l.Seq, l.Lane, l.Run))
				case sum != l.Log:
					bad = append(bad, fmt.Sprintf("entry #%d: swim %d's log for run %s was changed after the round ended", l.Seq, l.Lane, l.Run))
				}
			}
		case "approve":
			approvals++
			if l.Method == "signature" {
				for _, a := range l.Approvers {
					if err := sigfile.VerifyStatement(l.Statement, a.Key, a.SignedAt, a.Sig); err != nil {
						bad = append(bad, fmt.Sprintf("entry #%d: %s's approval signature: %v", l.Seq, or(a.Name, "an approver"), err))
					}
				}
			}
		}
	}
	for fp := range signers {
		warn = append(warn, "entries signed by a key not in lookup.yml: "+fp)
	}
	fmt.Printf("chain of custody: %d entries (%d rounds, %d approvals) in %s\n", len(links), rounds, approvals, rel(root, chain.Path(root)))
	files, ferrs := sigfile.All(root)
	for _, e := range ferrs {
		bad = append(bad, e.Error())
	}
	for _, f := range files {
		cur := ""
		if data, err := os.ReadFile(lane.Script(root, f.Lane)); err == nil {
			cur = chain.Hash(data)
		}
		state := "matches lane." + strconv.Itoa(f.Lane) + ".sh"
		if cur != f.SHA256 {
			state = "stale: lane." + strconv.Itoa(f.Lane) + ".sh changed since it was signed"
		}
		if _, badSigs := f.Verify(lk); len(badSigs) > 0 {
			bad = append(bad, rel(root, f.Path)+": "+strings.Join(badSigs, "; "))
		}
		fmt.Printf("signature %s: approves %s; %d signatories; %s\n", rel(root, f.Path), approvesText(f), len(f.Signatories), state)
	}
	for _, w := range warn {
		fmt.Println(p.Paint(ui.Yellow, "warning: "+w))
	}
	for _, b := range bad {
		fmt.Println(p.Paint(ui.Red, "BROKEN: "+b))
	}
	if len(bad) > 0 || strict && len(warn) > 0 {
		return exitError{1}
	}
	fmt.Println(p.Paint(ui.Green, "verified: every link, signature, log and script checks out"))
	return nil
}

// roundLogHash is the hash of lane n's round in run, from its current or
// archived log (ok false if no log holds it).
func roundLogHash(root string, n int, run string) (string, bool) {
	for _, f := range append(archives(root, n), lane.Log(root, n)) {
		rounds, _ := roundsOf(f, run)
		if len(rounds) > 0 {
			return chain.Hash([]byte(roundSection(rounds[len(rounds)-1]))), true
		}
	}
	return "", false
}
