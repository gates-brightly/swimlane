package cli

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"swim/internal/config"
	"swim/internal/display"
	"swim/internal/history"
	"swim/internal/lane"
	"swim/internal/logparse"
	"swim/internal/status"
	"swim/internal/step"
	"swim/internal/ui"
)

// Hidden commands called by the lane script library (lib.sh).

func reraise(s syscall.Signal) {
	signal.Reset(s)
	syscall.Kill(os.Getpid(), s)
	time.Sleep(100 * time.Millisecond)
	os.Exit(128 + int(s))
}

// laneCtx resolves root, config and lane number for a hook.
func laneCtx(arg string) (string, *config.Config, int, error) {
	root, cfg, err := repo()
	if err != nil {
		return "", nil, 0, err
	}
	n, err := laneArg(cfg, arg)
	if err != nil {
		return "", nil, 0, err
	}
	return root, cfg, n, nil
}

func appendLog(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(text)
	return err
}

func painter() ui.Painter { return ui.Painter{On: ui.ColorEnabled(os.Stderr)} }

// swim _start N --pid P --script PATH
func cmdStart(args []string) error {
	var pidS, script string
	rest, err := flags{strs: map[string]*string{"pid": &pidS, "script": &script}}.parse(args)
	if err != nil || len(rest) != 1 {
		return usagef("usage: swim _start N --pid P --script PATH")
	}
	root, cfg, n, err := laneCtx(rest[0])
	if err != nil {
		return err
	}
	pid, _ := strconv.Atoi(pidS)
	if pid <= 0 {
		pid = os.Getppid()
	}
	if want := filepath.Base(lane.Script(root, n)); script != "" && filepath.Base(script) != want {
		fmt.Fprintf(os.Stderr, "swim: warning: lane_init %d called from %s (expected %s)\n", n, filepath.Base(script), want)
	}
	if err := lane.WritePID(root, n, pid); err != nil {
		return err
	}
	info, _ := lane.ReadScript(root, n)
	round := info.Round
	if round == "" {
		round = "(no Round: line)"
	}
	job := info.Job
	if job == "" {
		// Every job gets an id; a hand-written script without a Job: line
		// gets one for this run (it can't be pinned before it starts).
		job = lane.NewJobID()
	}
	ts := status.Now()
	header := fmt.Sprintf("\n%s %s swim %d job=%s\nRound: %s\nscript: %s   pid: %d\n\n",
		logparse.RoundStart, ts, n, job, round, filepath.Base(lane.Script(root, n)), pid)
	if err := appendLog(lane.Log(root, n), header); err != nil {
		lane.RemovePID(root, n)
		return err
	}
	p := painter()
	fmt.Fprintln(os.Stderr, p.Paint(ui.LaneColor(n)+ui.Bold, fmt.Sprintf("swim %d", n))+"  "+p.Paint(ui.Bold, "Round: "+round)+"  "+p.Paint(ui.Dim, "job "+job))
	ref := step.GitRef(root)
	history.Log(root, history.Entry{Event: history.Start, Lane: n, Job: job, Detail: round})
	// stdout carries the job id back to lane_init, which exports SWIM_JOB.
	defer fmt.Println(job)
	return status.UpdateFile(root, cfg.Lanes, func(f *status.File) error {
		f.Branch = ref
		l := f.Get(n)
		l.ResetRun()
		l.State = status.Running
		l.Round = info.Round
		l.Job = job
		l.Pending, l.PendingJob = info.Round, info.Job
		l.PID = pid
		l.StartedAt = status.Str(ts)
		return nil
	})
}

// swim _finish N [--exit CODE]
func cmdFinish(args []string) error {
	var exitS string
	rest, err := flags{strs: map[string]*string{"exit": &exitS}}.parse(args)
	if err != nil || len(rest) != 1 {
		return usagef("usage: swim _finish N [--exit CODE]")
	}
	root, cfg, n, err := laneCtx(rest[0])
	if err != nil {
		return err
	}
	logPath := lane.Log(root, n)
	r, err := logparse.ParseFile(logPath)
	if err != nil {
		return err
	}
	failed := r.Failed()
	code := 0
	if exitS != "" {
		code, _ = strconv.Atoi(exitS)
	} else if len(failed) > 0 {
		code = 1
	}
	state := status.Passed
	switch {
	case code == 130 || code == 143 || code == 129 || r.Interrupt:
		state = status.Interrupted
	case code != 0:
		state = status.Failed
	}

	now := time.Now()
	var dur time.Duration
	if t, err := time.Parse(time.RFC3339, r.StartedAt); err == nil {
		dur = now.Sub(t)
	}
	ds := fmt.Sprintf("%.1fs", dur.Seconds())
	ts := now.UTC().Format(time.RFC3339)

	var b strings.Builder
	b.WriteString(logparse.SummaryLine(ts, n, r, code, ds) + "\n")
	for _, f := range failed {
		b.WriteString("failed: " + f.Text() + "\n")
	}
	b.WriteString(logparse.End + "\n")
	if err := appendLog(logPath, b.String()); err != nil {
		return err
	}

	p := painter()
	word := map[string]string{status.Passed: "PASS", status.Failed: "FAIL", status.Interrupted: "INTERRUPTED"}[state]
	fmt.Fprintf(os.Stderr, "%s %s  pass=%d fail=%d skip=%d drift=%d  exit %d  %s\n",
		p.Paint(ui.LaneColor(n)+ui.Bold, fmt.Sprintf("swim %d summary:", n)),
		p.Paint(ui.StateColor(word)+ui.Bold, word), r.Pass, r.Fail, r.Skip, r.Drift, code, display.Elapsed(dur))
	for _, f := range failed {
		fmt.Fprintln(os.Stderr, "  "+p.Paint(ui.Red, f.Text()))
	}

	failedSteps := []string{}
	for _, f := range failed {
		failedSteps = append(failedSteps, f.Text())
	}
	err = status.Update(root, n, cfg.Lanes, func(l *status.Lane) {
		l.State = state
		l.Round = r.Title
		l.Job = r.Job
		l.PID = 0
		l.WaitingOn = []int{}
		l.FinishedAt = status.Str(ts)
		if l.StartedAt == nil && r.StartedAt != "" {
			l.StartedAt = status.Str(r.StartedAt)
		}
		l.DurationS = status.Float(float64(dur.Round(100*time.Millisecond)) / float64(time.Second))
		l.ExitCode = status.Int(code)
		l.Pass, l.Fail, l.Skip, l.Drift = r.Pass, r.Fail, r.Skip, r.Drift
		l.FailedSteps = failedSteps
	})
	if pid, ok := lane.Running(root, n); !ok || pid == os.Getppid() {
		lane.RemovePID(root, n)
	}
	event := map[string]string{status.Passed: history.Pass, status.Failed: history.Fail, status.Interrupted: history.Interrupted}[state]
	detail := fmt.Sprintf("pass=%d fail=%d skip=%d drift=%d exit=%d %s  %s", r.Pass, r.Fail, r.Skip, r.Drift, code, ds, r.Title)
	if len(failedSteps) > 0 {
		detail += "  | " + strings.Join(failedSteps, "; ")
	}
	history.Log(root, history.Entry{Event: event, Lane: n, Job: r.Job, Detail: detail})
	if err != nil {
		return err
	}
	if code != 0 {
		return exitError{code}
	}
	return nil
}

var dryRunRE = regexp.MustCompile(`^dry run: set ([A-Za-z_][A-Za-z0-9_]*)=1 to approve$`)

// swim _mark N KIND label [detail]
func cmdMark(args []string) error {
	if len(args) < 3 {
		return usagef("usage: swim _mark N KIND label [detail]")
	}
	kind, label, detail := args[1], args[2], ""
	if len(args) > 3 {
		detail = args[3]
	}
	switch kind {
	case logparse.Skip, logparse.Drift, logparse.Approved, logparse.Stop:
	default:
		return usagef("unknown mark kind %q", kind)
	}
	line := logparse.ResultLine(kind, label, detail)
	p := painter()
	fmt.Fprintln(os.Stderr, p.Paint(ui.StateColor(kind), line))

	if args[0] == "" {
		// Library used outside lane_init: log only, if a log is set.
		if l := os.Getenv("STEP_LOG"); l != "" {
			return appendLog(l, line+"\n")
		}
		return nil
	}
	root, cfg, n, err := laneCtx(args[0])
	if err != nil {
		return err
	}
	if m := dryRunRE.FindStringSubmatch(detail); m != nil {
		fmt.Fprintln(os.Stderr, p.Paint(ui.Dim, fmt.Sprintf("  to approve: %s=1 swim run %d", m[1], n)))
	}
	if err := appendLog(lane.Log(root, n), line+"\n"); err != nil {
		return err
	}
	return status.Update(root, n, cfg.Lanes, func(l *status.Lane) {
		switch kind {
		case logparse.Skip:
			l.Skip++
		case logparse.Drift:
			l.Drift++
		case logparse.Stop:
			l.FailedSteps = append(l.FailedSteps, line)
		}
	})
}
