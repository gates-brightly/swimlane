package cli

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/envfile"
	"github.com/gates-brightly/swimlane/internal/finding"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/launcher"
	"github.com/gates-brightly/swimlane/internal/lint"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/manifest"
	"github.com/gates-brightly/swimlane/internal/queue"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// cmdQueue manages queued batches: swim queue [list|add|show|rm|requeue].
// swim queue 1 2 3 is shorthand for swim queue add 1 2 3.
func cmdQueue(args []string) error {
	sub := "list"
	if len(args) > 0 {
		switch {
		case looksLikeLaneRef(args[0]):
			sub = "add"
		case !strings.HasPrefix(args[0], "-"):
			sub, args = args[0], args[1:]
		}
	}
	switch sub {
	case "list", "ls":
		return queueList(args)
	case "add":
		return queueAdd(args)
	case "show":
		return queueShow(args)
	case "rm", "remove":
		return queueRemove(args)
	case "requeue":
		return queueRequeue(args)
	}
	return usagef("usage: swim queue [list|add|show|rm|requeue] (swim queue --help)")
}

// queueAdd queues a batch: the named lanes (or every pending round that
// hasn't passed), pinned to the jobs they hold now, with approvals.
func queueAdd(args []string) error {
	var approve []string
	var note, parallel string
	var yes bool
	// --approve repeats; -y is the short --yes.
	var rest0 []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--approve" && i+1 < len(args):
			approve = append(approve, strings.Split(args[i+1], ",")...)
			i++
		case strings.HasPrefix(a, "--approve="):
			approve = append(approve, strings.Split(strings.TrimPrefix(a, "--approve="), ",")...)
		case a == "-y":
			yes = true
		default:
			rest0 = append(rest0, a)
		}
	}
	rest, err := flags{bools: map[string]*bool{"yes": &yes}, strs: map[string]*string{"note": &note, "parallel": &parallel}}.parse(rest0)
	if err != nil {
		return err
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	if err := refreshStatus(root, cfg); err != nil {
		return err
	}
	var lanes []int
	for _, a := range laneArgs(rest) {
		n, err := laneRef(root, cfg, a, false)
		if err != nil {
			return err
		}
		lanes = append(lanes, n)
	}
	if len(lanes) == 0 {
		if lanes, _, err = launcher.Select(launcher.Options{Root: root, Cfg: cfg, DryRun: true}); err != nil {
			return err
		}
	}
	if len(lanes) == 0 {
		return fmt.Errorf("nothing to queue: no lane holds a pending round that hasn't passed (name lanes to queue them anyway)")
	}
	o := queueOpts{approve: approve, note: note, yes: yes}
	if parallel != "" {
		n, err := strconv.Atoi(parallel)
		if err != nil || n < 0 {
			return usagef("--parallel needs 0 (unlimited) or a positive number, got %q", parallel)
		}
		o.parallel = &n
	}
	b, err := addBatch(root, cfg, lanes, o)
	var refused lintRefused
	if errors.As(err, &refused) {
		finding.WriteText(os.Stderr, refused.fs)
		fmt.Fprintln(os.Stderr, "swim queue: not queued: swim run would refuse it; fix the errors above (swim lint shows every check), then queue again")
		return exitError{2}
	}
	if err != nil {
		return err
	}
	if b.warn != "" {
		fmt.Fprintln(os.Stderr, "swim queue: "+b.warn)
	}
	refreshStatus(root, cfg)
	e, guards := b.entry, b.guards
	seen := map[string]bool{}
	for _, f := range e.Approve {
		seen[f] = true
	}
	noun := "lanes"
	if len(e.Lanes) == 1 {
		noun = "lane"
	}
	fmt.Printf("queued #%d (%s): %d %s\n", e.Seq, e.ID, len(e.Lanes), noun)
	for _, l := range e.Lanes {
		fmt.Printf("  swim %-2d %s  %s\n", l.Lane, lane.ShortJob(l.Job), l.Round)
	}
	var unapproved []string
	for g := range guards {
		if !e.ApproveAll && !seen[g] {
			unapproved = append(unapproved, g)
		}
	}
	sort.Strings(unapproved)
	switch {
	case e.ApproveAll:
		fmt.Println("  approvals: every guard (-y)")
	case len(e.Approve) > 0:
		fmt.Println("  approvals: " + strings.Join(e.Approve, ", "))
	}
	asker := b.asker
	if len(unapproved) > 0 {
		if asker != nil {
			fmt.Println("  not approved yet: " + strings.Join(unapproved, ", ") + " (the operator is asked on the screen of " + asker.Name() + ")")
		} else {
			fmt.Println("  not approved (dry runs unless approved; no swimmer up asks for them): " + strings.Join(unapproved, ", "))
		}
	}
	if !e.ApproveAll && len(e.Approve) == 0 && asker == nil {
		warnUnapproved(root, e)
	}
	if why := launcher.Busy(root, cfg); why != "" {
		fmt.Printf("  runs when swim is idle (now: %s) and a swimmer is up: swim watch\n", why)
	} else {
		fmt.Println("  runs when swim watch takes it (swim watch --once runs what's queued, then exits)")
	}
	fmt.Printf("  wait for it: swim wait %s\n", e.ID)
	return nil
}

// waitEnd is how something swim wait waits on ended: its result (a lane
// state, or a batch's) and the line that says so.
type waitEnd struct{ result, line string }

// waitTarget is one lane, batch or run swim wait waits on: polled, it says
// how it ended, or what it is still waiting for.
type waitTarget func() (end *waitEnd, waiting string, err error)

// waitFor waits until every target has ended (any: until the first does),
// says how each ended, and exits as the ended ones went: 0 when they
// passed, ExitDryRun when one dry-ran a guard and none failed, 1
// otherwise (with any, the first to end decides). A limit > 0 gives up
// after that long. It checks quickly at first, backing off to every.
func waitFor(targets []waitTarget, any bool, every, limit time.Duration) error {
	s := sayer{p: ui.Painter{On: ui.ColorEnabled(os.Stdout)}, prefix: "swim wait: "}
	start, nap := time.Now(), min(every, 100*time.Millisecond)
	ended := make([]bool, len(targets))
	var order []waitEnd // how they ended, in the order they did
	for {
		var waiting []string
		for i, t := range targets {
			if ended[i] {
				continue
			}
			end, why, err := t()
			if err != nil {
				return err
			}
			if end != nil {
				ended[i] = true
				order = append(order, *end)
				continue
			}
			waiting = append(waiting, why)
		}
		if len(waiting) == 0 || any && len(order) > 0 {
			return waitEnded(order, any)
		}
		if limit > 0 && time.Since(start) >= limit {
			return fmt.Errorf("still waiting after %s: %s", limit, strings.Join(waiting, ", "))
		}
		s.say("waiting: " + strings.Join(waiting, ", "))
		time.Sleep(nap)
		nap = min(2*nap, every)
	}
}

// waitTargetFor resolves what swim wait was given: a lane (its number,
// script path or task), a batch (#41, a queue id or its first characters,
// or a job id: the newest batch that queued it), a swim run id, or a job
// id no batch queued (the lane that holds or last ran it).
func waitTargetFor(root string, cfg *config.Config, ref string) (waitTarget, error) {
	ref, err := taskRef(root, cfg, ref)
	if err != nil {
		return nil, err
	}
	if _, err := strconv.Atoi(ref); err == nil {
		n, err := laneArg(cfg, ref)
		if err != nil {
			return nil, err
		}
		return laneTarget(root, n), nil
	}
	if strings.HasPrefix(ref, "#") {
		return batchTarget(root, ref), nil
	}
	if _, err := findBatch(root, ref); err == nil {
		return batchTarget(root, ref), nil
	}
	if run := runOf(root, cfg, ref); run != "" {
		return runTarget(root, cfg, run), nil
	}
	if n, err := laneRef(root, cfg, ref, true); err == nil {
		return laneTarget(root, n), nil
	}
	return nil, fmt.Errorf("%q is no lane, queued batch, job or swim run (swim queue --all lists batches; swim status, lanes)", ref)
}

// batchTarget waits on the batch ref names. A batch whose swimmer died
// (orphaned) has ended too: it never runs on its own.
func batchTarget(root, ref string) waitTarget {
	return func() (*waitEnd, string, error) {
		e, err := findBatch(root, ref)
		if err != nil {
			return nil, "", err
		}
		switch {
		case e.State == queue.Done && e.Result != nil, e.State == queue.Orphaned && !e.BeingClaimed():
			result := e.State
			if e.Result != nil {
				result = e.Result.Result
			}
			line := fmt.Sprintf("%s (#%d): %s · swim %s", e.ID, e.Seq, result, joinLanes(e))
			if result == queue.Orphaned {
				line += " (its swimmer is gone; swim queue requeue " + e.ID + " runs it again)"
			}
			return &waitEnd{result, line}, "", nil
		case e.State == queue.Queued:
			return nil, fmt.Sprintf("#%d queued (not yet taken by a swimmer: swim watch)", e.Seq), nil
		}
		return nil, fmt.Sprintf("#%d %s", e.Seq, e.State), nil
	}
}

func joinLanes(e *queue.Entry) string {
	lanes := make([]string, len(e.Lanes))
	for k, l := range e.Lanes {
		lanes[k] = strconv.Itoa(l.Lane)
	}
	return strings.Join(lanes, ",")
}

// laneTarget waits on lane n's round: the one running, or the one a swim
// run holds it waiting to start; on an idle lane, its last round (or the
// first, when it has none yet).
func laneTarget(root string, n int) waitTarget {
	return func() (*waitEnd, string, error) {
		if _, running := lane.Running(root, n); running {
			return nil, fmt.Sprintf("swim %d running", n), nil
		}
		if laneWaiting(root, n) {
			return nil, fmt.Sprintf("swim %d waiting to start", n), nil
		}
		rounds, err := splitRounds(lane.Log(root, n))
		if err != nil {
			return nil, "", err
		}
		if len(rounds) == 0 {
			return nil, fmt.Sprintf("swim %d has no round yet", n), nil
		}
		result := "stopped without finishing its round (no END in its log)"
		if st, ok := roundEnd(rounds[len(rounds)-1]); ok {
			result = st
		}
		return &waitEnd{result, fmt.Sprintf("swim %d: %s", n, result)}, "", nil
	}
}

// roundEnd is how a round ended, as a lane state (passed, failed,
// skipped, interrupted), from its END line.
func roundEnd(round []string) (string, bool) {
	for _, l := range round {
		if _, ok := logparse.EndExit(l); ok {
			word := strings.Fields(strings.TrimPrefix(l, logparse.EndMark))[0]
			if st, ok := map[string]string{"PASS": status.Passed, "FAIL": status.Failed, "SKIP": status.Skipped}[word]; ok {
				return st, true
			}
			return strings.ToLower(word), true
		}
	}
	return "", false
}

// runOf is the full id of the swim run ref names (all of it, or 8+ of
// its first characters): a lane's run in status.yml, or a round's in its
// log. "" for none.
func runOf(root string, cfg *config.Config, ref string) string {
	if st, _ := status.Load(root); st != nil {
		for _, l := range st.Lanes {
			if lane.MatchJob(l.Run, ref) {
				return l.Run
			}
		}
	}
	for n := 1; n <= cfg.Lanes; n++ {
		rounds, _ := splitRounds(lane.Log(root, n))
		for _, r := range rounds {
			if len(r) > 1 {
				if run := logparse.ParseContext(r[1])["run"]; lane.MatchJob(run, ref) {
					return run
				}
			}
		}
	}
	return ""
}

// runTarget waits on every lane of swim run id: done when none of them is
// running or waiting to start; then each lane's round of that run says
// how the run went: passed when every one passed, skipped (a dry run) when
// the rest passed, failed otherwise.
func runTarget(root string, cfg *config.Config, id string) waitTarget {
	return func() (*waitEnd, string, error) {
		var active, lanes []string
		result := status.Passed
		st, _ := status.Load(root)
		if st == nil {
			st = &status.File{}
		}
		for n := 1; n <= cfg.Lanes; n++ {
			if l := st.Get(n); l != nil && l.Run == id {
				if _, running := lane.Running(root, n); running || laneWaiting(root, n) {
					active = append(active, strconv.Itoa(n))
					continue
				}
			}
			rounds, err := roundsOf(lane.Log(root, n), id)
			if err != nil {
				return nil, "", err
			}
			if len(rounds) == 0 {
				continue
			}
			lanes = append(lanes, strconv.Itoa(n))
			r, ok := roundEnd(rounds[len(rounds)-1])
			switch {
			case !ok || r != status.Passed && r != status.Skipped: // no END: its lane died
				result = status.Failed
			case r == status.Skipped && result == status.Passed:
				result = status.Skipped
			}
		}
		if len(active) > 0 {
			return nil, fmt.Sprintf("run %s: swim %s", id, strings.Join(active, ",")), nil
		}
		if len(lanes) == 0 {
			return nil, "", fmt.Errorf("run %s left no round in any lane's current log", id)
		}
		return &waitEnd{result, fmt.Sprintf("%s: %s · swim %s", id, result, strings.Join(lanes, ","))}, "", nil
	}
}

// findBatch finds a queue entry by what swim queue show takes, or by a job
// id it queued (the newest such batch).
func findBatch(root, ref string) (*queue.Entry, error) {
	e, err := queue.Find(root, ref)
	if err == nil || len(ref) < 8 {
		return e, err
	}
	all, lerr := queue.List(root, true)
	if lerr != nil {
		return nil, lerr
	}
	var newest *queue.Entry
	for _, c := range all {
		for _, l := range c.Lanes {
			if l.Job != "" && strings.HasPrefix(l.Job, ref) && (newest == nil || c.Seq > newest.Seq) {
				newest = c
			}
		}
	}
	if newest == nil {
		return nil, fmt.Errorf("no queue entry or queued job %q (swim queue --all lists them)", ref)
	}
	return newest, nil
}

// waitEnded says how each target ended and returns the exit they make.
func waitEnded(order []waitEnd, any bool) error {
	code := 0
	for i, e := range order {
		fmt.Println(e.line)
		if any && i > 0 {
			continue
		}
		switch e.result {
		case status.Passed:
		case status.Skipped:
			if code == 0 {
				code = launcher.ExitDryRun
			}
		default:
			code = 1
		}
	}
	if code != 0 {
		return exitError{code}
	}
	return nil
}

// queueOpts are what a batch holds besides its lanes (swim queue add's
// flags).
type queueOpts struct {
	approve  []string
	note     string
	yes      bool
	parallel *int
}

// askingSwimmer is the first of the live swimmers whose screen asks the
// operator for the batches' guards (nil for none): with one,
// a batch's unapproved guards are asked, not dry runs.
func askingSwimmer(live []*queue.Presence) *queue.Presence {
	for _, p := range live {
		if p.Asks {
			return p
		}
	}
	return nil
}

// warnUnapproved says, on stderr, which lanes of a batch that approves
// nothing will dry-run their guards when no swimmer up asks for them: a
// round that "passes" while changing nothing is easy to miss in the
// listing above. It warns rather than refuses: queuing a guarded round
// unapproved, for the operator to decide, is how a planner hands one over.
func warnUnapproved(root string, e *queue.Entry) {
	var lines []string
	var first, firstFlag string
	for _, l := range e.Lanes {
		info, err := lane.ReadScript(root, l.Lane)
		if err != nil {
			continue
		}
		flags := info.GuardFlags()
		if len(flags) == 0 {
			continue
		}
		if first == "" {
			first, firstFlag = strconv.Itoa(l.Lane), flags[0]
		}
		lines = append(lines, fmt.Sprintf("  swim %d  %s", l.Lane, strings.Join(flags, ", ")))
	}
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "swim queue: warning: #%d approves no guards and no swimmer up asks for them, so these dry-run if it runs before one does:\n%s\n", e.Seq, strings.Join(lines, "\n"))
	fmt.Fprintf(os.Stderr, "  the operator decides: swim watch on a terminal asks on its screen; or swim queue %s --approve %s (each flag), or -y (every guard)\n", first, firstFlag)
}

// batch is a batch just queued: its entry, the guards its lanes hold, and
// what to warn about.
type batch struct {
	entry  *queue.Entry
	guards map[string]bool
	warn   string
	// asker is a swimmer up as it was queued whose screen asks the
	// operator for its guards (nil for none).
	asker *queue.Presence
}

// addBatch queues lanes as one batch, pinned to the scripts and env they
// hold now, and logs it.
func addBatch(root string, cfg *config.Config, lanes []int, o queueOpts) (batch, error) {
	e := &queue.Entry{By: operator(), Note: o.note, ApproveAll: o.yes, Parallel: o.parallel}
	b := batch{entry: e}
	// What swim run would refuse is refused now, while whoever queues it
	// can fix it, not later, when the operator's swimmer runs it.
	if err := queueLint(root, cfg, lanes); err != nil {
		return b, err
	}
	var err error
	if e.Lanes, e.Approve, b.guards, err = pinBatch(root, cfg, lanes, o.approve); err != nil {
		return b, err
	}
	e.ID = queue.NewID()
	// A worker up now that can't run it: refuse, rather than queue a batch
	// that waits on (or used to fail in) the wrong swim.
	live := queue.LiveWorkers(root)
	b.asker = askingSwimmer(live)
	refuse, warnWorkers := checkWorkers(root, e, live)
	if refuse != nil {
		return b, refuse
	}
	if err := queue.Add(root, e); err != nil {
		return b, err
	}
	if w := refusedDeps(root, cfg, lanes); w != "" {
		if b.warn != "" {
			b.warn += "; "
		}
		b.warn += w
	}
	if warnWorkers != "" {
		if b.warn != "" {
			b.warn += "; "
		}
		b.warn += warnWorkers
	}
	var ns []string
	for _, l := range e.Lanes {
		ns = append(ns, strconv.Itoa(l.Lane))
	}
	detail := fmt.Sprintf("%s: swim %s", e.ID, strings.Join(ns, ","))
	if len(e.Approve) > 0 {
		detail += "  approve=" + strings.Join(e.Approve, ",")
	}
	if e.ApproveAll {
		detail += "  approve=all (-y)"
	}
	if e.Note != "" {
		detail += "  " + e.Note
	}
	history.Log(root, history.Entry{Event: history.QueueAdd, Detail: detail})
	return b, nil
}

// pinBatch pins lanes as a batch holds them, with the guards they hold and
// approve (each must name one of those guards: a typo would otherwise
// approve nothing, silently).
func pinBatch(root string, cfg *config.Config, lanes []int, approve []string) ([]queue.Lane, []string, map[string]bool, error) {
	var pinned []queue.Lane
	guards := map[string]bool{}
	for _, n := range lanes {
		l, info, err := pinLane(root, cfg, n)
		if err != nil {
			return nil, nil, guards, err
		}
		pinned = append(pinned, l)
		for _, g := range info.Guards {
			guards[g.Flag] = true
		}
	}
	var approved []string
	seen := map[string]bool{}
	for _, f := range approve {
		f = strings.TrimSpace(f)
		if f == "" || seen[f] {
			continue
		}
		if !guards[f] {
			return nil, nil, guards, fmt.Errorf("--approve %s: no lane in this batch has a guard %s (its guards: %s)", f, f, or(strings.Join(sortedKeys(guards), ", "), "none"))
		}
		seen[f] = true
		approved = append(approved, f)
	}
	sort.Strings(approved)
	return pinned, approved, guards, nil
}

// pinLane is lane n as a queue entry holds it: its job and round, pinned
// to the script, env and manifest it has now.
func pinLane(root string, cfg *config.Config, n int) (queue.Lane, lane.Info, error) {
	info, err := lane.ReadScript(root, n)
	if err != nil {
		return queue.Lane{}, info, err
	}
	if !info.Pending() {
		return queue.Lane{}, info, fmt.Errorf("swim %d holds no pending round (a stub, or no Round: line); write one before queueing it", n)
	}
	data, err := lane.ScriptBytes(root, n)
	if err != nil {
		return queue.Lane{}, info, err
	}
	r, err := envfile.Check(root, cfg, n, info)
	if err != nil {
		return queue.Lane{}, info, err
	}
	envSum := r.SHA256()
	m, err := manifest.Build(root, cfg, n, envSum)
	if err != nil {
		return queue.Lane{}, info, err
	}
	return queue.Lane{Lane: n, Job: info.Job, Round: info.Round, SHA256: chain.Hash(data), EnvSHA256: &envSum, Manifest: m.SHA256}, info, nil
}

// cmdWait blocks until work is done: the lanes, batches or runs named
// (exiting with how they went), or with none named, until swim is idle and
// nothing is about to start: no lane running, no swim run waiting on
// lanes, and no queued or running batch.
func cmdWait(args []string) error {
	var interval, timeout string
	var any bool
	rest, err := flags{bools: map[string]*bool{"any": &any}, strs: map[string]*string{"interval": &interval, "timeout": &timeout}}.parse(args)
	if err != nil {
		return err
	}
	if any && len(rest) == 0 {
		return usagef("usage: swim wait --any N|BATCH|RUN ... (what to wait on)")
	}
	every, err := durationFlag("interval", interval, pollEvery, "2s")
	if err != nil {
		return err
	}
	limit, err := durationFlag("timeout", timeout, 0, "30m")
	if err != nil {
		return err
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return waitIdle(root, every, limit)
	}
	var targets []waitTarget
	for _, ref := range laneArgs(rest) {
		t, err := waitTargetFor(root, cfg, ref)
		if err != nil {
			return err
		}
		targets = append(targets, t)
	}
	return waitFor(targets, any, every, limit)
}

// pollEvery is how often swim wait and swim watch check, by default.
const pollEvery = 2 * time.Second

// durationFlag parses a --name duration flag's value v, or def when unset.
func durationFlag(name, v string, def time.Duration, example string) (time.Duration, error) {
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, usagef("--%s takes a duration like %s, got %q", name, example, v)
	}
	return d, nil
}

// sayer prints a dim status line only when it differs from the last one.
type sayer struct {
	p            ui.Painter
	prefix, said string
}

func (s *sayer) say(msg string) {
	if msg != s.said {
		fmt.Println(s.p.Paint(ui.Dim, s.prefix+msg))
		s.said = msg
	}
}

// waitIdle polls until swim is idle and the queue has nothing left to run,
// saying what it waits on whenever that changes. A limit > 0 gives up after
// that long. It checks quickly at first, backing off to every.
func waitIdle(root string, every, limit time.Duration) error {
	s := sayer{p: ui.Painter{On: ui.ColorEnabled(os.Stdout)}, prefix: "swim wait: "}
	start, nap := time.Now(), min(every, 100*time.Millisecond)
	for {
		cfg, err := loadCfg(root)
		if err != nil {
			return err
		}
		why := launcher.Busy(root, cfg)
		if why == "" {
			if why, err = queue.Busy(root); err != nil {
				return err
			}
		}
		if why == "" {
			s.say("idle")
			return nil
		}
		if limit > 0 && time.Since(start) >= limit {
			return fmt.Errorf("still busy after %s: %s", limit, why)
		}
		s.say("waiting: " + why)
		time.Sleep(nap)
		nap = min(2*nap, every)
	}
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func queueList(args []string) error {
	var all, asYAML bool
	rest, err := flags{bools: map[string]*bool{"all": &all, "yaml": &asYAML}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return usagef("usage: swim queue [list] [--all] [--yaml]")
	}
	root, _, err := repo()
	if err != nil {
		return err
	}
	entries, err := queue.List(root, true)
	if err != nil {
		return err
	}
	if !all { // the last 5 consumed entries, every other
		var done []*queue.Entry
		var rest []*queue.Entry
		for _, e := range entries {
			if e.State == queue.Done {
				done = append(done, e)
			} else {
				rest = append(rest, e)
			}
		}
		if len(done) > 5 {
			done = done[len(done)-5:]
		}
		entries = append(done, rest...)
	}
	if asYAML {
		type row struct {
			*queue.Entry `yaml:",inline"`
			State        string `yaml:"state"`
		}
		var rows []row
		for _, e := range entries {
			rows = append(rows, row{e, e.State})
		}
		return yaml.NewEncoder(os.Stdout).Encode(map[string]any{"schema": "swim.queue-list/v1", "entries": rows})
	}
	p := ui.Painter{On: ui.ColorEnabled(os.Stdout)}
	if len(entries) == 0 {
		fmt.Println("queue is empty. Queue a batch: swim queue N|JOB ... [--approve FLAG] [-y]")
		return nil
	}
	held := heldReasons(root)
	for _, e := range entries {
		var ns []string
		for _, l := range e.Lanes {
			ns = append(ns, strconv.Itoa(l.Lane))
		}
		state, color := e.State, ""
		switch e.State {
		case queue.Running:
			state, color = fmt.Sprintf("running (pid %d)", e.Worker.PID), ui.Cyan
			if e.Worker.Remote() {
				state = fmt.Sprintf("running (pid %d on %s)", e.Worker.PID, e.Worker.Host)
				if e.Worker.Heartbeat == "" {
					state = fmt.Sprintf("running? (pid %d on %s; an older swimmer with no heartbeat: can't tell)", e.Worker.PID, e.Worker.Host)
				}
			}
		case queue.Orphaned:
			state, color = "orphaned: its swimmer is gone (swim queue requeue "+strconv.Itoa(e.Seq)+")", ui.Red
		case queue.Done:
			state, color = "done: "+e.Result.Result, ui.StateColor(e.Result.Result)
		case queue.Queued:
			color = ui.Yellow
		}
		appr := ""
		switch {
		case e.ApproveAll:
			appr = "  approve all"
		case len(e.Approve) > 0:
			appr = "  approve " + strings.Join(e.Approve, ",")
		}
		fmt.Printf("#%-3d %s  %s  swim %s%s  %s\n", e.Seq, e.ID, p.Paint(color, state), strings.Join(ns, ","), appr, p.Paint(ui.Dim, e.Note))
		if why := held[e.ID]; why != "" && e.State == queue.Queued {
			fmt.Println("     " + p.Paint(ui.Red, "left queued: "+why))
		}
	}
	return nil
}

func queueShow(args []string) error {
	if len(args) != 1 {
		return usagef("usage: swim queue show ID")
	}
	root, _, err := repo()
	if err != nil {
		return err
	}
	e, err := queue.Find(root, args[0])
	if err != nil {
		return err
	}
	data, err := os.ReadFile(e.Path)
	if err != nil {
		return err
	}
	fmt.Printf("# %s (%s)\n", rel(root, e.Path), e.State)
	if why := heldReasons(root)[e.ID]; why != "" && e.State == queue.Queued {
		fmt.Printf("# left queued: %s\n", why)
	}
	fmt.Print(string(data))
	return nil
}

func queueRemove(args []string) error {
	if len(args) != 1 {
		return usagef("usage: swim queue rm ID")
	}
	root, _, err := repo()
	if err != nil {
		return err
	}
	e, err := queue.Find(root, args[0])
	if err != nil {
		return err
	}
	if err := queue.Remove(e); err != nil {
		return err
	}
	fmt.Printf("removed %s (#%d, %s)\n", e.ID, e.Seq, e.State)
	return nil
}

func queueRequeue(args []string) error {
	if len(args) != 1 {
		return usagef("usage: swim queue requeue ID")
	}
	root, _, err := repo()
	if err != nil {
		return err
	}
	e, err := queue.Find(root, args[0])
	if err != nil {
		return err
	}
	if e.State == queue.Queued {
		return fmt.Errorf("%s is already waiting in the queue", e.ID)
	}
	from := e.State
	if err := queue.Requeue(root, e); err != nil {
		return err
	}
	fmt.Printf("requeued %s (was %s) as #%d\n", e.ID, from, e.Seq)
	return nil
}

// cmdWatch is the queue's worker: it runs each queued batch, oldest first,
// as soon as swim is idle, and consumes it when it ends.
func cmdWatch(args []string) error {
	var once, yes, noTUI bool
	var interval, batchesFlag string
	args = append([]string(nil), args...)
	for i, a := range args {
		if a == "-y" {
			args[i] = "--yes"
		}
	}
	var plain bool // as swim run says it: --plain is --no-tui here
	rest, err := flags{bools: map[string]*bool{"once": &once, "yes": &yes, "no-tui": &noTUI, "plain": &plain},
		strs: map[string]*string{"interval": &interval, "batches": &batchesFlag}}.parse(args)
	if err != nil {
		return err
	}
	noTUI = noTUI || plain
	if len(rest) > 0 {
		return usagef("usage: swim watch [--once] [-y] [--interval D] [--batches N] [--no-tui|--plain]")
	}
	every, err := durationFlag("interval", interval, pollEvery, "2s")
	if err != nil {
		return err
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	if err := requireVersion(root, true); err != nil {
		return err
	}
	batches := cfg.WatchBatchesFor()
	if batchesFlag != "" {
		if batches, err = strconv.Atoi(batchesFlag); err != nil || batches < 1 {
			return usagef("--batches takes a number, 1 or more (how many batches run at once), got %q", batchesFlag)
		}
	}
	defer holdWatchLock(root)() // others can tell a worker is up
	// ... and swim queue add which swim it is (.swim/workers/).
	watchPresence = queue.Announce(root, operator(), cfg.QueueLeaseFor())
	defer func() { watchPresence.Stop(); watchPresence = nil }()
	p := ui.Painter{On: ui.ColorEnabled(os.Stdout)}
	for _, r := range launcher.ReapDead(root, cfg, self()) {
		fmt.Println(p.Paint(ui.Yellow, "swim watch: "+r.String()))
	}
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	stop := false
	stopped := func() bool {
		for {
			select {
			case <-sigs:
				stop = true
			default:
				return stop
			}
		}
	}
	sleep := func() {
		select {
		case <-sigs:
			stop = true
		case <-time.After(every):
		}
	}
	mode := "watching .swim/queue/ (Ctrl-C stops after the batch in hand)"
	switch {
	case once:
		mode = "running every queued batch, then exiting"
	case batches > 1:
		mode = fmt.Sprintf("watching .swim/queue/, up to %d batches at once (Ctrl-C interrupts the batches in hand and stops)", batches)
	}
	if once && batches > 1 {
		mode = fmt.Sprintf("running every queued batch, up to %d at once, then exiting", batches)
	}
	fmt.Println(p.Paint(ui.Bold, "swim watch · "+mode))
	if yes {
		fmt.Println(p.Paint(ui.Yellow+ui.Bold, "swim watch: -y: every guard in every batch is approved, beyond what each batch approves (logged as approved by swim watch -y ("+operator()+"))"))
	}
	code, s := 0, sayer{p: p, prefix: "swim watch: "}
	// On a terminal, waiting shows the jobs and the queue: r runs one now,
	// q adds one to the next batch and s submits it (watchview.go).
	var view *watchView
	// The terminal's notifications and progress bar (notify setting), and
	// notify_command.
	notes := newWatchNotes(terminalNotifier(cfg), commandHook(cfg, root), root)
	defer notes.stop()
	if !once && watchViewAllowed(noTUI) {
		view = newWatchView(root, cfg, yes)
		if notes != nil {
			notes.asks = view.asks
			view.asks.onAsk = notes.asked
		}
		sleep = func() {
			ok := view.wait(func() bool {
				if stopped() {
					return true
				}
				// Back to the loop when there's a batch to take.
				c, err := loadCfg(root)
				if err != nil {
					return true
				}
				waiting, _ := queue.List(root, false)
				for _, e := range waiting {
					if e.State == queue.Queued {
						return launcher.Busy(root, c) == ""
					}
				}
				return false
			})
			if !ok {
				stop = true
			}
		}
	}
	say := func(msg string) {
		if view != nil {
			view.activity = "swimmer: " + msg
			return
		}
		s.say(msg)
	}
	if batches > 1 {
		var c int
		b := newBatchRunner(root, yes, view != nil, batches)
		b.notes = notes
		c, err = watchBatches(root, cfg, b, view, once, every, sigs, say)
		if err != nil {
			return err
		}
		if c != 0 {
			return exitError{c}
		}
		return nil
	}
	for !stopped() {
		waiting, err := queue.List(root, false)
		if err != nil {
			return err
		}
		n := 0
		for _, e := range waiting {
			if e.State == queue.Queued {
				n++
			}
		}
		if n == 0 {
			if once {
				break
			}
			say("queue empty; waiting for swim queue N")
			sleep()
			continue
		}
		if cfg, err = loadCfg(root); err != nil {
			return err
		}
		if why := launcher.Busy(root, cfg); why != "" {
			say(fmt.Sprintf("%d queued; waiting for swim to be idle: %s", n, why))
			sleep()
			continue
		}
		unlock, ok, err := queue.LockWorker(root)
		if err != nil {
			return err
		}
		if !ok {
			say(fmt.Sprintf("%d queued; another swimmer is running a batch", n))
			sleep()
			continue
		}
		e, err := claimDue(root, cfg)
		if err != nil || e == nil {
			unlock()
			if err != nil {
				return err
			}
			// Waiting, but none this worker can run (held for another).
			say(fmt.Sprintf("%d queued; none can run here", n))
			sleep()
			continue
		}
		s.said = ""
		began := time.Now()
		res, interrupted := runClaimed(root, cfg, e, yes, view != nil, false, nil)
		unlock()
		notes.batchDone(e.Seq, res.Result, laneNums(e), time.Since(began))
		if view != nil {
			view.picker.Note = fmt.Sprintf("#%d %s", e.Seq, res.Result)
		}
		if res.Exit != 0 {
			code = 1
		}
		if interrupted {
			stop = true
		}
	}
	if code != 0 {
		return exitError{code}
	}
	return nil
}

// runClaimed runs a batch this worker claimed and consumes it with its
// result. view says the watch screen is up: the run's own view may then
// take the keyboard (to move between lanes; it never answers guards).
func runClaimed(root string, cfg *config.Config, e *queue.Entry, yes, view, plain bool, ask approval.Asker) (queue.Result, bool) {
	p := ui.Painter{On: ui.ColorEnabled(os.Stdout)}
	stopBeat := heartbeatClaim(e)
	res, interrupted := runEntry(root, cfg, e, yes, view, plain, ask)
	stopBeat()
	if err := queue.Consume(root, e, res); err != nil {
		fmt.Fprintln(os.Stderr, "swim watch: consume "+e.ID+": "+err.Error())
	}
	history.Log(root, history.Entry{Event: history.QueueDone, Run: res.Run, Detail: fmt.Sprintf("%s: %s  exit=%d", e.ID, res.Result, res.Exit)})
	fmt.Println(p.Paint(ui.StateColor(res.Result)+ui.Bold, fmt.Sprintf("swim watch: %s (#%d) %s; consumed to %s", e.ID, e.Seq, res.Result, rel(root, e.Path))))
	return res, interrupted
}

// heartbeatClaim renews a claimed entry's heartbeat every third of its
// lease while the batch runs, so a worker on another host can tell this
// one is alive (and that it's gone once the heartbeat stops). The returned
// func stops it, and returns once no renewal is in flight.
func heartbeatClaim(e *queue.Entry) (stop func()) {
	if e.Worker == nil {
		return func() {}
	}
	done, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		tick := time.NewTicker(e.Worker.LeaseFor() / 3)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				err := queue.Touch(e)
				switch {
				case errors.Is(err, queue.ErrClaimLost):
					// Requeued or removed from another host: another
					// swimmer may run it now. Stop this run, as Ctrl-C
					// would (lanes stop at their next step boundary).
					fmt.Fprintln(os.Stderr, "swim watch: "+err.Error()+": stopping this batch (another swimmer may run it)")
					syscall.Kill(os.Getpid(), syscall.SIGINT)
					return
				case err != nil:
					fmt.Fprintln(os.Stderr, "swim watch: heartbeat "+e.ID+": "+err.Error())
				}
			}
		}
	}()
	return func() { close(done); <-finished }
}

// loadCfg reloads the repo's config (a long-running watch picks up edits).
func loadCfg(root string) (*config.Config, error) {
	_, cfg, err := repoNoMigrate()
	return cfg, err
}

// runEntry runs one claimed batch through the launcher with the batch's
// approvals and nothing else: a guard flag the batch doesn't approve is
// cleared from the environment for the run, so the queue file alone says
// what may change. It never prompts at a terminal (nobody is watching a
// queue); ask, if set, is swim watch's screen, which may answer what
// nothing else does (watchask.go).
func runEntry(root string, cfg *config.Config, e *queue.Entry, workerYes, keys, plain bool, ask approval.Asker) (queue.Result, bool) {
	started := time.Now().UTC()
	res := queue.Result{Started: started.Format(time.RFC3339)}
	var lanes []int
	pins := map[int]string{}
	guards := map[string]bool{}
	for _, l := range e.Lanes {
		lanes = append(lanes, l.Lane)
		if l.Job != "" {
			pins[l.Lane] = l.Job
		}
		if info, err := lane.ReadScript(root, l.Lane); err == nil {
			for _, g := range info.Guards {
				guards[g.Flag] = true
			}
		}
	}
	approved := map[string]bool{}
	for _, f := range e.Approve {
		approved[f] = true
	}
	restore := map[string]*string{}
	setenv := func(k, v string, unset bool) {
		if _, ok := restore[k]; !ok {
			if old, had := os.LookupEnv(k); had {
				restore[k] = &old
			} else {
				restore[k] = nil
			}
		}
		if unset {
			os.Unsetenv(k)
		} else {
			os.Setenv(k, v)
		}
	}
	for g := range guards {
		setenv(g, "1", !approved[g])
	}
	for f := range approved {
		setenv(f, "1", false)
	}
	setenv("SWIM_QUEUE", fmt.Sprintf("queue %s by %s", e.ID, e.By), false)
	setenv("SWIM_QUEUE_APPROVED", strings.Join(e.Approve, " "), false)
	defer func() {
		for k, v := range restore {
			if v == nil {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, *v)
			}
		}
	}()

	pinSHA, pinEnv, pinMan := map[int]string{}, map[int]string{}, map[int]string{}
	for _, l := range e.Lanes {
		if l.SHA256 != "" {
			pinSHA[l.Lane] = l.SHA256
		}
		if l.Manifest != "" {
			pinMan[l.Lane] = l.Manifest
		}
		if l.EnvSHA256 != nil {
			pinEnv[l.Lane] = *l.EnvSHA256
		}
	}
	o := launcher.Options{Root: root, Cfg: cfg, Lanes: lanes, Pins: pins, PinSHA: pinSHA, PinEnv: pinEnv, PinManifest: pinMan, QueuedBy: e.Swim, Self: self(), Out: os.Stdout,
		RunID: lane.NewRunID(), Parallel: e.Parallel,
		AutoApprove: e.ApproveAll, ApprovedBy: e.By, ApprovedVia: "queue " + e.ID + " -y",
		Unattended: true, Ask: ask}
	o.Plain = plain // a batch swim watch runs alongside others (_batch)
	if keys && len(lanes) > 1 {
		// The live view's keys move between the batch's lanes. Parallel
		// lanes get no stdin, so nothing in the batch can prompt.
		o.Stdin, o.Interactive = os.Stdin, display.TUIAllowed(os.Getenv)
	}
	if !e.ApproveAll && workerYes {
		// The worker approves what the batch doesn't (the batch's own
		// approvals keep their credit): swim watch -y.
		o.AutoApprove, o.ApprovedBy, o.ApprovedVia = true, operator(), "swim watch -y"
		res.ApprovedBy = "swim watch -y (" + operator() + ")"
	}
	interrupted := false
	o.Finished = func(f launcher.Finish) { interrupted = f.Interrupted }
	res.Run = o.RunID
	var ns []string
	for _, n := range lanes {
		ns = append(ns, strconv.Itoa(n))
	}
	detail := fmt.Sprintf("%s: swim %s  queued by %s", e.ID, strings.Join(ns, ","), e.By)
	if res.ApprovedBy != "" {
		detail += "  every other guard approved by " + res.ApprovedBy
	}
	history.Log(root, history.Entry{Event: history.QueueRun, Run: o.RunID, Detail: detail})
	fmt.Printf("\nswim watch: running %s (#%d, queued by %s): swim %s\n", e.ID, e.Seq, e.By, strings.Join(ns, " "))

	head := signoffHead(root)
	code, err := launcher.Run(o)
	if err == nil {
		signoffsAfterRun(root, cfg, o.RunID, head)
	}
	res.Finished = time.Now().UTC().Format(time.RFC3339)
	res.Exit = code
	switch {
	case err != nil:
		res.Result, res.Error, res.Exit = "error", err.Error(), 2
	case interrupted:
		res.Result = status.Interrupted
	case code == 0:
		res.Result = status.Passed
	case code == launcher.ExitDryRun:
		res.Result = status.Skipped
	default:
		res.Result = status.Failed
	}
	if st, _ := status.Load(root); st != nil {
		for _, n := range lanes {
			if l := st.Get(n); l != nil {
				res.Lanes = append(res.Lanes, queue.LaneResult{Lane: n, Result: l.State, Exit: l.ExitCode, Reason: l.Reason})
			}
		}
	}
	return res, interrupted
}

// lintRefused is a batch refused at enqueue: lint errors swim run would
// refuse it for. Error is one line (the watch screen shows it); the
// findings are for swim queue to print in full.
type lintRefused struct{ fs []finding.Finding }

func (e lintRefused) Error() string {
	f := e.fs[0]
	more := ""
	if len(e.fs) > 1 {
		more = fmt.Sprintf(" (and %d more)", len(e.fs)-1)
	}
	return fmt.Sprintf("not queued: %s:%d %s [%s]%s; swim run would refuse it: fix it (swim lint), then queue again", f.File, f.Line, f.Message, f.Code, more)
}

func init() { launcher.LintRefusal = lintRefusal }

// lintRefusal is the first lint error swim run would refuse lane n for, as
// "lane.N.sh:L message [code]" ("" when there's none).
func lintRefusal(root string, n int) string {
	cfg, err := config.Load(root)
	if err != nil {
		return ""
	}
	fs, err := lint.Errors(root, cfg, []int{n})
	if err != nil || len(fs) == 0 {
		return ""
	}
	return fmt.Sprintf("%s:%d %s [%s]", fs[0].File, fs[0].Line, fs[0].Message, fs[0].Code)
}

// refusedDeps warns about the lanes a batch waits on, outside it, that
// swim lint refuses: they can't pass, so the lanes waiting on them would
// skip. It names each one's first lint error.
func refusedDeps(root string, cfg *config.Config, lanes []int) string {
	deps, err := launcher.ResolveDeps(root, cfg, lanes)
	if err != nil {
		return ""
	}
	var warns []string
	seen := map[int]bool{}
	for _, n := range lanes {
		for _, d := range deps[n] {
			if slices.Contains(lanes, d.Lane) || seen[d.Lane] {
				continue
			}
			seen[d.Lane] = true
			if why := lintRefusal(root, d.Lane); why != "" {
				warns = append(warns, fmt.Sprintf("swim %d waits on swim %d, which swim lint refuses (%s): it can't pass, so swim %d will skip; fix swim %d first", n, d.Lane, why, n, d.Lane))
			}
		}
	}
	return strings.Join(warns, "; ")
}

// queueLint runs swim run's lint pre-flight on lanes about to be queued.
func queueLint(root string, cfg *config.Config, lanes []int) error {
	fs, err := lint.Errors(root, cfg, lanes)
	if err != nil || len(fs) == 0 {
		return err
	}
	logLintErrors(root, fs, "queue")
	return lintRefused{fs}
}
