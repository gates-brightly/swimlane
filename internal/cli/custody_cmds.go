package cli

import (
	"errors"
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
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/evidence"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/launcher"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/manifest"
	"github.com/gates-brightly/swimlane/internal/outputs"
	"github.com/gates-brightly/swimlane/internal/sigfile"
	"github.com/gates-brightly/swimlane/internal/sign"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// laneFile is a lane script named by path (lane.3.py, ./lane.3.sh).
func laneFile(arg string) (int, bool) {
	if !strings.HasPrefix(filepath.Base(arg), "lane.") {
		return 0, false
	}
	n, _, ok := lane.ParseScriptName(filepath.Base(arg))
	return n, ok
}

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
	var guards, expires, keyPath, note, auto, cell string
	var all, noEvidence bool
	rest, err := flags{bools: map[string]*bool{"all": &all, "no-evidence": &noEvidence},
		strs: map[string]*string{"guards": &guards, "expires": &expires, "key": &keyPath, "note": &note, "auto": &auto, "cell": &cell}}.parse(args)
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
		return listApprovals(root, cfg, lk)
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
		if k, ok := laneFile(rest[0]); ok {
			n = k
		} else if n, err = laneRef(root, cfg, rest[0], false); err != nil {
			return err
		}
		data, err := lane.ScriptBytes(root, n)
		if err != nil {
			return fmt.Errorf("swim %d has no lane script", n)
		}
		info, err := lane.ReadScript(root, n)
		if err != nil {
			return err
		}
		scriptGuards = info.GuardFlags()
		// The approval binds the execution manifest: the script and every
		// file it reads as code, its sidecar, how it runs and the policy in
		// swim.yml and lookup.yml.
		m, err := manifest.Build(root, cfg, n, "")
		if err != nil {
			return err
		}
		sha := chain.Hash(data)
		man := m.SHA256
		known := map[string]bool{}
		for _, g := range scriptGuards {
			known[g] = true
		}
		for _, g := range approves {
			if !known[g] {
				return fmt.Errorf("--guards %s: %s has no guard %s (its guards: %s)", g, lane.ScriptName(root, n), g, or(strings.Join(scriptGuards, ", "), "none"))
			}
		}
		path := sigfile.ScriptPath(root, n, sha, man)
		var cells []string
		if cell != "" {
			// One cell of a matrix round.
			c, ok := info.Cell(cell)
			if !info.IsMatrix() || !ok {
				return fmt.Errorf("--cell %s: swim %d has no such cell (a matrix round's cells: %s)", cell, n, strings.Join(launcher.CellNames(info.Cells()), ", "))
			}
			cells, path = []string{c.Name}, sigfile.CellPath(root, n, sha, man, c.Name)
		}
		// A round with evidence steps (or a guard whose rule needs it) is
		// approved together with its latest evidence: the plan, not just
		// the script.
		evHash := ""
		if !noEvidence && (info.UsesEvidence || needsEvidence(lk, approves, all, scriptGuards)) {
			ev, err := approvalEvidence(root, n, info, sha, cells)
			if err != nil {
				return err
			}
			evHash = ev.Hash
			cellName := ""
			if len(cells) > 0 {
				cellName = cells[0]
			}
			path = sigfile.EvidencePath(root, n, sha, man, ev.Hash, cellName)
			text, _ := ev.Text(root)
			fmt.Printf("evidence from %s at %s:\n", map[bool]string{true: "swim plan --check", false: "a run"}[ev.Check], ev.At)
			for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
				fmt.Println("  " + l)
			}
			fmt.Printf("approving %s (script %s, evidence %s)\n", lane.ScriptName(root, n), short12(sha), short12(ev.Hash))
		}
		f, err = loadOrNew(path, func() *sigfile.File {
			return &sigfile.File{Kind: sigfile.Script, Script: filepath.Base(lane.Script(root, n)), SHA256: sha, Lane: n, Job: info.Job, Round: info.Round, Cells: cells, Evidence: evHash,
				Manifest: man, Pinned: &m.Pinned}
		})
		if err == nil && len(f.Events) == 0 {
			printPinned(m)
		}
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
	what := fmt.Sprintf("%s %s", lane.ScriptName(root, f.Lane), short12(f.SHA256))
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
	text := strings.Join(f.Approves, ", ")
	if f.All {
		text = "every guard"
	}
	if len(f.Cells) > 0 {
		text += " (cell " + strings.Join(f.Cells, ", ") + " only)"
	}
	return text
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

// printPinned shows what an approval of manifest m covers.
func printPinned(m *manifest.Manifest) {
	fmt.Printf("approving manifest %s:\n", short12(m.SHA256))
	fmt.Printf("  script   %s %s\n", m.Pinned.Script.Path, short12(m.Pinned.Script.SHA256))
	for _, f := range m.Pinned.Includes {
		fmt.Printf("  includes %s %s (%s)\n", f.Path, short12(f.SHA256), f.From)
	}
	if sc := m.Pinned.Sidecar; sc != nil {
		fmt.Printf("  sidecar  %s %s\n", sc.Path, short12(sc.SHA256))
	}
	fmt.Printf("  runner   %s\n", m.Pinned.Runner)
	fmt.Printf("  config   %s (swim.yml: %s)\n", short12(m.Pinned.Config.SHA256), strings.Join(m.Pinned.Config.Keys, ", "))
	if m.Pinned.Config.Rules != "" {
		fmt.Printf("  rules    %s (lookup.yml require:)\n", short12(m.Pinned.Config.Rules))
	}
}

// staleWhy says why script approval f no longer applies to its lane as it
// is now ("" if it still does): the script changed, or (bound to a
// manifest) something else it pins did. mans caches each lane's manifest.
func staleWhy(root string, cfg *config.Config, f *sigfile.File, mans map[int]*manifest.Manifest) string {
	if f.Kind != sigfile.Script {
		return ""
	}
	m, ok := mans[f.Lane]
	if !ok {
		m, _ = manifest.Build(root, cfg, f.Lane, "")
		mans[f.Lane] = m
	}
	if m == nil || m.ScriptSHA != f.SHA256 {
		return "the script changed since"
	}
	if f.Manifest != "" && f.Manifest != m.SHA256 {
		return f.StaleReason(m)
	}
	return ""
}

// listApprovals shows every approvals file: what it approves and whose
// approvals stand.
func listApprovals(root string, cfg *config.Config, lk *lookup.Lookup) error {
	files, errs := sigfile.All(root)
	for _, e := range errs {
		fmt.Fprintln(os.Stderr, "swim approve: "+e.Error())
	}
	if len(files) == 0 {
		fmt.Println("no approvals yet: swim approve N --guards FLAG (this script) or swim approve --auto USER (standing)")
		return nil
	}
	mans := map[int]*manifest.Manifest{}
	for _, f := range files {
		signers, bad := f.Standing(lk)
		var names []string
		for _, s := range signers {
			names = append(names, or(s.User, "unknown "+sign.Fingerprint(s.Key)))
		}
		what := fmt.Sprintf("swim %d (%s %s)", f.Lane, lane.ScriptName(root, f.Lane), short12(f.SHA256))
		if f.Manifest != "" {
			what += " manifest " + short12(f.Manifest)
		}
		if f.Kind == sigfile.Standing {
			what = "standing: every job " + f.For + " queues"
		} else if why := staleWhy(root, cfg, f, mans); why != "" {
			what += " · stale: " + why
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
			rows = append(rows, row{l.Entry, l.Hash, who(lk, l.Signer)})
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
			subject, body := historyEntry(lk, l)
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
			fmt.Fprintln(w, "Signer: "+who(lk, l.Signer))
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
func historyEntry(lk *lookup.Lookup, l chain.Link) (string, []string) {
	var names []string
	for _, a := range l.Approvers {
		names = append(names, or(a.Name, who(lk, a.Key)))
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
	case "run":
		var lanes []string
		for k := range l.Manifests {
			lanes = append(lanes, k)
		}
		sort.Strings(lanes)
		subject = fmt.Sprintf("run %s attested: swim %s, %d approvals", l.Run, strings.Join(lanes, " "), len(l.Approvals))
		for _, k := range lanes {
			field("Lane "+k, l.Manifests[k])
		}
		for _, a := range l.Approvals {
			field("Approval", a)
		}
		field("Flags", strings.Join(l.Flags, " "))
	case "repair":
		subject = "repair: " + l.Action
		for name := range l.Outputs {
			field("Kept", ".swim/"+name)
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
	root, cfg, err := repoNoMigrate()
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
	mans := map[int]*manifest.Manifest{}
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
	var notes []string
	runs, unattested := attestations(links, &bad)
	for _, l := range links {
		if l.Signer != "" {
			if _, ok := lk.Who(l.Signer); !ok {
				signers[sign.Fingerprint(l.Signer)] = true
			}
		}
		att, attested := runs[l.Run]
		switch l.Type {
		case "round":
			rounds++
			// What swim run attests its lane ran.
			if attested {
				key := strconv.Itoa(l.Lane)
				switch {
				case att.Manifests[key] == "" && att.Scripts[key] == "":
					bad = append(bad, fmt.Sprintf("entry #%d: a round of swim %d in run %s, which the run's attestation (entry #%d) doesn't list", l.Seq, l.Lane, l.Run, att.Seq))
				case l.Script != "" && att.Scripts[key] != "" && l.Script != att.Scripts[key]:
					bad = append(bad, fmt.Sprintf("entry #%d: swim %d's round in run %s names script %s, but the run ran %s (entry #%d)", l.Seq, l.Lane, l.Run, short12(l.Script), short12(att.Scripts[key]), att.Seq))
				case l.Manifest != "" && att.Manifests[key] != "" && l.Manifest != att.Manifests[key]:
					bad = append(bad, fmt.Sprintf("entry #%d: swim %d's round in run %s names manifest %s, but the run ran %s (entry #%d)", l.Seq, l.Lane, l.Run, short12(l.Manifest), short12(att.Manifests[key]), att.Seq))
				}
			}
			if l.Script != "" {
				if got, err := chain.HashFile(filepath.Join(scriptsDir(root), l.Script+".sh")); err != nil {
					warn = append(warn, fmt.Sprintf("entry #%d: the script copy %s.sh is missing", l.Seq, short12(l.Script)))
				} else if got != l.Script {
					bad = append(bad, fmt.Sprintf("entry #%d: the kept script %s.sh was changed", l.Seq, short12(l.Script)))
				}
			}
			// The round's staged copy, while it's kept (swim doctor --fix
			// prunes old stages): its manifest and files as they ran.
			if l.Manifest != "" {
				if m, err := manifest.Load(manifest.Dir(root, l.Run, l.Lane)); err == nil {
					if m.SHA256 != l.Manifest {
						bad = append(bad, fmt.Sprintf("entry #%d: the staged manifest of swim %d in run %s is %s, not the %s that ran", l.Seq, l.Lane, l.Run, short12(m.SHA256), short12(l.Manifest)))
					} else if err := m.Verify(); err != nil {
						bad = append(bad, fmt.Sprintf("entry #%d: swim %d's staged copy in run %s: %v", l.Seq, l.Lane, l.Run, err))
					}
				}
			}
			// What it read must be what its producers recorded.
			for key, sum := range l.Inputs {
				from := l.InputRounds[strings.SplitN(key, ":", 2)[0]]
				name := strings.SplitN(key, ":", 2)[1]
				prod, ok := producedBy(links, from)
				switch {
				case !ok:
					warn = append(warn, fmt.Sprintf("entry #%d: input %s came from round %s, which isn't in the chain", l.Seq, key, from))
				case prod.Outputs[name] != sum && prod.Outputs[outputs.ValuePrefix+name] != sum:
					bad = append(bad, fmt.Sprintf("entry #%d: input %s doesn't match what round %s produced (entry #%d): changed after it was produced", l.Seq, key, from, prod.Seq))
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
		case "repair":
			// Not a break: the removed bytes were never a complete entry.
			notes = append(notes, fmt.Sprintf("entry #%d: swim chain repair %s", l.Seq, l.Action))
		case "approve":
			approvals++
			// Under swim run only the launcher records approvals: one its
			// run's attestation doesn't list was written by someone else (a
			// lane recording its own approval).
			if attested && !slices.Contains(att.Approvals, l.Hash) {
				bad = append(bad, fmt.Sprintf("entry #%d: an approval of %s for swim %d in run %s that swim run didn't record (its attestation, entry #%d, doesn't list it)", l.Seq, or(l.Guard, "a confirm"), l.Lane, l.Run, att.Seq))
			}
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
		if why := staleWhy(root, cfg, f, mans); why == "the script changed since" {
			state = "; stale: " + lane.ScriptName(root, f.Lane) + " changed since"
		} else if why != "" {
			state = "; stale: " + why
		}
		fmt.Printf("approvals %s: %s, approves %s; %d standing of %d events%s\n", rel(root, f.Path), f.Kind, approvesText(f), len(signers), len(f.Events), state)
	}
	if unattested > 0 {
		notes = append(notes, fmt.Sprintf("%d runs have approvals or rounds but no run attestation (lanes run directly with bash, runs from before attestations, or a swim run that didn't finish): their approvals can't be checked against what swim run recorded", unattested))
	}
	for _, n := range notes {
		fmt.Println("note: " + n)
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

// attestations are the chain's run attestations by run id. A run attested
// twice is a break (someone appended a second). unattested counts the runs
// that have approve or round entries and no attestation.
func attestations(links []chain.Link, bad *[]string) (runs map[string]chain.Link, unattested int) {
	runs = map[string]chain.Link{}
	for _, l := range links {
		if l.Type != "run" {
			continue
		}
		if first, ok := runs[l.Run]; ok {
			*bad = append(*bad, fmt.Sprintf("entry #%d: run %s is attested twice (entries #%d and #%d)", l.Seq, l.Run, first.Seq, l.Seq))
			continue
		}
		runs[l.Run] = l
	}
	seen := map[string]bool{}
	for _, l := range links {
		if (l.Type == "approve" || l.Type == "round") && !seen[l.Run] {
			seen[l.Run] = true
			if _, ok := runs[l.Run]; !ok {
				unattested++
			}
		}
	}
	return runs, unattested
}

// needsEvidence reports whether a lookup.yml rule for any approved guard
// requires evidence-bound approval.
func needsEvidence(lk *lookup.Lookup, approves []string, all bool, scriptGuards []string) bool {
	flags := approves
	if all {
		flags = scriptGuards
	}
	for _, f := range flags {
		if lk.RuleFor(f).Evidence {
			return true
		}
	}
	return false
}

// approvalEvidence is the latest evidence of lane n's round, for an
// approval of the script with hash sha: refused when there's none, or it
// was gathered for another version of the script.
func approvalEvidence(root string, n int, info lane.Info, sha string, cells []string) (*evidence.Index, error) {
	job := info.Job
	if len(cells) > 0 {
		if c, ok := info.Cell(cells[0]); ok {
			job = info.ForCell(c).Job
		}
	}
	if job == "" {
		if st, _ := status.Load(root); st != nil {
			if l := st.Get(n); l != nil {
				job = l.Job
				if len(cells) > 0 {
					job = l.CellEntry(cells[0]).Job
				}
			}
		}
	}
	ref := fmt.Sprint(n)
	if len(cells) > 0 {
		info, _ := lane.ReadScript(root, n)
		ref = "'" + info.SubLabel(n, cells[0]) + "'"
	}
	key := evidence.LaneKey(n, "")
	if len(cells) > 0 {
		key = evidence.LaneKey(n, lane.CellSlug(cells[0]))
	}
	ev := evidence.Load(root, job)
	if lv := evidence.LoadLane(root, key); lv != nil && (ev == nil || ev.Script != sha || lv.At > ev.At) {
		ev = lv // the lane's latest (a round with no fixed job)
	}
	switch {
	case ev == nil || ev.Hash == "":
		return nil, fmt.Errorf("swim %d has evidence steps but no evidence yet: run swim plan --check %s first, then approve what it shows (--no-evidence approves the script only)", n, ref)
	case ev.Script != sha:
		return nil, fmt.Errorf("swim %d's evidence (%s) is for another version of its script: run swim plan --check %s again (--no-evidence approves the script only)", n, ev.At, ref)
	}
	return ev, nil
}

// producedBy is the round entry for "<job> <run>".
func producedBy(links []chain.Link, round string) (chain.Link, bool) {
	job, run, _ := strings.Cut(round, " ")
	for _, l := range links {
		if l.Type == "round" && l.Job == job && l.Run == run {
			return l, true
		}
	}
	return chain.Link{}, false
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
