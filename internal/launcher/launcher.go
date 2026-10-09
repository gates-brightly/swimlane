// Package launcher runs lanes in parallel, orders dependent lanes, relays
// their output with [N] prefixes and prints a summary. It never needs
// editing per round: what to run comes from the lane scripts on disk and the
// dependency table in config.
package launcher

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/runrec"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/step"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// Options configures a launch.
type Options struct {
	Root   string
	Cfg    *config.Config
	Lanes  []int  // explicit selection; empty means every lane with a pending round
	Rerun  bool   // with no explicit lanes, also run rounds that already passed
	DryRun bool   // plan only: don't refuse lanes that are running
	RunID  string // this run's id; generated if empty (swim run --run-id)
	// Parallel caps lanes running at once; nil means config's max_parallel,
	// 0 unlimited.
	Parallel *int
	Plain    bool
	Self     string // path of the swim binary, exported to lane scripts as SWIM_BIN
	Out      *os.File
	Stdin    *os.File
	// Finished, if set, is called once after the summary prints (the live
	// view is gone by then), e.g. to chime.
	Finished func(Finish)
	// YAML, if set, receives the run as a stream of YAML documents (swim
	// run --yaml) instead of the live view and summary table on Out;
	// YAMLOutput adds lane output lines to it.
	YAML       io.Writer
	YAMLOutput bool
	// Interrupt overrides config interrupt for this run: graceful or
	// immediate (swim run --interrupt).
	Interrupt string

	// Interactive allows the interactive view (keys pick a lane's log) when
	// more than one lane runs on a terminal; the view then reads Stdin.
	Interactive bool

	ev *events
}

// Finish is a completed run's outcome, passed to Options.Finished.
type Finish struct {
	Code        int            // Run's exit code: 0 only if every lane passed
	Interrupted bool           // Ctrl-C or a signal arrived during the run
	Counts      map[string]int // lanes by final state (status.Passed, ...)
	Elapsed     time.Duration
}

// Outcome is one lane's result.
type Outcome struct {
	N       int
	Job     string // job id from the script (empty for scripts without one)
	State   string
	Exit    int
	Reason  string
	Round   string
	Blocked bool // refused before starting (blocked command)
	Elapsed time.Duration
	// When the lane's dependencies were met, its locks and slot taken, its
	// process started and finished (zero when it never got that far). The
	// run record (.swim/runs/) keeps them for swim timeline.
	Ready, Locked, Slot, Start, End time.Time
}

// Select resolves which lanes to run and checks none is already running.
// With no explicit lanes it picks every pending round that hasn't already
// passed (unless Rerun), and returns the passed ones as done.
func Select(o Options) (sel, done []int, err error) {
	if len(o.Lanes) > 0 {
		seen := map[int]bool{}
		for _, n := range o.Lanes {
			if !o.Cfg.ValidLane(n) {
				return nil, nil, fmt.Errorf("no swim %d: lanes are numbered 1..%d", n, o.Cfg.Lanes)
			}
			info, err := lane.ReadScript(o.Root, n)
			if err != nil {
				return nil, nil, err
			}
			if !info.Pending() {
				return nil, nil, fmt.Errorf("swim %d has no pending round (%s is missing, a stub, or has no Round: line)", n, filepath.Base(lane.Script(o.Root, n)))
			}
			if !seen[n] {
				sel = append(sel, n)
				seen[n] = true
			}
		}
	} else {
		st, _ := status.Load(o.Root)
		for n := 1; n <= o.Cfg.Lanes; n++ {
			info, err := lane.ReadScript(o.Root, n)
			if err != nil || !info.Pending() {
				continue
			}
			var last *status.Lane
			if st != nil {
				last = st.Get(n)
			}
			if !o.Rerun && AlreadyPassed(info, last) {
				done = append(done, n)
				continue
			}
			sel = append(sel, n)
		}
	}
	sort.Ints(sel)
	for _, n := range sel {
		if pid, ok := lane.Running(o.Root, n); ok && !o.DryRun {
			return nil, nil, lane.ErrRunning{Lane: n, PID: pid}
		}
	}
	return sel, done, nil
}

// Run launches the selected lanes and returns 0 only if all passed.
func Run(o Options) (int, error) {
	sel, passed, err := Select(o)
	if err != nil {
		return 2, err
	}
	o.ev = newEvents(o.YAML, o.YAMLOutput)
	if o.RunID == "" {
		o.RunID = lane.NewRunID()
	}
	if len(sel) == 0 {
		if o.ev != nil {
			note := "nothing pending (every lane is a stub or has no lane script)"
			if len(passed) > 0 {
				note = "every pending job has already passed (swim " + joinInts(passed) + ")"
			}
			o.ev.nothing(o, note)
			return 0, nil
		}
		if len(passed) > 0 {
			fmt.Fprintf(o.Out, "swim: nothing to run: every pending job has already passed (swim %s). Rerun one with `swim run N`, or all with `swim all --rerun`.\n", joinInts(passed))
		} else {
			fmt.Fprintln(o.Out, "swim: nothing pending (every lane is a stub or has no lane script). Write one with `swim new N \"<goal>\"`.")
		}
		return 0, nil
	}
	isDone := map[int]bool{}
	for _, n := range passed {
		isDone[n] = true
	}
	selected := map[int]bool{}
	for _, n := range sel {
		selected[n] = true
	}
	deps, err := ResolveDeps(o.Root, o.Cfg, sel)
	if err != nil {
		return 2, err
	}
	blocked := BlockedLanes(o.Root, o.Cfg, sel)

	// The panel shows every lane 1..N so numbering never has gaps.
	views := make([]display.LaneView, 0, o.Cfg.Lanes)
	for n := 1; n <= o.Cfg.Lanes; n++ {
		info, _ := lane.ReadScript(o.Root, n)
		v := display.LaneView{N: n, State: display.Idle, Round: info.Round, Job: info.Job}
		if isDone[n] {
			v.State = display.Done
		}
		if selected[n] {
			v.State = display.Starting
			for _, d := range deps[n] {
				if selected[d.Lane] {
					v.State = display.Waiting
					v.WaitingOn = append(v.WaitingOn, d.Lane)
				}
			}
		}
		views = append(views, v)
	}
	start := time.Now()
	ref := step.GitRef(o.Root)
	cap := o.Cfg.Parallel()
	if o.Parallel != nil {
		cap = *o.Parallel
	}
	sl := newSlots(cap, chainBelow(sel, deps))
	lt := newLockTable()
	var ir *interrupter
	screen, summaryOut := o.Out, io.Writer(o.Out)
	if o.ev != nil {
		// The YAML stream owns stdout: no live view, no table.
		if devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0); err == nil {
			defer devnull.Close()
			screen = devnull
		}
		summaryOut = io.Discard
	}
	disp := display.New(display.Options{
		Out:   screen,
		Plain: o.Plain || o.ev != nil,
		Cap:   cap,
		Title: func(now time.Time) string {
			t := fmt.Sprintf("swim · %s · %s · %s", filepath.Base(o.Root), ref, display.Elapsed(now.Sub(start)))
			if n := ir.Notice(); n != "" {
				t += " · " + n
			}
			return t
		},
		// The interactive view takes the keyboard only when lanes get no stdin
		// (more than one lane, see runLane). Ctrl-C typed in it (raw mode)
		// goes to the same escalation as the terminal's own (interrupt.go).
		Interactive: o.Interactive && len(sel) > 1 && o.ev == nil,
		In:          o.Stdin,
		LogPath:     func(n int) string { return lane.Log(o.Root, n) },
		LogLabel:    func(n int) string { return fmt.Sprintf(".swim/logs/agent%d.log", n) },
		OnInterrupt: func() { ir.handle(syscall.SIGINT) },
	}, views)
	ir = newInterrupter(o, disp, sl, lt)
	defer ir.stop()

	var (
		mu       sync.Mutex
		outcomes = map[int]*Outcome{}
		done     = map[int]chan struct{}{}
	)
	for _, n := range sel {
		done[n] = make(chan struct{})
	}

	// Lanes run in their own process groups, so Ctrl-C reaches only swim,
	// which decides what the lanes get (interrupt.go): the first press stops
	// them at the next step boundary, the second interrupts and then kills
	// them, the third kills them at once. Lanes not yet started are skipped,
	// and the summary still prints.
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	go func() {
		for s := range sigs {
			ir.handle(s)
		}
	}()

	status.UpdateFile(o.Root, o.Cfg.Lanes, func(f *status.File) error { f.LastRun = o.RunID; return nil })
	runDetail := "swim " + joinInts(sel)
	if cap > 0 {
		runDetail += fmt.Sprintf("  max_parallel=%d", cap)
	}
	history.Log(o.Root, history.Entry{Event: history.Run, Run: o.RunID, Detail: runDetail})
	o.ev.emit(Event{Event: "run", Run: o.RunID, Lanes: sel, MaxParallel: cap, At: now()})
	disp.Start()
	defer disp.Restore()

	var wg sync.WaitGroup
	for _, n := range sel {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			defer close(done[n])
			var out *Outcome
			if reason := blocked[n]; reason != "" {
				out = refuseBlocked(o, n, reason, disp)
			} else {
				out = runLane(o, n, sel, selected, deps[n], sl, lt, disp, done, outcomes, &mu, ir)
			}
			o.ev.laneDone(o, out)
			mu.Lock()
			outcomes[n] = out
			mu.Unlock()
		}(n)
	}
	wg.Wait()
	disp.Stop()

	code := 0
	for _, n := range sel {
		if outcomes[n].State != status.Passed {
			code = 1
		}
	}
	counts := map[string]int{}
	for _, n := range sel {
		counts[outcomes[n].State]++
	}
	history.Log(o.Root, history.Entry{Event: history.RunDone, Run: o.RunID, Detail: fmt.Sprintf("swim %s  passed=%d failed=%d skipped=%d interrupted=%d  %s",
		joinInts(sel), counts[status.Passed], counts[status.Failed], counts[status.Skipped], counts[status.Interrupted], display.Elapsed(time.Since(start)))})
	elapsed := time.Since(start)
	if err := writeRunRecord(o, start, cap, sel, selected, deps, outcomes); err != nil {
		fmt.Fprintln(os.Stderr, "swim: run record: "+err.Error())
	}
	printSummary(summaryOut, o.Root, sel, outcomes, elapsed, disp.Color())
	stopped := ir.stopping()
	o.ev.summary(o, sel, outcomes, code, stopped, elapsed)
	if o.Finished != nil {
		o.Finished(Finish{Code: code, Interrupted: stopped, Counts: counts, Elapsed: elapsed})
	}
	return code, nil
}

func runLane(o Options, n int, sel []int, selected map[int]bool, deps []Dep, sl *slots, lt *lockTable, disp *display.Display,
	done map[int]chan struct{}, outcomes map[int]*Outcome, mu *sync.Mutex, ir *interrupter) *Outcome {

	var ready, locked, slotted time.Time
	stamp := func(oc *Outcome) *Outcome {
		oc.Ready, oc.Locked, oc.Slot = ready, locked, slotted
		if oc.End.IsZero() {
			oc.End = time.Now()
		}
		return oc
	}
	skip := func(reason string) *Outcome {
		disp.Set(n, func(v *display.LaneView) { v.State, v.Reason, v.WaitingOn = display.Skipped, reason, nil })
		info, _ := lane.ReadScript(o.Root, n)
		status.Update(o.Root, n, o.Cfg.Lanes, func(l *status.Lane) {
			// Record which round was skipped: a later plan or retry must see
			// this job as attempted, not new. Counts from an older round go.
			l.ResetRun()
			l.State, l.Reason = status.Skipped, reason
			l.Round, l.Job, l.Run = info.Round, info.Job, o.RunID
			l.FinishedAt = status.Str(status.Now())
		})
		history.Log(o.Root, history.Entry{Event: history.Skip, Lane: n, Job: info.Job, Run: o.RunID, Detail: reason + "  " + info.Round})
		return stamp(&Outcome{N: n, Job: info.Job, Round: info.Round, State: status.Skipped, Exit: -1, Reason: reason})
	}
	// Dependencies outside this run must already be satisfied (see
	// outsideBlocker); those inside it are waited for.
	var waitOn []int
	for _, d := range deps {
		if selected[d.Lane] {
			waitOn = append(waitOn, d.Lane)
		} else if why := outsideBlocker(o.Root, d); why != "" {
			return skip(why)
		}
	}
	for len(waitOn) > 0 {
		remaining := append([]int(nil), waitOn...)
		disp.Set(n, func(v *display.LaneView) { v.State, v.WaitingOn = display.Waiting, remaining })
		status.Update(o.Root, n, o.Cfg.Lanes, func(l *status.Lane) {
			l.State, l.WaitingOn, l.Reason, l.PID = status.Waiting, remaining, "", os.Getpid()
		})
		o.ev.emit(Event{Event: "waiting", Lane: n, On: remaining, At: now()})
		d := waitOn[0]
		<-done[d]
		mu.Lock()
		dep := outcomes[d]
		mu.Unlock()
		if dep == nil || dep.State != status.Passed {
			state := "did not finish"
			if dep != nil {
				state = dep.State
			}
			return skip(fmt.Sprintf("swim %d %s", d, state))
		}
		waitOn = waitOn[1:]
	}
	ready = time.Now()
	if ir.stopping() {
		return skip("interrupted before start")
	}
	// Take the round's resource locks (# Locks:), all at once: first from the
	// other lanes of this run, then across concurrent runs (lock files). A
	// lane waiting for a lock holds no slot.
	info, _ := lane.ReadScript(o.Root, n)
	var fileLocks lane.FileLocks
	if len(info.Locks) > 0 {
		lockWait := func(text string) {
			disp.Set(n, func(v *display.LaneView) { v.State, v.LockWait, v.WaitingOn = display.Locked, text, nil })
			status.Update(o.Root, n, o.Cfg.Lanes, func(l *status.Lane) {
				l.State, l.WaitingOn, l.Reason = status.Locked, []int{}, "waiting for lock "+text
			})
			history.Log(o.Root, history.Entry{Event: history.Locked, Lane: n, Job: info.Job, Run: o.RunID, Detail: "waiting for lock " + text})
			o.ev.emit(Event{Event: "locked", Lane: n, Lock: text, At: now()})
		}
		if !lt.acquire(n, info.Locks, func(name string, holder int) {
			if holder > 0 {
				lockWait(fmt.Sprintf("%s: swim %d", name, holder))
			} else {
				lockWait(name + ": queued behind another lane")
			}
		}) {
			return skip("interrupted before start")
		}
		defer lt.release(n, info.Locks)
		var ok bool
		var err error
		fileLocks, ok, err = lane.WaitFileLocks(o.Root, info.Locks, lane.LockHolder(n, info.Job, o.RunID),
			func(name, by string) { lockWait(fmt.Sprintf("%s: another run, %s", name, by)) },
			ir.stopping)
		if err != nil {
			disp.Line(n, "swim: lock files: "+err.Error())
			return skip("could not take locks: " + err.Error())
		}
		if !ok {
			return skip("interrupted before start")
		}
		defer fileLocks.Release()
		locked = time.Now()
		status.Update(o.Root, n, o.Cfg.Lanes, func(l *status.Lane) { l.Locks = append([]string(nil), info.Locks...) })
		defer status.Update(o.Root, n, o.Cfg.Lanes, func(l *status.Lane) { l.Locks = nil })
	}

	// Wait for a free slot (max_parallel); queued lanes don't hold one.
	loggedQueue := false
	if !sl.acquire(n, func(ahead int) {
		disp.Set(n, func(v *display.LaneView) { v.State, v.QueuePos, v.WaitingOn = display.Queued, ahead, nil })
		status.Update(o.Root, n, o.Cfg.Lanes, func(l *status.Lane) {
			l.State, l.WaitingOn, l.Reason = status.Queued, []int{}, fmt.Sprintf("%d ahead", ahead)
		})
		if !loggedQueue {
			loggedQueue = true
			info, _ := lane.ReadScript(o.Root, n)
			history.Log(o.Root, history.Entry{Event: history.Queued, Lane: n, Job: info.Job, Run: o.RunID, Detail: fmt.Sprintf("%d ahead", ahead)})
			o.ev.emit(Event{Event: "queued", Lane: n, Ahead: intp(ahead), At: now()})
		}
	}) {
		return skip("interrupted before start")
	}
	defer sl.release()
	slotted = time.Now()

	cmd := exec.Command("bash", lane.Script(o.Root, n))
	cmd.Dir = o.Root
	env := append(os.Environ(), "SWIM_BIN="+o.Self, "SWIM_LAUNCHED=1",
		"SWIM_RUN="+o.RunID, "SWIM_RUN_LANES="+joinInts(sel, " "))
	if disp.Color() {
		env = append(env, "SWIM_COLOR=1")
	} else {
		env = append(env, "SWIM_COLOR=0")
	}
	cmd.Env = env
	// Parallel lanes can't share a keyboard: give them no stdin so
	// fail-closed prompts stop instead of hanging. A lone lane may prompt.
	if len(sel) == 1 && o.Stdin != nil {
		cmd.Stdin = o.Stdin
	}
	// Its own process group, so the terminal's Ctrl-C reaches only swim.
	// Not for a lane reading the terminal: a background group that reads
	// it is stopped (SIGTTIN). That lane gets Ctrl-C from the terminal
	// directly, at once, as before.
	ownGroup := cmd.Stdin == nil || !ui.IsTTY(o.Stdin)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: ownGroup}
	// The lane's process inherits the lock files, so the locks stay held as
	// long as the lane runs, even if this launcher dies.
	cmd.ExtraFiles = fileLocks
	mk := o.Cfg.Masker(os.Environ())
	w := &lineWriter{emit: func(s string) {
		s = mk.String(s)
		disp.Line(n, s)
		if o.ev.wantsOutput() {
			o.ev.emit(Event{Event: "output", Lane: n, Line: &s})
		}
	}}
	cmd.Stdout, cmd.Stderr = w, w

	launched := time.Now()
	launchedTS := launched.UTC().Format(time.RFC3339)
	disp.Set(n, func(v *display.LaneView) {
		v.State, v.WaitingOn, v.Started, v.Round, v.Job = display.Running, nil, launched, info.Round, info.Job
	})
	if err := cmd.Start(); err != nil {
		disp.Line(n, "swim: cannot start lane script: "+err.Error())
		oc := finish(o, n, disp, launched, launchedTS, 127)
		oc.Job, oc.Round, oc.Start = info.Job, info.Round, launched
		return stamp(oc)
	}
	o.ev.emit(Event{Event: "start", Lane: n, Job: info.Job, Round: info.Round, At: launchedTS})
	ir.add(n, &laneProc{proc: cmd.Process, group: ownGroup, started: launched})
	err := cmd.Wait()
	w.Flush()
	ir.remove(n)

	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				code = 128 + int(ws.Signal())
			}
		} else {
			code = 1
		}
	}
	end := time.Now()
	oc := finish(o, n, disp, launched, launchedTS, code)
	oc.Job, oc.Round, oc.Start, oc.End = info.Job, info.Round, launched, end
	return stamp(oc)
}

func finish(o Options, n int, disp *display.Display, started time.Time, startedTS string, code int) *Outcome {
	state := status.Passed
	switch {
	case code == 130 || code == 143 || code == 129 || code == 137:
		state = status.Interrupted
	case code != 0:
		state = status.Failed
	}
	now := time.Now()
	disp.Set(n, func(v *display.LaneView) {
		v.Exit, v.Finished = code, now
		switch state {
		case status.Passed:
			v.State = display.Passed
		case status.Interrupted:
			v.State = display.Interrupted
		default:
			v.State = display.Failed
		}
	})
	// The lane script records its own final state. If it died before
	// lane_init (syntax error, missing swim), record it here instead.
	died := false
	status.Update(o.Root, n, o.Cfg.Lanes, func(l *status.Lane) {
		if l.StartedAt != nil && *l.StartedAt >= startedTS {
			return
		}
		died = true
		l.ResetRun()
		l.State = state
		l.StartedAt = status.Str(startedTS)
		l.FinishedAt = status.Str(status.Now())
		l.DurationS = status.Float(now.Sub(started).Round(100 * time.Millisecond).Seconds())
		l.ExitCode = status.Int(code)
		l.Reason = "lane script exited before lane_init"
	})
	if !died && code > 128 {
		closeKilledRound(o, n, code)
	}
	if died {
		info, _ := lane.ReadScript(o.Root, n)
		history.Log(o.Root, history.Entry{Event: history.Fail, Lane: n, Job: info.Job, Run: o.RunID,
			Detail: fmt.Sprintf("exit=%d  lane script exited before lane_init  %s", code, info.Round)})
	}
	return &Outcome{N: n, State: state, Exit: code, Elapsed: now.Sub(started)}
}

// lineWriter splits a byte stream into lines for the display.
type lineWriter struct {
	buf  []byte
	emit func(string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := indexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.emit(strings.TrimRight(string(w.buf[:i]), "\r"))
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

// Flush emits any trailing partial line.
func (w *lineWriter) Flush() {
	if len(w.buf) > 0 {
		w.emit(strings.TrimRight(string(w.buf), "\r"))
		w.buf = nil
	}
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

func printSummary(out io.Writer, root string, sel []int, outcomes map[int]*Outcome, total time.Duration, color bool) {
	p := ui.Painter{On: color}
	fmt.Fprintln(out)
	fmt.Fprintln(out, p.Paint(ui.Bold, fmt.Sprintf("swim summary (%s)", display.Elapsed(total))))
	fmt.Fprintln(out, p.Paint(ui.Dim, fmt.Sprintf("  %-8s %-8s %-12s %4s %5s %5s %5s %6s %7s", "lane", "job", "result", "exit", "pass", "fail", "skip", "drift", "time")))
	for _, n := range sel {
		oc := outcomes[n]
		label := p.Paint(ui.LaneColor(n)+ui.Bold, fmt.Sprintf("%-8s", fmt.Sprintf("swim %d", n)))
		r, _ := logparse.ParseFile(lane.Log(root, n))
		job := oc.Job
		if job == "" && oc.State != status.Skipped {
			job = r.Job
		}
		if job == "" {
			job = "-"
		}
		label += " " + p.Paint(ui.Dim, fmt.Sprintf("%-8s", lane.ShortJob(job)))
		if oc.Blocked { // refused before starting: no round of its own in the log
			fmt.Fprintf(out, "  %s %s %4d %5s %5s %5s %6s %7s\n", label, p.Paint(ui.Red, fmt.Sprintf("%-12s", "FAIL")), oc.Exit, "-", "-", "-", "-", "-")
			fmt.Fprintf(out, "  %-17s %s\n", "", p.Paint(ui.Red, oc.Reason))
			continue
		}
		result := strings.ToUpper(oc.State)
		if oc.State == status.Passed {
			result = "PASS"
		} else if oc.State == status.Failed {
			result = "FAIL"
		} else if oc.State == status.Skipped {
			result = "SKIP"
		}
		res := p.Paint(ui.StateColor(result), fmt.Sprintf("%-12s", result))
		if oc.State == status.Skipped {
			fmt.Fprintf(out, "  %s %s %4s %5s %5s %5s %6s %7s  %s\n", label, res, "-", "-", "-", "-", "-", "-", oc.Reason)
			continue
		}
		fmt.Fprintf(out, "  %s %s %4d %5d %5d %5d %6d %7s\n", label, res, oc.Exit, r.Pass, r.Fail, r.Skip, r.Drift, display.Elapsed(oc.Elapsed))
		for _, f := range r.Failed() {
			fmt.Fprintf(out, "  %-17s %s\n", "", p.Paint(ui.Red, f.Text()))
		}
	}
	fmt.Fprintln(out, p.Paint(ui.Dim, "  logs: swim log N (or "+logList(sel)+")   status: swim status"))
}

func logList(sel []int) string {
	parts := make([]string, len(sel))
	for i, n := range sel {
		parts[i] = fmt.Sprintf(".swim/logs/agent%d.log", n)
	}
	return strings.Join(parts, " ")
}

// joinInts joins lane numbers with sep (default ",").
func joinInts(ns []int, sep ...string) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = fmt.Sprint(n)
	}
	s := ","
	if len(sep) > 0 {
		s = sep[0]
	}
	return strings.Join(parts, s)
}

// writeRunRecord saves when each lane became ready, started and finished,
// and the dependencies this run resolved, so swim timeline can draw any past
// run even after its scripts change.
func writeRunRecord(o Options, start time.Time, cap int, sel []int, selected map[int]bool, deps map[int][]Dep, outcomes map[int]*Outcome) error {
	r := &runrec.Run{Run: o.RunID, Started: start.UTC(), Finished: time.Now().UTC(), MaxParallel: cap}
	for _, n := range sel {
		oc := outcomes[n]
		l := runrec.Lane{Lane: n, Job: oc.Job, Round: oc.Round, State: oc.State, Reason: oc.Reason, After: []int{}}
		for _, d := range deps[n] {
			if selected[d.Lane] {
				l.After = append(l.After, d.Lane)
			}
		}
		if info, err := lane.ReadScript(o.Root, n); err == nil {
			l.Locks = info.Locks
		}
		if oc.Exit >= 0 {
			e := oc.Exit
			l.Exit = &e
		}
		l.Ready = runrec.Since(start, oc.Ready)
		l.Locked = runrec.Since(start, oc.Locked)
		l.Slot = runrec.Since(start, oc.Slot)
		l.Start = runrec.Since(start, oc.Start)
		l.End = runrec.Since(start, oc.End)
		r.Lanes = append(r.Lanes, l)
	}
	return runrec.Write(o.Root, r)
}

// closeKilledRound finishes the round of a lane killed by a signal (SIGKILL
// is a force quit's last resort): its bash never ran _finish, so the step
// output in flight is recovered and the round's END and final state are
// recorded. A lane that ended its round itself is left alone.
func closeKilledRound(o Options, n, code int) {
	st, _ := status.Load(o.Root)
	if st == nil || st.Get(n) == nil || st.Get(n).State != status.Running {
		return
	}
	cmd := exec.Command(o.Self, "_finish", fmt.Sprint(n), "--exit", fmt.Sprint(code))
	cmd.Dir = o.Root
	cmd.Env = append(os.Environ(), "SWIM_COLOR=0")
	cmd.Run()
}
