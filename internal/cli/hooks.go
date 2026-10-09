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

	"github.com/gates-brightly/swimlane/internal/assets"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/step"
	"github.com/gates-brightly/swimlane/internal/syntax"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// Hidden commands called by the lane script library (lib.sh).

func reraise(s syscall.Signal) {
	signal.Reset(s)
	syscall.Kill(os.Getpid(), s)
	time.Sleep(100 * time.Millisecond)
	os.Exit(128 + int(s))
}

// laneCtx resolves root, config and lane number for a hook. Hooks run from
// inside a lane script, so they never migrate files: rewriting a script bash
// is reading would corrupt the run.
func laneCtx(arg string) (string, *config.Config, int, error) {
	root, cfg, err := repoNoMigrate()
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
	// Lane scripts run directly with bash honour the lock too.
	if err := requireVersion(root, true); err != nil {
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
	info, err := lane.ReadScript(root, n)
	if err != nil {
		lane.RemovePID(root, n)
		return err
	}
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
	run := os.Getenv("SWIM_RUN")
	if run == "" {
		run = lane.NewRunID() // run directly with bash: a run of its own
	}
	// The git shim lane_init puts first on PATH (swim never writes to git).
	shim := filepath.Join(root, ".swim", "bin", "git")
	if err := os.MkdirAll(filepath.Dir(shim), 0o755); err == nil {
		os.WriteFile(shim+".tmp", []byte(assets.GitShim), 0o755)
		os.Rename(shim+".tmp", shim)
	}
	logPath := lane.Log(root, n)
	// This lane's own log is safe to convert: bash never reads it.
	if err := logparse.EnsureV2(logPath, n); err != nil {
		lane.RemovePID(root, n)
		return err
	}
	step.RecoverSpool(logPath)

	now := time.Now()
	ts := now.UTC().Format(time.RFC3339)
	ref := step.GitRef(root)
	deadline, timeoutText := int64(0), "none"
	if info.Timeout > 0 {
		deadline, timeoutText = now.Add(info.Timeout).Unix(), info.TimeoutText
	}
	or := func(v, def string) string {
		if v == "" {
			return def
		}
		return v
	}
	ctx := []logparse.KV{
		{K: "run", V: run},
		{K: "script", V: filepath.Base(lane.Script(root, n))},
		{K: "pid", V: strconv.Itoa(pid)},
		{K: "operator", V: or(os.Getenv("USER"), "-")},
		{K: "owner", V: or(info.Owner, "-")},
		{K: "created", V: or(info.Created, "-")},
		{K: "after", V: or(strings.Join(info.After, " "), "-")},
		{K: "timeout", V: timeoutText},
		{K: "step-timeout", V: or(info.StepTimeoutText, "none")},
		{K: "guards", V: or(strings.Join(info.GuardFlags(), " "), "-")},
		{K: "git", V: ref},
	}
	if cfg.Runtime != "" {
		ctx = append(ctx, logparse.KV{K: "runtime", V: step.RuntimeVersion(cfg.Runtime)})
	}
	if env := step.EnvLine(cfg.HeaderEnv); env != "" {
		ctx = append(ctx, logparse.KV{K: "env", V: env})
	}
	for _, kv := range info.Extra {
		ctx = append(ctx, logparse.KV{K: strings.ToLower(kv.K), V: kv.V})
	}
	header := logparse.RoundHeader(ts, job, round, ctx)
	for _, prob := range info.Problems {
		header += logparse.MarkLine(logparse.Warn, prob, "") + "\n"
	}
	if err := appendLog(logPath, header); err != nil {
		lane.RemovePID(root, n)
		return err
	}
	p := painter()
	fmt.Fprintln(os.Stderr, p.Paint(ui.LaneColor(n)+ui.Bold, fmt.Sprintf("swim %d", n))+"  "+p.Paint(ui.Bold, "Round: "+round)+"  "+p.Paint(ui.Dim, "job "+job))
	for _, prob := range info.Problems {
		fmt.Fprintln(os.Stderr, p.Paint(ui.Yellow, "WARN  "+prob))
	}
	history.Log(root, history.Entry{Event: history.Start, Lane: n, Job: job, Run: run, Detail: round})
	stepTimeout := "0"
	if info.StepTimeout > 0 {
		stepTimeout = info.StepTimeoutText
	}
	// stdout carries "<job> <deadline epoch or 0> <run> <step timeout or 0>
	// <timeout>" back to lane_init, which exports SWIM_JOB, SWIM_DEADLINE,
	// SWIM_RUN, SWIM_STEP_TIMEOUT and SWIM_TIMEOUT.
	defer fmt.Printf("%s %d %s %s %s\n", job, deadline, run, stepTimeout, timeoutText)
	return status.UpdateFile(root, cfg.Lanes, func(f *status.File) error {
		f.Branch = ref
		l := f.Get(n)
		l.ResetRun()
		l.State = status.Running
		l.Round = info.Round
		l.Job = job
		l.Run = run
		l.Pending, l.PendingJob = info.Round, info.Job
		l.PID = pid
		l.StartedAt = status.Str(ts)
		l.Stage = syntax.Setup
		return nil
	})
}

// swim _stage N NAME: the lane script entered a stage.
func cmdStage(args []string) error {
	if len(args) != 2 {
		return usagef("usage: swim _stage N NAME")
	}
	name := args[1]
	if syntax.StageIndex(name) < 0 {
		return fmt.Errorf("unknown stage %q: stages are %s (in that order)", name, strings.Join(syntax.Stages, ", "))
	}
	root, cfg, n, err := laneCtx(args[0])
	if err != nil {
		return err
	}
	logPath := lane.Log(root, n)
	r, err := logparse.ParseFile(logPath)
	if err != nil {
		return err
	}
	var warns []string
	seen := map[string]bool{}
	last := -1
	for _, d := range r.Declared {
		seen[d] = true
		last = max(last, syntax.StageIndex(d))
	}
	order := strings.Join(syntax.Stages, ", ")
	switch {
	case seen[name]:
		warns = append(warns, fmt.Sprintf("stage %s declared twice", name))
	case syntax.StageIndex(name) < last:
		warns = append(warns, fmt.Sprintf("stage %s after %s (expected order: %s)", name, syntax.Stages[last], order))
	}
	if name == syntax.Change {
		var missing []string
		for _, need := range []string{syntax.Snapshot, syntax.CheckSt} {
			if !seen[need] {
				missing = append(missing, need)
			}
		}
		if len(missing) > 0 {
			warns = append(warns, fmt.Sprintf("change stage without a %s stage before it", strings.Join(missing, " or ")))
		}
	}
	text := "\n" + logparse.StageLine(name, time.Now().Format("15:04:05")) + "\n"
	for _, w := range warns {
		text += logparse.MarkLine(logparse.Warn, w, "") + "\n"
	}
	if err := appendLog(logPath, text); err != nil {
		return err
	}
	p := painter()
	fmt.Fprintln(os.Stderr, p.Paint(ui.Bold, "-- stage "+name))
	for _, w := range warns {
		fmt.Fprintln(os.Stderr, p.Paint(ui.Yellow, "WARN  "+w))
	}
	return status.Update(root, n, cfg.Lanes, func(l *status.Lane) { l.Stage = name })
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
	step.RecoverSpool(logPath)
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

	word := map[string]string{status.Passed: "PASS", status.Failed: "FAIL", status.Interrupted: "INTERRUPTED"}[state]
	if err := appendLog(logPath, logparse.EndBlock(word, r, code, ds, ts)); err != nil {
		return err
	}
	stages := r.StageResults()

	p := painter()
	fmt.Fprintf(os.Stderr, "%s %s  pass=%d fail=%d skip=%d drift=%d  exit %d  %s\n",
		p.Paint(ui.LaneColor(n)+ui.Bold, fmt.Sprintf("swim %d summary:", n)),
		p.Paint(ui.StateColor(word)+ui.Bold, word), r.Pass, r.Fail, r.Skip, r.Drift, code, display.Elapsed(dur))
	fmt.Fprintln(os.Stderr, "  "+p.Paint(ui.Dim, "stages: "+logparse.StagesText(stages)))
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
		if r.Run != "" {
			l.Run = r.Run
		}
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
		l.Stage = ""
		l.Stages = []string{}
		for _, st := range stages {
			l.Stages = append(l.Stages, st.Name+" "+st.State)
		}
	})
	if pid, ok := lane.Running(root, n); !ok || pid == os.Getppid() {
		lane.RemovePID(root, n)
	}
	event := map[string]string{status.Passed: history.Pass, status.Failed: history.Fail, status.Interrupted: history.Interrupted}[state]
	detail := fmt.Sprintf("pass=%d fail=%d skip=%d drift=%d exit=%d %s  %s", r.Pass, r.Fail, r.Skip, r.Drift, code, ds, r.Title)
	if len(failedSteps) > 0 {
		detail += "  | " + strings.Join(failedSteps, "; ")
	}
	history.Log(root, history.Entry{Event: event, Lane: n, Job: r.Job, Run: r.Run, Detail: detail})
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
	case logparse.Skip, logparse.Drift, logparse.Approved, logparse.Stop, logparse.Warn, logparse.Blocked:
	default:
		return usagef("unknown mark kind %q", kind)
	}
	line := logparse.ResultLine(kind, label, detail)
	logLine := logparse.MarkLine(kind, label, detail)
	p := painter()
	fmt.Fprintln(os.Stderr, p.Paint(ui.StateColor(kind), line))

	if args[0] == "" {
		// Library used outside lane_init: log only, if a log is set.
		if l := os.Getenv("STEP_LOG"); l != "" {
			if err := logparse.EnsureV2(l, 0); err != nil {
				return err
			}
			return appendLog(l, logLine+"\n")
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
	if err := appendLog(lane.Log(root, n), logLine+"\n"); err != nil {
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
		case logparse.Blocked:
			l.Fail++
			l.FailedSteps = append(l.FailedSteps, line)
		}
	})
}
