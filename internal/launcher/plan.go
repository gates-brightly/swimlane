package launcher

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/observe"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// plan is what swim run would do, before it's rendered.
type plan struct {
	what      string
	sel       []int
	passed    []int
	selected  map[int]bool
	scripts   map[int]lane.Info
	children  map[int][]int
	inRunDeps map[int][]int
	skip      map[int]string
	actions   map[int]planAction
	counts    map[planAction]int
	// guards is how swim run would decide each lane's guards now, and
	// awaiting the lane whose unapproved guard each lane depends on (0:
	// none): a dry run there skips it.
	guards   map[int][]GuardVerdict
	awaiting map[int]int
}

// planFacts are what a plan is decided from, as gatherPlan reads them from
// the repo (scripts, status, the queue, the config). decidePlan reads
// nothing else, so plan decisions can be checked without a repo.
type planFacts struct {
	what        string
	sel, passed []int
	lanes       int  // the config's lane count
	named       bool // lanes were named (swim run N), not swim all
	scripts     map[int]lane.Info
	status      map[int]*status.Lane   // nil for a lane with no status
	deps        map[int][]Dep          // each selected lane's dependencies
	blocked     map[int]string         // selected lanes refused before they run (BlockedLanes)
	outside     map[Dep]string         // why a dependency outside the run blocks ("" if it doesn't)
	guards      map[int][]GuardVerdict // how swim run would decide each lane's guards now
}

func computePlan(o Options) (*plan, error) {
	f, err := gatherPlan(o)
	if err != nil {
		return nil, err
	}
	return decidePlan(f), nil
}

// gatherPlan reads what a plan is decided from.
func gatherPlan(o Options) (planFacts, error) {
	o.DryRun = true
	f := planFacts{lanes: o.Cfg.Lanes, named: len(o.Lanes) > 0, scripts: map[int]lane.Info{}, status: map[int]*status.Lane{},
		outside: map[Dep]string{}}
	var err error
	if f.sel, f.passed, err = Select(o); err != nil {
		return f, err
	}
	if f.deps, err = ResolveDeps(o.Root, o.Cfg, f.sel); err != nil {
		return f, err
	}
	st, _ := status.Load(o.Root)
	for n := 1; n <= o.Cfg.Lanes; n++ {
		f.scripts[n], _ = lane.ReadScript(o.Root, n)
		if st != nil {
			f.status[n] = st.Get(n)
		}
	}
	selected := map[int]bool{}
	for _, n := range f.sel {
		selected[n] = true
	}
	for _, n := range f.sel {
		for _, d := range f.deps[n] {
			if !selected[d.Lane] {
				f.outside[d] = outsideBlocker(o.Root, d)
			}
		}
	}
	f.blocked = BlockedLanes(o.Root, o.Cfg, f.sel)
	f.guards = planGuards(o, f.sel, f.scripts)
	f.what = "swim all"
	if len(o.Lanes) > 0 {
		parts := make([]string, len(o.Lanes))
		for i, n := range o.Lanes {
			parts[i] = fmt.Sprint(n)
		}
		f.what = "swim run " + strings.Join(parts, " ")
	} else if o.Rerun {
		f.what = "swim all --rerun"
	}
	return f, nil
}

// decidePlan decides, from f alone, what swim run would do with each lane:
// the tree, the predicted skips and each lane's action.
func decidePlan(f planFacts) *plan {
	sel, scripts := f.sel, f.scripts
	selected := map[int]bool{}
	for _, n := range sel {
		selected[n] = true
	}

	// Edges inside the run, and lanes predicted to be skipped.
	children := map[int][]int{}
	inRunDeps := map[int][]int{}
	blocked := map[int]string{}
	for _, n := range sel {
		for _, d := range f.deps[n] {
			if selected[d.Lane] {
				children[d.Lane] = append(children[d.Lane], n)
				inRunDeps[n] = append(inRunDeps[n], d.Lane)
			} else if why := f.outside[d]; why != "" && blocked[n] == "" {
				blocked[n] = why
			}
		}
	}
	for n, why := range f.blocked {
		blocked[n] = why
	}
	skip := map[int]string{}
	var skipReason func(n int) string
	skipReason = func(n int) string {
		if r, ok := skip[n]; ok {
			return r
		}
		skip[n] = "" // guards against revisiting; cycles were rejected already
		r := blocked[n]
		if r == "" {
			for _, d := range inRunDeps[n] {
				if skipReason(d) != "" {
					r = fmt.Sprintf("swim %d won't run", d)
					break
				}
			}
		}
		skip[n] = r
		return r
	}
	for _, n := range sel {
		skipReason(n)
	}

	// Each lane in the run gets a Terraform-style action.
	actions := map[int]planAction{}
	counts := map[planAction]int{}
	for _, n := range sel {
		a := actionFor(scripts[n], f.status[n], skip[n] != "")
		actions[n] = a
		counts[a]++
	}
	// Lanes whose round, or one they sit under, needs an approval: a dry
	// run there skips them. A lane skipped anyway needs none.
	awaiting := awaitingApproval(sel, inRunDeps, f.guards)
	for n := range awaiting {
		if actions[n] == actSkip {
			delete(awaiting, n)
		}
	}

	return &plan{what: f.what, sel: sel, passed: f.passed, selected: selected, scripts: scripts, children: children,
		inRunDeps: inRunDeps, skip: skip, actions: actions, counts: counts,
		guards: f.guards, awaiting: awaiting}
}

// WritePlan prints what `swim run` with these options would do, without
// running anything: a tree of the lanes that will run (each under the
// lanes it waits for), predicted skips, guard flags, and what won't run.
func WritePlan(w io.Writer, o Options, color bool) error {
	p := ui.Painter{On: color}
	pl, err := computePlan(o)
	if err != nil {
		return err
	}
	what, sel, passed, selected, scripts, children := pl.what, pl.sel, pl.passed, pl.selected, pl.scripts, pl.children
	known, _ := observe.LoadKnowledge(o.Root)
	if known == nil {
		known = &observe.KnowledgeFile{}
	}
	inRunDeps, skip, actions, counts := pl.inRunDeps, pl.skip, pl.actions, pl.counts
	fmt.Fprintln(w, p.Paint(ui.Bold, fmt.Sprintf("swim plan · %s · %s · lanes 1..%d", filepath.Base(o.Root), what, o.Cfg.Lanes)))
	fmt.Fprintln(w)
	if len(sel) > 0 {
		fmt.Fprintln(w, "Job actions are indicated with the following symbols:")
		for _, a := range []planAction{actRun, actRetry, actRerun, actSkip} {
			if counts[a] > 0 {
				fmt.Fprintf(w, "  %s %s\n", a.symbol(p), p.Paint(ui.Dim, a.legend()))
			}
		}
		fmt.Fprintln(w)
		how := "top-level lanes start at once; each lane starts when the lanes it sits under pass"
		fmt.Fprintf(w, "Swim would start these rounds (%s).\n", p.Paint(ui.Dim, how))
		fmt.Fprintln(w, p.Paint(ui.Dim, "A plan says what runs, in what order, and what each guard needs; what a script changes is up to the script."))
		fmt.Fprintln(w)
	}

	labelW := len(fmt.Sprintf("swim %d", o.Cfg.Lanes))
	shown := map[int]bool{}
	stopped := 0
	var walk func(n int, prefix, branch string)
	walk = func(n int, prefix, branch string) {
		info := scripts[n]
		label := p.Paint(ui.LaneColor(n)+ui.Bold, fmt.Sprintf("%-*s", labelW, fmt.Sprintf("swim %d", n)))
		sym := actions[n].symbol(p)
		if shown[n] {
			fmt.Fprintf(w, "%s%s%s %s %s\n", prefix, branch, sym, label, p.Paint(ui.Dim, "(shown above)"))
			return
		}
		shown[n] = true
		line := prefix + branch + sym + " " + label + "  " + info.Round
		if info.Job != "" {
			line += "  " + p.Paint(ui.Dim, lane.ShortJob(info.Job))
		}
		var notes []string
		if ds := inRunDeps[n]; len(ds) > 1 {
			parts := make([]string, len(ds))
			for i, d := range ds {
				parts[i] = fmt.Sprint(d)
			}
			notes = append(notes, p.Paint(ui.Dim, "wait ["+strings.Join(parts, ",")+"]"))
		}
		if pid, ok := lane.Running(o.Root, n); ok {
			notes = append(notes, p.Paint(ui.Red, fmt.Sprintf("RUNNING NOW (pid %d): swim run would refuse", pid)))
		}
		switch wa := pl.awaiting[n]; {
		case wa == n:
			notes = append(notes, p.Paint(ui.Yellow, "needs approval: a dry run without it"))
		case wa != 0:
			notes = append(notes, p.Paint(ui.Yellow, fmt.Sprintf("waits on swim %d's approval", wa)))
		}
		if r := skip[n]; r != "" {
			notes = append(notes, p.Paint(ui.Red, "skip: "+r))
		}
		if o.ShowChecks {
			if c := checkOf(o, n, inRunDeps[n]); c != nil {
				notes = append(notes, c.text(p))
				if c.Result == "stopped" {
					stopped++
				}
			}
		}
		if len(notes) > 0 {
			line += "  " + strings.Join(notes, "  ")
		}
		fmt.Fprintln(w, line)

		childPrefix := prefix
		switch branch {
		case "├── ":
			childPrefix += "│   "
		case "└── ":
			childPrefix += "    "
		}
		bar := "│ "
		if len(children[n]) == 0 {
			bar = "  "
		}
		for _, v := range pl.guards[n] {
			fmt.Fprintf(w, "%s%s%s %s (%s)%s\n", childPrefix, p.Paint(ui.Dim, bar), p.Paint(ui.Dim, "guard"), v.Flag, v.text(p), v.desc(p))
		}
		// Modules, with what they declare they do: an approver reads them
		// next to the guards.
		if len(info.Modules) > 0 {
			mods, err := planModules(o.Root, o.Cfg, info.Modules)
			for k, m := range mods {
				label := "        "
				if k == 0 {
					label = "modules "
				}
				fmt.Fprintf(w, "%s%s%s %s\n", childPrefix, p.Paint(ui.Dim, bar), p.Paint(ui.Dim, label), moduleText(m))
				if m.Lock != "" {
					fmt.Fprintf(w, "%s%s%s\n", childPrefix, p.Paint(ui.Dim, bar), p.Paint(ui.Red, "blocked: "+m.Lock))
				}
			}
			if err != nil {
				for _, e := range strings.Split(err.Error(), "\n") {
					fmt.Fprintf(w, "%s%s%s\n", childPrefix, p.Paint(ui.Dim, bar), p.Paint(ui.Red, "blocked: "+e))
				}
			}
		}
		if info.FailHard {
			fmt.Fprintf(w, "%s%s%s\n", childPrefix, p.Paint(ui.Dim, bar), p.Paint(ui.Dim, "fail: hard (the first failed step ends the round)"))
		}
		// What people accepted about it (swim observe accept): to read
		// before running, not a decision.
		for _, k := range observe.ForLane(known, n) {
			fmt.Fprintf(w, "%s%s%s\n", childPrefix, p.Paint(ui.Dim, bar), p.Paint(ui.Yellow, "known: "+k))
		}
		if info.Timeout > 0 {
			fmt.Fprintf(w, "%s%s%s\n", childPrefix, p.Paint(ui.Dim, bar), p.Paint(ui.Dim, "timeout "+info.TimeoutText))
		}
		if len(info.Locks) > 0 {
			fmt.Fprintf(w, "%s%s%s\n", childPrefix, p.Paint(ui.Dim, bar), p.Paint(ui.Dim, "locks "+strings.Join(info.Locks, ", ")))
		}
		if len(info.Cache) > 0 {
			fmt.Fprintf(w, "%s%s%s\n", childPrefix, p.Paint(ui.Dim, bar), p.Paint(ui.Dim, "cache "+cacheText(info.Cache)))
		}
		kids := children[n]
		sort.Ints(kids)
		for i, c := range kids {
			b := "├── "
			if i == len(kids)-1 {
				b = "└── "
			}
			walk(c, childPrefix, b)
		}
	}
	var roots []int
	for _, n := range sel {
		if len(inRunDeps[n]) == 0 {
			roots = append(roots, n)
		}
	}
	for _, n := range roots {
		walk(n, "", "")
	}

	// No-action items are summarised, not listed.
	var idle []string
	if len(o.Lanes) == 0 {
		for _, n := range o.Cfg.Active {
			if selected[n] || contains(passed, n) {
				continue
			}
			switch info := scripts[n]; {
			case !info.Exists:
				idle = append(idle, fmt.Sprintf("swim %d (no lane script)", n))
			case info.Stub:
				idle = append(idle, fmt.Sprintf("swim %d (stub)", n))
			default:
				idle = append(idle, fmt.Sprintf("swim %d (no Round: line)", n))
			}
		}
	}

	if len(sel) > 0 {
		fmt.Fprintln(w)
	}
	if len(sel) == 0 {
		fmt.Fprintln(w, p.Paint(ui.Green+ui.Bold, "No changes.")+" "+completedLine(len(passed)))
	} else {
		fmt.Fprintf(w, "%s %s to run, %s to retry, %s to rerun, %s to skip.\n", p.Paint(ui.Bold, "Plan:"),
			p.Paint(ui.Green, fmt.Sprint(counts[actRun])), p.Paint(ui.Yellow, fmt.Sprint(counts[actRetry])),
			p.Paint(ui.Mag, fmt.Sprint(counts[actRerun])), p.Paint(ui.Red, fmt.Sprint(counts[actSkip])))
		if len(passed) > 0 {
			fmt.Fprintln(w, completedLine(len(passed)))
		}
		if line := approvalsLine(pl); line != "" {
			fmt.Fprintln(w, p.Paint(ui.Bold, "Approvals:")+" "+line)
		}
		if o.ChecksRan {
			var guarded []string
			for _, n := range sel {
				if actions[n] != actSkip && len(scripts[n].Guards) > 0 {
					guarded = append(guarded, fmt.Sprintf("swim %d %s", n, strings.Join(scripts[n].GuardFlags(), " ")))
				}
			}
			line := fmt.Sprintf("Checks: %d would stop at a gate.", stopped)
			if len(guarded) > 0 {
				line += " Guarded changes: " + strings.Join(guarded, "; ") + "."
			}
			fmt.Fprintln(w, p.Paint(ui.Bold, line))
		}
	}
	if len(idle) > 0 {
		fmt.Fprintln(w, p.Paint(ui.Dim, "Idle lanes: "+strings.Join(idle, ", ")))
	}
	// Lanes that share a lock never run at the same time (in either order).
	byLock := map[string][]int{}
	for _, n := range sel {
		if actions[n] == actSkip {
			continue
		}
		for _, l := range scripts[n].Locks {
			byLock[l] = append(byLock[l], n)
		}
	}
	var lockNames []string
	for name, ls := range byLock {
		if len(ls) > 1 {
			lockNames = append(lockNames, name)
		}
	}
	sort.Strings(lockNames)
	for _, name := range lockNames {
		fmt.Fprintf(w, "%s %s: swim %s take turns (never at the same time)\n", p.Paint(ui.Yellow, "Lock"), name, joinInts(byLock[name], ", "))
	}
	return nil
}

func completedLine(n int) string {
	word := "items have"
	if n == 1 {
		word = "item has"
	}
	if n == 0 {
		return "Nothing pending."
	}
	return fmt.Sprintf("%d %s completed with no remaining work.", n, word)
}

// planAction is what `swim run` would do with one lane, Terraform style.
type planAction int

const (
	actRun   planAction = iota // [+]   new job, never run
	actRetry                   // [~]   ran before and didn't pass
	actRerun                   // [+/-] already passed; runs again
	actSkip                    // [-]   pending but won't run
)

func (a planAction) symbol(p ui.Painter) string {
	switch a {
	case actRetry:
		return p.Paint(ui.Yellow, "[~]")
	case actRerun:
		return p.Paint(ui.Mag, "[+/-]")
	case actSkip:
		return p.Paint(ui.Red, "[-]")
	}
	return p.Paint(ui.Green, "[+]")
}

func (a planAction) legend() string {
	switch a {
	case actRetry:
		return "retry: the job ran before and didn't pass"
	case actRerun:
		return "rerun: the job already passed and runs again"
	case actSkip:
		return "skip: the job won't run (a dependency won't pass)"
	}
	return "run: a new job"
}

// actionFor classifies a lane's pending job against its last run.
func actionFor(info lane.Info, last *status.Lane, skipped bool) planAction {
	if skipped {
		return actSkip
	}
	if AlreadyPassed(info, last) {
		return actRerun
	}
	if last != nil && last.State != status.Idle {
		same := info.Job != "" && last.Job == info.Job
		if info.Job == "" {
			same = last.Round == info.Round
		}
		if same {
			return actRetry
		}
	}
	return actRun
}

func contains(ns []int, n int) bool {
	for _, x := range ns {
		if x == n {
			return true
		}
	}
	return false
}

func (a planAction) name() string {
	return [...]string{"run", "retry", "rerun", "skip"}[a]
}

// PlanGuard is a guard flag a job's round declares, and how swim run
// would decide it now.
type PlanGuard struct {
	Flag string `yaml:"flag"`
	Set  bool   `yaml:"set"` // its flag is 1 in this environment
	Desc string `yaml:"description,omitempty"`
	// Approved: by its flag, signed approvals or -y (How says which).
	// Otherwise Asks says whether swim run would ask (else a dry run), and
	// Why why the signed approvals don't count yet.
	Approved bool   `yaml:"approved"`
	How      string `yaml:"how,omitempty"`
	Asks     bool   `yaml:"asks,omitempty"`
	Why      string `yaml:"why,omitempty"`
}

// PlanLane is one lane swim run would act on.
type PlanLane struct {
	Lane    int         `yaml:"lane"`
	Job     string      `yaml:"job,omitempty"`
	Round   string      `yaml:"round"`
	Action  string      `yaml:"action"` // run | retry | rerun | skip
	WaitsOn []int       `yaml:"waits_on,flow"`
	Reason  string      `yaml:"reason,omitempty"` // why it would be skipped
	Running int         `yaml:"running_pid,omitempty"`
	Guards  []PlanGuard `yaml:"guards,omitempty"`
	// AwaitsApproval is the lane (this one, or one it sits under) whose
	// guard isn't approved yet: a dry run there skips this round.
	AwaitsApproval int      `yaml:"awaits_approval,omitempty"`
	Timeout        string   `yaml:"timeout,omitempty"`
	Locks          []string `yaml:"locks,flow,omitempty"`
	// Cache are the tool caches the round uses (# Cache:), with the
	// lockfiles each is checked against.
	Cache []PlanCache `yaml:"cache,omitempty"`
	// Modules are the modules the round declares (# Modules:), and
	// ModuleError why they don't resolve (the lane won't start).
	Modules     []PlanModule `yaml:"modules,omitempty"`
	ModuleError string       `yaml:"module_error,omitempty"`
	// Check is the lane's last read-only check (swim plan --check).
	Check *PlanCheck `yaml:"check,omitempty"`
	// Claim is the lane's live claim (swim claim): a session may still be
	// writing its round.
	Claim *lane.Claim `yaml:"claim,omitempty"`
	Fail  string      `yaml:"fail,omitempty"` // hard, when Fail: hard
}

// PlanIdle is a lane with nothing to do.
type PlanIdle struct {
	Lane int    `yaml:"lane"`
	Why  string `yaml:"why"` // passed | stub | no lane script | no Round: line
}

// PlanDoc is swim plan --yaml.
type PlanDoc struct {
	Schema  string         `yaml:"schema"`
	Command string         `yaml:"command"`
	Lanes   []PlanLane     `yaml:"lanes"`
	Idle    []PlanIdle     `yaml:"idle"`
	Counts  map[string]int `yaml:"counts"`
}

// Plan returns what swim run with these options would do, as data.
func Plan(o Options) (*PlanDoc, error) {
	pl, err := computePlan(o)
	if err != nil {
		return nil, err
	}
	doc := &PlanDoc{Schema: "swim.plan/v1", Command: pl.what, Lanes: []PlanLane{}, Idle: []PlanIdle{},
		Counts: map[string]int{"run": 0, "retry": 0, "rerun": 0, "skip": 0}}
	for a, c := range pl.counts {
		doc.Counts[a.name()] = c
	}
	for _, n := range pl.sel {
		info := pl.scripts[n]
		l := PlanLane{Lane: n, Job: info.Job, Round: info.Round, Action: pl.actions[n].name(),
			WaitsOn: append([]int{}, pl.inRunDeps[n]...), Reason: pl.skip[n], Timeout: info.TimeoutText, Locks: info.Locks}
		if pid, ok := lane.Running(o.Root, n); ok {
			l.Running = pid
		}
		for _, d := range info.Cache {
			l.Cache = append(l.Cache, PlanCache{Preset: d.Label(), Lockfiles: d.Lockfiles})
		}
		for _, v := range pl.guards[n] {
			l.Guards = append(l.Guards, PlanGuard{Flag: v.Flag, Set: os.Getenv(v.Flag) == "1", Desc: v.Desc,
				Approved: v.Approved, How: v.How, Asks: v.Asks, Why: v.Why})
		}
		l.AwaitsApproval = pl.awaiting[n]
		if len(info.Modules) > 0 {
			var err error
			if l.Modules, err = planModules(o.Root, o.Cfg, info.Modules); err != nil {
				l.ModuleError = err.Error()
			}
		}
		if info.FailHard {
			l.Fail = "hard"
		}
		if o.ShowChecks {
			l.Check = checkOf(o, n, pl.inRunDeps[n])
		}
		l.Claim = lane.ReadClaim(o.Root, n)
		doc.Lanes = append(doc.Lanes, l)
	}
	sort.SliceStable(doc.Lanes, func(a, b int) bool { return doc.Lanes[a].Lane < doc.Lanes[b].Lane })
	for _, n := range pl.passed {
		doc.Idle = append(doc.Idle, PlanIdle{Lane: n, Why: "passed"})
	}
	if len(o.Lanes) == 0 {
		for _, n := range o.Cfg.Active {
			if pl.selected[n] || contains(pl.passed, n) {
				continue
			}
			why := "no Round: line"
			switch info := pl.scripts[n]; {
			case !info.Exists:
				why = "no lane script"
			case info.Stub:
				why = "stub"
			}
			doc.Idle = append(doc.Idle, PlanIdle{Lane: n, Why: why})
		}
	}
	sort.Slice(doc.Idle, func(a, b int) bool { return doc.Idle[a].Lane < doc.Idle[b].Lane })
	return doc, nil
}

// PlanCheck is a lane's last read-only check as the plan shows it.
type PlanCheck struct {
	Result    string   `yaml:"result"` // checked | stopped | failed | interrupted | refused
	StoppedAt string   `yaml:"stopped_at,omitempty"`
	Guards    []string `yaml:"guards,flow,omitempty"` // guards it reached (never approved)
	At        string   `yaml:"at,omitempty"`
	Stale     bool     `yaml:"stale,omitempty"`           // the script changed since
	DependsOn []int    `yaml:"depends_on,flow,omitempty"` // checked against today's state; these lanes' changes haven't run
}

// checkOf is lane n's last check (nil if it has none).
func checkOf(o Options, n int, deps []int) *PlanCheck {
	st, _ := status.Load(o.Root)
	if st == nil {
		return nil
	}
	l := st.Get(n)
	if l == nil || l.CheckResult == "" {
		return nil
	}
	c := &PlanCheck{Result: l.CheckResult, StoppedAt: l.CheckStop, Guards: l.CheckGuards, At: l.CheckAt, DependsOn: deps}
	if l.CheckSHA != "" {
		if data, err := lane.ScriptBytes(o.Root, n); err != nil || fmt.Sprintf("%x", sha256.Sum256(data)) != l.CheckSHA {
			c.Stale = true
		}
	}
	return c
}

func (c *PlanCheck) text(p ui.Painter) string {
	var s string
	switch c.Result {
	case "checked":
		s = p.Paint(ui.Green, "check PASS")
		if len(c.DependsOn) > 0 {
			s = p.Paint(ui.Yellow, "check ~ (depends on unrun changes in "+joinInts(c.DependsOn, ", ")+")")
		}
	case "stopped":
		s = p.Paint(ui.Red, fmt.Sprintf("stops at gate %q", c.StoppedAt))
	case "refused":
		s = p.Paint(ui.Yellow, c.StoppedAt)
	default:
		s = p.Paint(ui.Red, "check "+strings.ToUpper(c.Result))
	}
	if c.Stale {
		s += p.Paint(ui.Dim, " (stale: the script changed since)")
	}
	return s
}
