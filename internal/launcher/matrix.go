package launcher

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/runner"
	"github.com/gates-brightly/swimlane/internal/status"
)

// runMatrix runs a matrix round: one process per cell (SWIM_CELL set, the
// cell's variables exported), at most Matrix-Parallel at once and each in
// a slot of the run's --parallel, with the cell's own locks. The lane
// passes only if every cell does; with Matrix-Fail: stop, cells not started
// after a failure are skipped.
func runMatrix(o Options, n int, sel []int, info lane.Info, sl *slots, lt *lockTable, disp *display.Display, ir *interrupter, laneLocks lane.FileLocks) *Outcome {
	if info.Job == "" {
		// One job for the round, so its cells are <job>/<cell>. Rerunning
		// some cells of the same round (--failed, 3[cell]) keeps its job.
		info.Job = lane.NewJobID()
		if o.FailedCells || len(o.Cells[n]) > 0 {
			if st, _ := status.Load(o.Root); st != nil {
				if l := st.Get(n); l != nil && l.Job != "" && len(l.Cells) > 0 {
					info.Job = l.Job
				}
			}
		}
	}
	cells := selectCells(o, n, info)
	start := time.Now()
	startTS := start.UTC().Format(time.RFC3339)
	// A matrix's cells all run the lane's script, and so do the jobs of a
	// lane in another language; a bash lane's jobs each run their own
	// (runCell resolves it).
	var plan runner.Plan
	if !info.IsGroup() || info.NativeJobs() {
		var perr error
		if plan, perr = runner.Resolve(o.Cfg, o.manifests[n].Script()); perr != nil {
			return refuseStart(o, n, perr, disp)
		}
	}
	// The run's other lanes wait for a group lane's Always sub-jobs, not
	// the rest of it: open the gate once those have finished.
	setupLeft := map[string]bool{}
	for _, name := range info.AlwaysCells() {
		for _, c := range cells {
			if c.Name == name {
				setupLeft[name] = true
			}
		}
	}
	if len(setupLeft) == 0 {
		o.openSetup(n)
	}
	if len(cells) == 0 {
		why := "no cells to run"
		if o.FailedCells {
			why = "every cell already passed"
		}
		disp.Set(n, func(v *display.LaneView) { v.State, v.Reason = display.Skipped, why })
		return &Outcome{N: n, Job: info.Job, Round: info.Round, State: status.Skipped, Exit: -1, Reason: why, End: time.Now()}
	}
	status.Update(o.Root, n, o.Cfg.Lanes, func(l *status.Lane) {
		l.ResetRun()
		l.State, l.Round, l.Job, l.Run = status.Running, info.Round, info.Job, o.RunID
		l.PID, l.StartedAt = os.Getpid(), status.Str(startTS)
		for _, c := range cells {
			e := l.CellEntry(c.Name)
			e.ResetRun()
			e.State, e.Round, e.Job, e.Run = status.Waiting, info.ForCell(c).Round, info.ForCell(c).Job, o.RunID
		}
	})
	disp.Set(n, func(v *display.LaneView) {
		v.State, v.WaitingOn, v.Started, v.Round, v.Job = display.Running, nil, start, info.Round, info.Job
	})
	o.ev.emit(Event{Event: "start", Lane: n, Job: info.Job, Round: info.Round, At: startTS})
	kind := "matrix: %d cells"
	if info.IsGroup() {
		kind = "%d jobs"
	}
	history.Log(o.Root, history.Entry{Event: history.Start, Lane: n, Job: info.Job, Run: o.RunID,
		Detail: fmt.Sprintf("%s ("+kind+")", info.Round, len(cells))})

	var mu sync.Mutex
	results := map[string]string{}
	running := 0
	show := func() {
		mu.Lock()
		text := cellSummary(len(cells), results, running)
		mu.Unlock()
		disp.Set(n, func(v *display.LaneView) { v.Cells = text })
	}
	show()
	par := max(info.MatrixParallel, 1)
	sem := make(chan struct{}, par)
	var stopRest atomic.Bool
	var wg sync.WaitGroup
	perCell, _ := lane.CellLocks(info.Locks)
	// Finally jobs ("# swim job 1.9 finally"): they run after their
	// earlier siblings, however those ended, and the first stop doesn't
	// stop them (finally.go).
	finJob := map[string]bool{}
	if info.IsGroup() {
		for _, c := range cells {
			if info.ForCell(c).FinallyJob {
				finJob[c.Name] = true
				ir.markFinallyJob(n, c.Name)
			}
		}
	}
	for idx, c := range cells {
		// Cells start in order (so Matrix-Fail: stop skips the later ones).
		sem <- struct{}{}
		wg.Add(1)
		var finEnv []string
		if finJob[c.Name] {
			mu.Lock()
			finEnv = finallyJobEnv(n, c.Name, cells[:idx], results)
			mu.Unlock()
		}
		go func(idx int, c lane.Cell, finEnv []string) {
			defer wg.Done()
			defer func() { <-sem }()
			state := runCell(o, n, idx, c, sel, info, plan, perCell, sl, lt, disp, ir, laneLocks, &stopRest, finJob[c.Name], finEnv, func(delta int) {
				mu.Lock()
				running += delta
				mu.Unlock()
				show()
			})
			mu.Lock()
			results[c.Name] = state
			delete(setupLeft, c.Name)
			setupDone := len(setupLeft) == 0
			mu.Unlock()
			if state != status.Passed && state != status.Skipped && info.MatrixFail == "stop" {
				stopRest.Store(true)
			}
			if setupDone {
				o.openSetup(n)
			}
			show()
		}(idx, c, finEnv)
	}
	wg.Wait()

	// The lane's result: every cell passed, or not.
	state, exit := status.Passed, 0
	var failed []string
	pass, fail, skip := 0, 0, 0
	finFailed := false
	for _, c := range cells {
		if finJob[c.Name] {
			// A finally job doesn't decide the lane, but its failure shows.
			switch results[c.Name] {
			case status.Passed:
				pass++
			case status.Skipped:
				skip++
			default:
				fail++
				finFailed = true
				failed = append(failed, "finally: "+info.SubLabel(n, c.Name)+" "+strings.ToUpper(results[c.Name]))
			}
			continue
		}
		switch results[c.Name] {
		case status.Passed:
			pass++
		case status.Interrupted:
			if state != status.Failed {
				state, exit = status.Interrupted, 130
			}
			failed = append(failed, info.SubLabel(n, c.Name)+" interrupted")
		case status.Skipped:
			skip++
			if state == status.Passed {
				state, exit = status.Failed, 1
			}
			failed = append(failed, info.SubLabel(n, c.Name)+" skipped")
		default:
			fail++
			state, exit = status.Failed, 1
			failed = append(failed, info.SubLabel(n, c.Name)+" failed")
		}
	}
	switch {
	case finFailed && state == status.Passed:
		state, exit = status.Failed, 1
	case finFailed:
		exit = 89 // failed, and the cleanup failed too
	}
	end := time.Now()
	status.Update(o.Root, n, o.Cfg.Lanes, func(l *status.Lane) {
		l.State, l.PID, l.FinishedAt = state, 0, status.Str(end.UTC().Format(time.RFC3339))
		l.ExitCode = status.Int(exit)
		l.DurationS = status.Float(end.Sub(start).Round(100 * time.Millisecond).Seconds())
		l.Pass, l.Fail, l.Skip = pass, fail, skip
		l.FailedSteps = append([]string{}, failed...)
		l.CurrentStep, l.Stage = "", ""
	})
	disp.Set(n, func(v *display.LaneView) {
		v.Exit, v.Finished = exit, end
		switch state {
		case status.Passed:
			v.State = display.Passed
		case status.Interrupted:
			v.State = display.Interrupted
		default:
			v.State = display.Failed
		}
	})
	event := map[string]string{status.Passed: history.Pass, status.Failed: history.Fail, status.Interrupted: history.Interrupted}[state]
	history.Log(o.Root, history.Entry{Event: event, Lane: n, Job: info.Job, Run: o.RunID,
		Detail: fmt.Sprintf("cells: %d passed, %d failed, %d skipped  %s", pass, fail, skip, info.Round)})
	return &Outcome{N: n, Job: info.Job, Round: info.Round, State: state, Exit: exit, Cells: results,
		Start: start, End: end, Elapsed: end.Sub(start), Reason: strings.Join(failed, ", ")}
}

// selectCells is the cells of lane n this run runs: named ones, the ones
// that didn't pass (--failed), or all.
func selectCells(o Options, n int, info lane.Info) []lane.Cell {
	all := info.Cells()
	if want := o.Cells[n]; len(want) > 0 {
		var out []lane.Cell
		for _, name := range want {
			if c, ok := info.Cell(name); ok {
				out = append(out, c)
			}
		}
		return out
	}
	// swim all runs a group lane's Always sub-jobs, and the ones that
	// haven't passed; named (or --rerun), all of it.
	if info.IsGroup() && !o.FailedCells && !o.Rerun && !contains(o.Lanes, n) {
		always := map[string]bool{}
		for _, name := range info.AlwaysCells() {
			always[name] = true
		}
		st, _ := status.Load(o.Root)
		var out []lane.Cell
		for _, c := range all {
			if !always[c.Name] && st != nil {
				if l := st.Get(n); l != nil {
					if e := l.FindCell(c.Name); e != nil && e.State == status.Passed && e.Job != "" && e.Job == info.ForCell(c).Job {
						continue
					}
				}
			}
			out = append(out, c)
		}
		return out
	}
	if !o.FailedCells {
		return all
	}
	st, _ := status.Load(o.Root)
	var out []lane.Cell
	for _, c := range all {
		if st != nil {
			if l := st.Get(n); l != nil {
				if e := l.FindCell(c.Name); e != nil && e.State == status.Passed && e.Job == info.ForCell(c).Job {
					continue
				}
			}
		}
		out = append(out, c)
	}
	return out
}

// runCell runs one cell and returns how it ended.
func runCell(o Options, n, idx int, c lane.Cell, sel []int, info lane.Info, plan runner.Plan, perCell []string,
	sl *slots, lt *lockTable, disp *display.Display, ir *interrupter, laneLocks lane.FileLocks, stopRest *atomic.Bool,
	finJob bool, finEnv []string, moving func(int)) string {
	// A finally job runs after an earlier job failed, and through the
	// first stop.
	stopped := ir.stopping
	if finJob {
		stopped = ir.stopsFinallyJob
	}
	setCell := func(fn func(*status.Lane)) {
		status.Update(o.Root, n, o.Cfg.Lanes, func(l *status.Lane) { fn(l.CellEntry(c.Name)) })
	}
	// A matrix cell's lines read "[3] [orders] ..."; a job's "[1.3] ...".
	label, job := c.Name, ""
	stopWhy := "Matrix-Fail: stop after a failed cell"
	if info.IsGroup() {
		label, job, stopWhy = info.SubLabel(n, c.Name), info.SubLabel(n, c.Name), "an earlier job failed"
	}
	line := func(text string) {
		if job != "" {
			disp.JobLine(n, job, text)
		} else {
			disp.Line(n, "["+label+"] "+text)
		}
	}
	skipCell := func(why string) string {
		setCell(func(e *status.Lane) { e.State, e.Reason = status.Skipped, why })
		line(fmt.Sprintf("SKIP (%s)", why))
		return status.Skipped
	}
	if stopRest.Load() && !finJob {
		return skipCell(stopWhy)
	}
	if info.IsGroup() && !info.NativeJobs() {
		path, ok := o.manifests[n].JobScript(c.Name)
		if !ok {
			return skipCell("its staged file doesn't define it")
		}
		p, err := runner.Resolve(o.Cfg, path)
		if err != nil {
			return skipCell(err.Error())
		}
		plan = p
	}
	if stopped() {
		return skipCell("interrupted before start")
	}
	ci := info.ForCell(c)
	// The cell's own locks ($KEY substituted).
	var cellLocks []string
	if len(perCell) > 0 {
		cellLocks = lane.Info{Locks: perCell}.ForCell(c).Locks
		if !lt.acquire(n, cellLocks, func(name string, holder int) {
			setCell(func(e *status.Lane) { e.State, e.Reason = status.Locked, "waiting for lock "+name })
		}) {
			return skipCell("interrupted before start")
		}
		defer lt.release(n, cellLocks)
	}
	files := append(lane.FileLocks(nil), laneLocks...)
	if len(cellLocks) > 0 {
		fl, ok, err := lane.WaitFileLocks(o.Root, cellLocks, lane.LockHolder(n, ci.Job, o.RunID), func(string, string) {}, ir.stopping)
		if err != nil || !ok {
			return skipCell("could not take its locks")
		}
		defer fl.Release()
		files = append(files, fl...)
	}
	if finJob && ir.stopping() {
		// The slots are closed once a run stops; finally work needs none.
	} else if !sl.acquire(n, func(int) {}) {
		return skipCell("interrupted before start")
	} else {
		defer sl.release()
	}
	if stopRest.Load() && !finJob {
		return skipCell(stopWhy)
	}
	prefix := "[" + label + "] "
	if job != "" {
		prefix = ""
	}
	// The staged files, checked again right before this cell starts.
	if err := o.manifests[n].Verify(); err != nil {
		line("blocked: " + err.Error())
		setCell(func(e *status.Lane) { e.State, e.Reason = status.Failed, "blocked: "+err.Error() })
		return status.Failed
	}
	who := approval.Caller{Cell: c.Name, Job: ci.Job}
	cmd, ownGroup, w, tok := laneCommand(o, n, sel, plan, append(append(c.Env(o.Root, n), lane.MatrixJobEnv+"="+info.Job), finEnv...), prefix, job, who, disp, files)
	defer exited(o, tok)
	if stopped() {
		return skipCell("interrupted before start")
	}
	launched := time.Now()
	if err := cmd.Start(); err != nil {
		line(fmt.Sprintf("swim: cannot start: %v", err))
		setCell(func(e *status.Lane) { e.State, e.ExitCode = status.Failed, status.Int(127) })
		return status.Failed
	}
	started(o, tok, cmd.Process.Pid)
	moving(+1)
	key := n*1000 + idx + 1
	ir.add(key, &laneProc{proc: cmd.Process, group: ownGroup, started: launched, lane: n, cell: c.Name})
	err := cmd.Wait()
	w.Flush()
	ir.remove(key)
	moving(-1)
	code := exitCode(err)
	// The cell's hooks recorded how it ended; a cell that died before its
	// round started is recorded here.
	state := status.Passed
	if st, _ := status.Load(o.Root); st != nil {
		if l := st.Get(n); l != nil {
			if e := l.FindCell(c.Name); e != nil && e.Run == o.RunID {
				switch e.State {
				case status.Passed, status.Failed, status.Interrupted:
					return e.State
				}
			}
		}
	}
	switch {
	case code == 130 || code == 143 || code == 137:
		state = status.Interrupted
	case code != 0:
		state = status.Failed
	}
	setCell(func(e *status.Lane) {
		e.State, e.ExitCode = state, status.Int(code)
		e.Reason = "the cell's script exited before its round started"
	})
	return state
}

// cellSummary is a matrix lane's cell counts: "2/4 pass · 1 running · 1
// waiting".
func cellSummary(total int, results map[string]string, running int) string {
	counts := map[string]int{}
	for _, s := range results {
		counts[s]++
	}
	parts := []string{fmt.Sprintf("%d/%d pass", counts[status.Passed], total)}
	for _, k := range []string{status.Failed, status.Interrupted, status.Skipped} {
		if counts[k] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
		}
	}
	if running > 0 {
		parts = append(parts, fmt.Sprintf("%d running", running))
	}
	if waiting := total - len(results) - running; waiting > 0 {
		parts = append(parts, fmt.Sprintf("%d waiting", waiting))
	}
	return strings.Join(parts, " · ")
}

// CellNames are a lane's cells, sorted, for messages.
func CellNames(cells []lane.Cell) []string {
	out := make([]string, len(cells))
	for i, c := range cells {
		out[i] = c.Name
	}
	sort.Strings(out)
	return out
}

// finallyJobEnv is a finally job's context: how each earlier sibling (a
// job under the same parent, and those under it) ended, as
// SWIM_FINALLY_<N>_<K> (K the job's path, dots as underscores), and
// SWIM_FINALLY.
func finallyJobEnv(n int, name string, earlier []lane.Cell, results map[string]string) []string {
	parent := ""
	if i := strings.LastIndex(name, "."); i >= 0 {
		parent = name[:i]
	}
	all := true
	var env []string
	for _, c := range earlier {
		if parent != "" && !lane.PathUnder(c.Name, parent) {
			continue
		}
		word := finallyWord(results[c.Name])
		if word != "pass" {
			all = false
		}
		env = append(env, fmt.Sprintf("SWIM_FINALLY_%d_%s=%s", n, strings.ReplaceAll(c.Name, ".", "_"), word))
	}
	if all {
		return append(env, "SWIM_FINALLY=pass")
	}
	return append(env, "SWIM_FINALLY=fail")
}
