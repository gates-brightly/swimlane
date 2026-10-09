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

	"swim/internal/config"
	"swim/internal/display"
	"swim/internal/lane"
	"swim/internal/logparse"
	"swim/internal/status"
	"swim/internal/step"
	"swim/internal/ui"
)

// Options configures a launch.
type Options struct {
	Root  string
	Cfg   *config.Config
	Lanes []int // explicit selection; empty means every lane with a pending round
	Plain bool
	Self  string // path of the swim binary, exported to lane scripts as SWIM_BIN
	Out   *os.File
	Stdin *os.File
}

// Outcome is one lane's result.
type Outcome struct {
	N       int
	Job     string // job id from the script (empty for scripts without one)
	State   string
	Exit    int
	Reason  string
	Elapsed time.Duration
}

// Select resolves which lanes to run and checks none is already running.
func Select(o Options) ([]int, error) {
	var sel []int
	if len(o.Lanes) > 0 {
		seen := map[int]bool{}
		for _, n := range o.Lanes {
			if !o.Cfg.ValidLane(n) {
				return nil, fmt.Errorf("no swim %d: lanes are numbered 1..%d", n, o.Cfg.Lanes)
			}
			info, err := lane.ReadScript(o.Root, n)
			if err != nil {
				return nil, err
			}
			if !info.Pending() {
				return nil, fmt.Errorf("swim %d has no pending round (%s is missing, a stub, or has no Round: line)", n, filepath.Base(lane.Script(o.Root, n)))
			}
			if !seen[n] {
				sel = append(sel, n)
				seen[n] = true
			}
		}
	} else {
		for n := 1; n <= o.Cfg.Lanes; n++ {
			if info, err := lane.ReadScript(o.Root, n); err == nil && info.Pending() {
				sel = append(sel, n)
			}
		}
	}
	sort.Ints(sel)
	for _, n := range sel {
		if pid, ok := lane.Running(o.Root, n); ok {
			return nil, lane.ErrRunning{Lane: n, PID: pid}
		}
	}
	return sel, nil
}

// Run launches the selected lanes and returns 0 only if all passed.
func Run(o Options) (int, error) {
	sel, err := Select(o)
	if err != nil {
		return 2, err
	}
	if len(sel) == 0 {
		fmt.Fprintln(o.Out, "swim: nothing pending (every lane is a stub or has no lane script). Write one with `swim new N \"<goal>\"`.")
		return 0, nil
	}
	selected := map[int]bool{}
	for _, n := range sel {
		selected[n] = true
	}

	// The panel shows every lane 1..N so numbering never has gaps.
	views := make([]display.LaneView, 0, o.Cfg.Lanes)
	for n := 1; n <= o.Cfg.Lanes; n++ {
		info, _ := lane.ReadScript(o.Root, n)
		v := display.LaneView{N: n, State: display.Idle, Round: info.Round, Job: info.Job}
		if selected[n] {
			v.State = display.Queued
			for _, d := range o.Cfg.DepsOf(n) {
				if selected[d] {
					v.State = display.Waiting
					v.WaitingOn = append(v.WaitingOn, d)
				}
			}
		}
		views = append(views, v)
	}
	start := time.Now()
	ref := step.GitRef(o.Root)
	disp := display.New(display.Options{
		Out:   o.Out,
		Plain: o.Plain,
		Title: func(now time.Time) string {
			return fmt.Sprintf("swim · %s · %s · %s", filepath.Base(o.Root), ref, display.Elapsed(now.Sub(start)))
		},
	}, views)

	var (
		mu          sync.Mutex
		interrupted bool
		procs       = map[int]*os.Process{}
		outcomes    = map[int]*Outcome{}
		done        = map[int]chan struct{}{}
	)
	for _, n := range sel {
		done[n] = make(chan struct{})
	}

	// Ctrl-C reaches the lanes directly (same process group); the launcher
	// stays up so it can skip lanes that haven't started and still print
	// the summary. SIGTERM is aimed at us, so pass it on.
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	go func() {
		for s := range sigs {
			mu.Lock()
			interrupted = true
			if s != syscall.SIGINT {
				for _, p := range procs {
					p.Signal(s)
				}
			}
			mu.Unlock()
		}
	}()

	disp.Start()
	defer disp.Restore()

	var wg sync.WaitGroup
	for _, n := range sel {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			defer close(done[n])
			out := runLane(o, n, sel, selected, disp, done, outcomes, &mu, &interrupted, procs)
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
	printSummary(o.Out, o.Root, sel, outcomes, time.Since(start), disp.Color())
	return code, nil
}

func runLane(o Options, n int, sel []int, selected map[int]bool, disp *display.Display,
	done map[int]chan struct{}, outcomes map[int]*Outcome, mu *sync.Mutex, interrupted *bool, procs map[int]*os.Process) *Outcome {

	// Wait for dependencies that are part of this run. A dependency outside
	// the run counts as satisfied: the operator chose not to run it now.
	var waitOn []int
	for _, d := range o.Cfg.DepsOf(n) {
		if selected[d] {
			waitOn = append(waitOn, d)
		}
	}
	skip := func(reason string) *Outcome {
		disp.Set(n, func(v *display.LaneView) { v.State, v.Reason, v.WaitingOn = display.Skipped, reason, nil })
		status.Update(o.Root, n, o.Cfg.Lanes, func(l *status.Lane) {
			l.State, l.Reason, l.WaitingOn, l.PID = status.Skipped, reason, []int{}, 0
		})
		info, _ := lane.ReadScript(o.Root, n)
		return &Outcome{N: n, Job: info.Job, State: status.Skipped, Exit: -1, Reason: reason}
	}
	for len(waitOn) > 0 {
		remaining := append([]int(nil), waitOn...)
		disp.Set(n, func(v *display.LaneView) { v.State, v.WaitingOn = display.Waiting, remaining })
		status.Update(o.Root, n, o.Cfg.Lanes, func(l *status.Lane) {
			l.State, l.WaitingOn, l.Reason, l.PID = status.Waiting, remaining, "", os.Getpid()
		})
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
	mu.Lock()
	stop := *interrupted
	mu.Unlock()
	if stop {
		return skip("interrupted before start")
	}

	info, _ := lane.ReadScript(o.Root, n)
	cmd := exec.Command("bash", lane.Script(o.Root, n))
	cmd.Dir = o.Root
	env := append(os.Environ(), "SWIM_BIN="+o.Self, "SWIM_LAUNCHED=1")
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
	w := &lineWriter{emit: func(s string) { disp.Line(n, s) }}
	cmd.Stdout, cmd.Stderr = w, w

	launched := time.Now()
	launchedTS := launched.UTC().Format(time.RFC3339)
	disp.Set(n, func(v *display.LaneView) {
		v.State, v.WaitingOn, v.Started, v.Round, v.Job = display.Running, nil, launched, info.Round, info.Job
	})
	if err := cmd.Start(); err != nil {
		disp.Line(n, "swim: cannot start lane script: "+err.Error())
		oc := finish(o, n, disp, launched, launchedTS, 127)
		oc.Job = info.Job
		return oc
	}
	mu.Lock()
	procs[n] = cmd.Process
	mu.Unlock()
	err := cmd.Wait()
	w.Flush()
	mu.Lock()
	delete(procs, n)
	mu.Unlock()

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
	oc := finish(o, n, disp, launched, launchedTS, code)
	oc.Job = info.Job
	return oc
}

func finish(o Options, n int, disp *display.Display, started time.Time, startedTS string, code int) *Outcome {
	state := status.Passed
	switch {
	case code == 130 || code == 143 || code == 129:
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
	status.Update(o.Root, n, o.Cfg.Lanes, func(l *status.Lane) {
		if l.StartedAt != nil && *l.StartedAt >= startedTS {
			return
		}
		l.ResetRun()
		l.State = state
		l.StartedAt = status.Str(startedTS)
		l.FinishedAt = status.Str(status.Now())
		l.DurationS = status.Float(now.Sub(started).Round(100 * time.Millisecond).Seconds())
		l.ExitCode = status.Int(code)
		l.Reason = "lane script exited before lane_init"
	})
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
	fmt.Fprintln(out, p.Paint(ui.Dim, "  logs: "+logList(sel)+"   status: swim status / cat .swim/status.yml"))
}

func logList(sel []int) string {
	parts := make([]string, len(sel))
	for i, n := range sel {
		parts[i] = fmt.Sprintf("agent%d.log", n)
	}
	return strings.Join(parts, " ")
}
