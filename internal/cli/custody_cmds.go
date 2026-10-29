package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/manifest"
	"github.com/gates-brightly/swimlane/internal/outputs"
	"github.com/gates-brightly/swimlane/internal/sigfile"
	"github.com/gates-brightly/swimlane/internal/sign"
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
		return listApprovals(root, cfg, lk)
	}
	var approves []string
	for _, g := range strings.Split(guards, ",") {
		if g = strings.TrimSpace(g); g != "" {
			approves = append(approves, g)
		}
	}
	sort.Strings(approves)
	// A guard whose lookup.yml rule asks for evidence-bound approvals
	// can't be approved by anything any more: don't sign what won't count.
	for _, g := range approves {
		if why := lk.EvidenceWhy(g); why != "" {
			return fmt.Errorf("--guards %s: %s", g, why)
		}
	}
	var f *sigfile.File
	var scriptGuards []string
	var showPinned *manifest.Manifest // printed for a new approval
	if auto != "" {
		if len(rest) > 0 {
			return usagef("swim approve --auto USER takes no lanes (it approves every job USER queues)")
		}
		if _, ok := lk.Users[auto]; !ok {
			return fmt.Errorf("--auto %s: no user %s in lookup.yml (standing approvals are for people swim can identify)", auto, auto)
		}
		fresh := func() *sigfile.File {
			f := &sigfile.File{Kind: sigfile.Standing, For: auto}
			f.BindRules(lk)
			return f
		}
		f, err = loadOrNew(sigfile.StandingPath(root, auto), fresh)
		// One file per user: a standing approval that no longer counts
		// (revoked, expired, stale, outlasting standing.max) gives way to a
		// new one. Its events stay in the chain of custody.
		if err == nil && len(f.Events) > 0 {
			if st := f.State(lk, time.Now(), sigfile.Current{}); st.Name == sigfile.Revoked || st.Name == sigfile.Expired || st.Name == sigfile.Stale || f.LifetimeWhy(lk) != "" {
				fmt.Printf("replacing %s (%s)\n", rel(root, f.Path), st)
				path := f.Path
				f = fresh()
				f.Path = path
			}
		}
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
		f, err = loadOrNew(path, func() *sigfile.File {
			return &sigfile.File{Kind: sigfile.Script, Script: filepath.Base(lane.Script(root, n)), SHA256: sha, Lane: n, Job: info.Job, Round: info.Round,
				Manifest: man, Pinned: &m.Pinned}
		})
		if err == nil && len(f.Events) == 0 {
			showPinned = m // once the arguments check out
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
		if showPinned != nil {
			printPinned(showPinned)
			printKept(root, showPinned.Lane)
		}
		f.Approves, f.All, f.Note = approves, all, note
		life, err := approvalLifetime(lk, f.Kind, expires)
		if err != nil {
			return err
		}
		if life > 0 {
			f.Expires = time.Now().Add(life).UTC().Format(time.RFC3339)
		}
	} else if (len(approves) > 0 && strings.Join(approves, ",") != strings.Join(f.Approves, ",")) || (all && !f.All) || expires != "" {
		return fmt.Errorf("%s already states what it approves (%s); approving again signs that statement as is (drop --guards/--all/--expires, or revoke and start a new one)", rel(root, f.Path), approvesText(f))
	}
	key, err := sign.LoadKey(or(keyPath, cfg.SigningKey))
	if err != nil {
		return err
	}
	name, known := lk.Who(key.Public)
	if !known && lk.Path == "" {
		// A repo's first approval: an unlisted key counts for nothing, so
		// name it, rather than leave a signature that silently doesn't.
		user := localUser()
		if _, err := lookup.AddKey(root, user, key.Public); err != nil {
			return err
		}
		if lk, err = lookup.Load(root); err != nil {
			return err
		}
		name, known = lk.Who(key.Public)
		fmt.Printf("wrote lookup.yml: %s signs with %s (commit it; swim keys add NAME KEY for teammates)\n", user, sign.Fingerprint(key.Public))
	}
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

// defaultStanding is how long a standing approval lasts unless --expires
// says otherwise (or lookup.yml's standing.max is shorter).
const defaultStanding = 30 * 24 * time.Hour

// approvalLifetime is how long a new approval of kind lasts (0: until
// revoked): --expires D, or for a standing approval 30 days by default,
// never more than lookup.yml's standing.max.
func approvalLifetime(lk *lookup.Lookup, kind, expires string) (time.Duration, error) {
	var d time.Duration
	if expires != "" {
		var err error
		if d, err = lookup.ParseLifetime(expires); err != nil {
			return 0, usagef("--expires: %v", err)
		}
	}
	if kind != sigfile.Standing {
		return d, nil
	}
	max := lk.MaxStanding
	switch {
	case d > 0 && max > 0 && d > max:
		return 0, fmt.Errorf("--expires %s is longer than lookup.yml's standing.max (%s)", expires, lk.Standing.Max)
	case d > 0:
		return d, nil
	case max > 0 && max < defaultStanding:
		d = max
	default:
		d = defaultStanding
	}
	fmt.Printf("standing approval expires in %s (--expires D to change)\n", lifetimeText(d))
	return d, nil
}

// lifetimeText is "30d", or a Go duration when not whole days.
func lifetimeText(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	}
	return d.String()
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
		text += " (matrix cell " + strings.Join(f.Cells, ", ") + " only: approves nothing now)"
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
		if why := lk.EvidenceWhy(g); why != "" {
			fmt.Println("  " + p.Paint(ui.Red, "never:    "+g+": "+why))
			continue
		}
		d := sigfile.Decide([]*sigfile.File{f}, lk, g, time.Now())
		r := lk.RuleFor(g)
		in, out := lk.Counted(r, d.Users)
		var notes string
		if len(out) > 0 {
			isnt := " signed but isn't in " + r.Group + ", so doesn't count"
			if len(out) > 1 {
				isnt = " signed but aren't in " + r.Group + ", so don't count"
			}
			notes = "; " + strings.Join(out, ", ") + isnt
		}
		if d.OK {
			fmt.Println("  " + p.Paint(ui.Green, "approved: "+g+" ("+strings.Join(in, ", ")+notes+")"))
		} else {
			fmt.Println("  " + p.Paint(ui.Yellow, "not yet:  "+g+": "+d.Why+notes))
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
	fmt.Printf("  config   %s (swim.yml: %s)\n", short12(m.Pinned.Config.SHA256), or(strings.Join(m.Pinned.Config.Keys, ", "), "no policy keys set"))
	if m.Pinned.Config.Rules != "" {
		fmt.Printf("  rules    %s (lookup.yml require:)\n", short12(m.Pinned.Config.Rules))
	}
}

// printKept says which of the round's steps its next run keeps rather
// than runs (run --checkpoint), for whoever approves it.
func printKept(root string, n int) {
	if keys := laneCheckpoints(root, n); len(keys) > 0 {
		fmt.Printf("  kept     %s that passed in an earlier run won't run again: %s (swim checkpoint %d)\n", plural(len(keys), "step"), strings.Join(keys, ", "), n)
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
	if f.Manifest != "" && !m.Matches(f.Manifest) {
		return f.StaleReason(m)
	}
	return ""
}

// approvalState is f's state against its lane as it is now (see
// sigfile.State). mans caches each lane's manifest.
func approvalState(root string, cfg *config.Config, lk *lookup.Lookup, f *sigfile.File, mans map[int]*manifest.Manifest) sigfile.State {
	cur := sigfile.Current{}
	if f.Kind == sigfile.Script {
		m, ok := mans[f.Lane]
		if !ok {
			m, _ = manifest.Build(root, cfg, f.Lane, "")
			mans[f.Lane] = m
		}
		cur.Script, cur.Manifest = "(none)", m
		if m != nil {
			cur.Script = m.ScriptSHA
		}
	}
	return f.State(lk, time.Now(), cur)
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
		}
		st := approvalState(root, cfg, lk, f, mans)
		what += " · " + st.String()
		exp := ""
		if f.Expires != "" {
			exp = " · expires " + f.Expires
		}
		fmt.Printf("%s\n  approves %s · approved by %s%s · %d events (%s)\n", what, approvesText(f), or(strings.Join(names, ", "), "nobody now"), exp, len(f.Events), rel(root, f.Path))
		for _, b := range bad {
			fmt.Println("  BROKEN: " + b)
		}
		for _, w := range st.Warnings {
			fmt.Println("  warning: " + w)
		}
	}
	return nil
}

// cmdKeys shows who's who: lookup.yml's users, keys and groups, trust.yml
// and its pin, and the key this machine signs with. swim keys pin and
// swim keys trust look after trust.yml (trustcmd.go).
func cmdKeys(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "pin":
			return keysPin(args[1:])
		case "trust":
			return keysTrust(args[1:])
		case "add":
			return keysAdd(args[1:])
		}
		return usagef("usage: swim keys [add NAME [KEY] | pin | trust KEY [--remove] [--sign] [--key K]]")
	}
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
	fmt.Println(trustLine(root))
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
		ref := taskRef(root, cfg, rest[0])
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
			rows = append(rows, row{l.Entry, l.Hash, who(lk, l.SignedBy())})
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
			fmt.Fprintln(w, "Signer: "+who(lk, l.SignedBy()))
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
		field("Scheduled for", l.ScheduledFor)
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
		signer := sign.Fingerprint(l.Signer)
		if name, ok := lk.Who(l.Signer); ok {
			signer = name
		}
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
		if by := l.SignedBy(); by != "" {
			if _, ok := v.lk.Who(by); !ok {
				v.unlisted[sign.Fingerprint(by)] = true
			}
		}
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
			// What it read must be what its producers recorded.
			for key, sum := range l.Inputs {
				from := l.InputRounds[strings.SplitN(key, ":", 2)[0]]
				name := strings.SplitN(key, ":", 2)[1]
				prod, ok := producedBy(links, from)
				switch {
				case !ok:
					v.warn = append(v.warn, fmt.Sprintf("entry #%d: input %s came from round %s, which isn't in the chain", l.Seq, key, from))
				case prod.Outputs[name] != sum && prod.Outputs[outputs.ValuePrefix+name] != sum:
					v.bad = append(v.bad, fmt.Sprintf("entry #%d: input %s doesn't match what round %s produced (entry #%d): changed after it was produced", l.Seq, key, from, prod.Seq))
				}
			}
			// Each step it kept must have passed in the run it names, as
			// that run's log (hashed into its own round entry) shows.
			for _, k := range l.Kept {
				why, gone := keptWhy(root, links, l, k)
				switch {
				case gone:
					v.warn = append(v.warn, fmt.Sprintf("entry #%d: swim %d kept %q from run %s, whose log is gone: the pass can't be rechecked", l.Seq, l.Lane, k.Key, k.Run))
				case why != "":
					v.bad = append(v.bad, fmt.Sprintf("entry #%d: swim %d kept %q from run %s, but %s", l.Seq, l.Lane, k.Key, k.Run, why))
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
		case "run":
			// A scheduled run starts no earlier than its time; its run id
			// says when it started.
			if started := runStarted(l.Run); l.ScheduledFor != "" && started != "" && started < l.ScheduledFor {
				v.bad = append(v.bad, fmt.Sprintf("entry #%d: run %s started at %s, before its scheduled time %s", l.Seq, l.Run, started, l.ScheduledFor))
			}
		case "repair":
			// Not a break: the removed bytes were never a complete entry.
			v.notes = append(v.notes, fmt.Sprintf("entry #%d: swim chain repair %s", l.Seq, l.Action))
		case "decide":
			// Under swim run only the launcher records a decision, as an
			// approval: one its run's attestation doesn't list was written
			// by someone else.
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
			for _, ap := range l.Approvers {
				if ap.Statement == "" || ap.Sig == "" {
					continue
				}
				if l.Method == approval.HubSigned {
					// An answer signed through the hub: its statement must be
					// for this guard and lane, approving.
					if why := hubStatementWhy(ap.Statement, l); why != "" {
						v.bad = append(v.bad, fmt.Sprintf("entry #%d: %s's answer through the hub %s", l.Seq, or(ap.Name, "an approver"), why))
					} else if err := sigfile.VerifyHubSig(ap.Statement, ap.Key, ap.Sig); err != nil {
						v.bad = append(v.bad, fmt.Sprintf("entry #%d: %s's answer through the hub: %v", l.Seq, or(ap.Name, "an approver"), err))
					}
					continue
				}
				ev := sigfile.Event{Type: sigfile.Approve, Key: ap.Key, At: ap.SignedAt, Prev: ap.Prev, Sig: ap.Sig}
				if err := sigfile.VerifyEvent(ap.Statement, ev); err != nil {
					v.bad = append(v.bad, fmt.Sprintf("entry #%d: %s's approval signature: %v", l.Seq, or(ap.Name, "an approver"), err))
				}
			}
		}
	}
}

// hubStatementWhy says how a hub answer's statement doesn't approve
// entry l's guard ("" when it does): another guard, lane or run, or a
// decline.
func hubStatementWhy(st string, l chain.Link) string {
	switch {
	case !strings.HasPrefix(st, "swim hub answer v1\n"):
		return "isn't a hub answer statement"
	case approval.StatementField(st, "decision") != approval.Approve:
		return "doesn't approve (decision: " + approval.StatementField(st, "decision") + ")"
	case approval.StatementField(st, "flag") != l.Guard:
		return "is for guard " + approval.StatementField(st, "flag") + ", not " + l.Guard
	case approval.StatementField(st, "lane") != strconv.Itoa(l.Lane):
		return "is for swim " + approval.StatementField(st, "lane")
	case l.Run != "" && approval.StatementField(st, "run") != l.Run:
		return "is for run " + approval.StatementField(st, "run")
	}
	return ""
}

// checkApprovalFiles checks every approvals file under approvals/ event by
// event, and lists each (and whether a script approval is stale).
func (v *verifier) checkApprovalFiles() {
	root := v.root
	files, ferrs := sigfile.All(root)
	for _, e := range ferrs {
		v.bad = append(v.bad, e.Error())
	}
	for _, f := range files {
		signers, broken := f.Standing(v.lk)
		for _, b := range broken {
			v.bad = append(v.bad, rel(root, f.Path)+": "+b)
		}
		state := ""
		switch st := approvalState(root, v.cfg, v.lk, f, v.mans); {
		case st.Name == sigfile.Stale && st.Why == "the script changed since":
			state = "; stale: " + lane.ScriptName(root, f.Lane) + " changed since"
		case st.Name != sigfile.Active:
			state = "; " + st.String()
		default:
			for _, w := range st.Warnings {
				v.warn = append(v.warn, rel(root, f.Path)+": "+w)
			}
		}
		v.files = append(v.files, fmt.Sprintf("approvals %s: %s, approves %s; %d standing of %d events%s", rel(root, f.Path), f.Kind, approvesText(f), len(signers), len(f.Events), state))
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

// keptWhy says how kept step k of round entry l isn't backed by an
// earlier pass ("" if it is): a round of the same lane in run k.Run before
// l in the chain, whose log shows the step passing with the same command
// hash. gone: that run's log isn't there to check.
func keptWhy(root string, links []chain.Link, l chain.Link, k chain.KeptStep) (why string, gone bool) {
	earlier := slices.ContainsFunc(links, func(e chain.Link) bool {
		return e.Type == "round" && e.Lane == l.Lane && e.Run == k.Run && e.Seq < l.Seq
	})
	if !earlier {
		return "the chain has no earlier round of it in that run", false
	}
	found := false
	for _, f := range append(archives(root, l.Lane), lane.Log(root, l.Lane)) {
		rounds, _ := roundsOf(f, k.Run)
		for _, lines := range rounds {
			found = true
			r, err := logparse.ParseRound(lines)
			if err != nil {
				continue
			}
			for _, res := range r.Results {
				if res.Kind == logparse.Pass && res.Checkpoint == k.Cmd && or(res.Key, res.Label) == k.Key {
					return "", false
				}
			}
		}
	}
	if !found {
		return "", true
	}
	return "that run's log has no pass of it with the same command", false
}

// runStarted is when a run started, from its id (r-20261015T121857Z-...),
// as RFC 3339; "" for an id that doesn't say (--run-id).
func runStarted(run string) string {
	stamp, ok := strings.CutPrefix(run, "r-")
	if !ok || len(stamp) < 16 {
		return ""
	}
	t, err := time.Parse("20060102T150405Z", stamp[:16])
	if err != nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// swim keys add NAME [KEY|FILE.pub]: list a key for NAME in lookup.yml
// (default: the key this machine signs with).
func keysAdd(args []string) error {
	if len(args) == 0 || len(args) > 2 {
		return usagef("usage: swim keys add NAME [KEY | FILE.pub]")
	}
	user := args[0]
	if !validUserName(user) {
		return usagef("%q: a name is letters, digits, '.', '_' or '-'", user)
	}
	root, cfg, err := repoNoMigrate()
	if err != nil {
		return err
	}
	var pub string
	switch {
	case len(args) == 2 && len(strings.Fields(args[1])) >= 2:
		pub = args[1]
	case len(args) == 2:
		data, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		pub = string(data)
	default:
		key, err := sign.LoadKey(cfg.SigningKey)
		if err != nil {
			return fmt.Errorf("no key named and none to sign with: %v", err)
		}
		pub = key.Public
	}
	created, err := lookup.AddKey(root, user, pub)
	if err != nil {
		return err
	}
	verb := "added to"
	if created {
		verb = "wrote"
	}
	fmt.Printf("%s lookup.yml: %s signs with %s (commit it)\n", verb, user, sign.Fingerprint(pub))
	return nil
}

// localUser names this machine's user for lookup.yml: $USER, made a valid
// name, or "me".
func localUser() string {
	u := os.Getenv("USER")
	if u == "" {
		if cu, err := user.Current(); err == nil {
			u = cu.Username
		}
	}
	var b strings.Builder
	for _, r := range strings.ToLower(u) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "me"
	}
	return b.String()
}

func validUserName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
