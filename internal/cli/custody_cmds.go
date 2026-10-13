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
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/sigfile"
	"github.com/gates-brightly/swimlane/internal/sign"
	"github.com/gates-brightly/swimlane/internal/ui"
)

var laneFileRE = regexp.MustCompile(`(?:^|/)lane\.([0-9]+)\.sh$`)

// cmdApprove is how approvers act:
//
//	swim approve                                  list approvals and standing approvals
//	swim approve N|JOB [--guards A,B | --all]     approve this exact lane script
//	swim approve --auto USER [--guards A,B|--all] standing approval of USER's queued jobs
//
// Each adds a signed event to a file under approvals/ (created if new;
// otherwise co-signed). --key picks the key, --expires D and --note T
// describe a new approval.
func cmdApprove(args []string) error {
	var guards, expires, keyPath, note, auto string
	var all bool
	rest, err := flags{bools: map[string]*bool{"all": &all},
		strs: map[string]*string{"guards": &guards, "expires": &expires, "key": &keyPath, "note": &note, "auto": &auto}}.parse(args)
	if err != nil {
		return err
	}
	root, cfg, err := repoNoMigrate()
	if err != nil {
		return err
	}
	lk, err := lookup.Load(root)
	if err != nil {
		return err
	}
	if auto == "" && len(rest) == 0 {
		return listApprovals(root, lk)
	}
	var approves []string
	for _, g := range strings.Split(guards, ",") {
		if g = strings.TrimSpace(g); g != "" {
			approves = append(approves, g)
		}
	}
	sort.Strings(approves)
	var f *sigfile.File
	var scriptGuards []string
	if auto != "" {
		if len(rest) > 0 {
			return usagef("swim approve --auto USER takes no lanes (it approves every job USER queues)")
		}
		if _, ok := lk.Users[auto]; !ok {
			return fmt.Errorf("--auto %s: no user %s in lookup.yml (standing approvals are for people swim can identify)", auto, auto)
		}
		f, err = loadOrNew(sigfile.StandingPath(root, auto), func() *sigfile.File {
			return &sigfile.File{Kind: sigfile.Standing, For: auto}
		})
	} else {
		if len(rest) != 1 {
			return usagef("usage: swim approve N|JOB [--guards A,B | --all]  or  swim approve --auto USER")
		}
		var n int
		if m := laneFileRE.FindStringSubmatch(rest[0]); m != nil {
			n, _ = strconv.Atoi(m[1])
		} else if n, err = laneRef(root, cfg, rest[0], false); err != nil {
			return err
		}
		data, err := os.ReadFile(lane.Script(root, n))
		if err != nil {
			return fmt.Errorf("swim %d has no lane script", n)
		}
		info, err := lane.ReadScript(root, n)
		if err != nil {
			return err
		}
		scriptGuards = info.GuardFlags()
		sha := chain.Hash(data)
		known := map[string]bool{}
		for _, g := range scriptGuards {
			known[g] = true
		}
		for _, g := range approves {
			if !known[g] {
				return fmt.Errorf("--guards %s: lane.%d.sh has no guard %s (its guards: %s)", g, n, g, or(strings.Join(scriptGuards, ", "), "none"))
			}
		}
		f, err = loadOrNew(sigfile.ScriptPath(root, n, sha), func() *sigfile.File {
			return &sigfile.File{Kind: sigfile.Script, Script: filepath.Base(lane.Script(root, n)), SHA256: sha, Lane: n, Job: info.Job, Round: info.Round}
		})
	}
	if err != nil {
		return err
	}
	if len(f.Events) == 0 {
		// A new approval states what it approves.
		if len(approves) == 0 && !all {
			if auto == "" {
				return usagef("say what this approves: --guards FLAG[,FLAG] or --all")
			}
			all = true // a standing approval defaults to every guard
		}
		f.Approves, f.All, f.Note = approves, all, note
		if expires != "" {
			d, err := time.ParseDuration(expires)
			if err != nil || d <= 0 {
				return usagef("--expires takes a duration like 24h, got %q", expires)
			}
			f.Expires = time.Now().Add(d).UTC().Format(time.RFC3339)
		}
	} else if (len(approves) > 0 && strings.Join(approves, ",") != strings.Join(f.Approves, ",")) || (all && !f.All) || expires != "" {
		return fmt.Errorf("%s already states what it approves (%s); approving again signs that statement as is (drop --guards/--all/--expires, or revoke and start a new one)", rel(root, f.Path), approvesText(f))
	}
	key, err := sign.LoadKey(or(keyPath, cfg.SigningKey))
	if err != nil {
		return err
	}
	name, known := lk.Who(key.Public)
	if f.HasApproved(lk, key.Public) {
		fmt.Printf("you already approve %s; nothing to add\n", rel(root, f.Path))
		return nil
	}
	if err := f.Add(key, sigfile.Approve, name); err != nil {
		return err
	}
	if err := f.Save(); err != nil {
		return err
	}
	recordGrant(root, cfg, f, sigfile.Approve, key, name)
	who := name
	if !known {
		who = "your key " + sign.Fingerprint(key.Public) + " (not in lookup.yml: it won't count until it's listed)"
	}
	what := fmt.Sprintf("lane.%d.sh %s", f.Lane, short12(f.SHA256))
	if f.Kind == sigfile.Standing {
		what = "every job " + f.For + " queues (standing; revoke with swim revoke --auto " + f.For + ")"
	}
	fmt.Printf("approved %s as %s: approves %s (%s)\n", what, who, approvesText(f), rel(root, f.Path))
	printDecision(lk, f, scriptGuards)
	return nil
}

// cmdRevoke withdraws your approval: swim revoke N|JOB, or swim revoke
// --auto USER. It adds a signed revoke event; the approval stops counting.
func cmdRevoke(args []string) error {
	var keyPath, auto string
	rest, err := flags{strs: map[string]*string{"key": &keyPath, "auto": &auto}}.parse(args)
	if err != nil {
		return err
	}
	root, cfg, err := repoNoMigrate()
	if err != nil {
		return err
	}
	lk, err := lookup.Load(root)
	if err != nil {
		return err
	}
	key, err := sign.LoadKey(or(keyPath, cfg.SigningKey))
	if err != nil {
		return err
	}
	var files []*sigfile.File
	switch {
	case auto != "" && len(rest) == 0:
		files = sigfile.StandingFor(root, auto)
	case auto == "" && len(rest) == 1:
		n, err := laneRef(root, cfg, rest[0], true)
		if err != nil {
			return err
		}
		// Every version of the lane's script you approve: revoking means
		// you no longer approve this lane's change.
		all, _ := sigfile.All(root)
		for _, f := range all {
			if f.Kind == sigfile.Script && f.Lane == n {
				files = append(files, f)
			}
		}
	default:
		return usagef("usage: swim revoke N|JOB  or  swim revoke --auto USER")
	}
	name, _ := lk.Who(key.Public)
	revoked := 0
	for _, f := range files {
		if !f.HasApproved(lk, key.Public) {
			continue
		}
		if err := f.Add(key, sigfile.Revoke, name); err != nil {
			return err
		}
		if err := f.Save(); err != nil {
			return err
		}
		recordGrant(root, cfg, f, sigfile.Revoke, key, name)
		fmt.Printf("revoked your approval in %s\n", rel(root, f.Path))
		revoked++
	}
	if revoked == 0 {
		return fmt.Errorf("you have no standing approval there to revoke")
	}
	return nil
}

func loadOrNew(path string, fresh func() *sigfile.File) (*sigfile.File, error) {
	f, err := sigfile.Load(path)
	if errors.Is(err, os.ErrNotExist) {
		f = fresh()
		f.Path = path
		return f, nil
	}
	return f, err
}

// recordGrant puts an approve or revoke action in the chain of custody.
func recordGrant(root string, cfg *config.Config, f *sigfile.File, typ string, key *sign.Key, name string) {
	e := chain.Entry{Type: "grant", Method: typ, Lane: f.Lane, Job: f.Job, Script: f.SHA256, Statement: f.Statement(),
		Approvers: []chain.Approver{{Name: name, Key: key.Public}}}
	if typ == sigfile.Revoke {
		e.Type = "revoke"
	}
	if f.Kind == sigfile.Standing {
		e.Action = "standing approval for jobs queued by " + f.For
	}
	record(root, cfg, e)
}

func approvesText(f *sigfile.File) string {
	if f.All {
		return "every guard"
	}
	return strings.Join(f.Approves, ", ")
}

// printDecision says, per guard, whether the approval is enough yet.
func printDecision(lk *lookup.Lookup, f *sigfile.File, scriptGuards []string) {
	p := ui.Painter{On: ui.ColorEnabled(os.Stdout)}
	flags := f.Approves
	if f.All {
		flags = scriptGuards
	}
	for _, g := range flags {
		d := sigfile.Decide([]*sigfile.File{f}, lk, g, time.Now())
		if d.OK {
			fmt.Println("  " + p.Paint(ui.Green, "approved: "+g+" ("+strings.Join(d.Users, ", ")+")"))
		} else {
			fmt.Println("  " + p.Paint(ui.Yellow, "not yet:  "+g+": "+d.Why))
		}
	}
}

// listApprovals shows every approvals file: what it approves and whose
// approvals stand.
func listApprovals(root string, lk *lookup.Lookup) error {
	files, errs := sigfile.All(root)
	for _, e := range errs {
		fmt.Fprintln(os.Stderr, "swim approve: "+e.Error())
	}
	if len(files) == 0 {
		fmt.Println("no approvals yet: swim approve N --guards FLAG (this script) or swim approve --auto USER (standing)")
		return nil
	}
	for _, f := range files {
		signers, bad := f.Standing(lk)
		var names []string
		for _, s := range signers {
			names = append(names, or(s.User, "unknown "+sign.Fingerprint(s.Key)))
		}
		what := fmt.Sprintf("swim %d (lane.%d.sh %s)", f.Lane, f.Lane, short12(f.SHA256))
		if f.Kind == sigfile.Standing {
			what = "standing: every job " + f.For + " queues"
		} else if data, err := os.ReadFile(lane.Script(root, f.Lane)); err != nil || chain.Hash(data) != f.SHA256 {
			what += " · stale: the script changed since"
		}
		fmt.Printf("%s\n  approves %s · approved by %s · %d events (%s)\n", what, approvesText(f), or(strings.Join(names, ", "), "nobody now"), len(f.Events), rel(root, f.Path))
		for _, b := range bad {
			fmt.Println("  BROKEN: " + b)
		}
	}
	return nil
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
		case "grant", "revoke":
			var names []string
			for _, a := range l.Approvers {
				names = append(names, or(a.Name, who(lk, a.Key)))
			}
			what := l.Action
			if what == "" {
				what = fmt.Sprintf("swim %d script %s", l.Lane, short12(l.Script))
			}
			verb, color := "approved", ui.Green
			if l.Type == "revoke" {
				verb, color = "revoked", ui.Yellow
			}
			fmt.Printf("#%-4d %s  %s  %s  by %s\n", l.Seq, l.At, p.Paint(color, verb), what, strings.Join(names, ", "))
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
			for _, ap := range l.Approvers {
				if ap.Statement == "" || ap.Sig == "" {
					continue
				}
				ev := sigfile.Event{Type: sigfile.Approve, Key: ap.Key, At: ap.SignedAt, Prev: ap.Prev, Sig: ap.Sig}
				if err := sigfile.VerifyEvent(ap.Statement, ev); err != nil {
					bad = append(bad, fmt.Sprintf("entry #%d: %s's approval signature: %v", l.Seq, or(ap.Name, "an approver"), err))
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
		signers, broken := f.Standing(lk)
		for _, b := range broken {
			bad = append(bad, rel(root, f.Path)+": "+b)
		}
		state := ""
		if f.Kind == sigfile.Script {
			if data, err := os.ReadFile(lane.Script(root, f.Lane)); err != nil || chain.Hash(data) != f.SHA256 {
				state = "; stale: lane." + strconv.Itoa(f.Lane) + ".sh changed since"
			}
		}
		fmt.Printf("approvals %s: %s, approves %s; %d standing of %d events%s\n", rel(root, f.Path), f.Kind, approvesText(f), len(signers), len(f.Events), state)
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
