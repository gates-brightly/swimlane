package cli

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/assets"
	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/envfile"
	"github.com/gates-brightly/swimlane/internal/evidence"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/manifest"
	"github.com/gates-brightly/swimlane/internal/outputs"
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
	if err := ownLane(n); err != nil {
		return "", nil, 0, err
	}
	return root, cfg, n, nil
}

// ownLane refuses a hook for any lane but the one this process belongs to.
// Under swim run the launcher says which that is (op whoami, by the lane
// process's approval token and process tree), so lane 1 can't mark, stage
// or finish lane 2's round.
func ownLane(n int) error {
	tok := os.Getenv(approval.TokenEnv)
	if !launched() || tok == "" {
		return nil
	}
	who, err := approval.Whoami(os.Getenv(approval.EnvVar), tok)
	var why string
	switch {
	case err != nil:
		why = "can't tell which lane this process belongs to: " + err.Error()
	case who.Lane != n:
		why = fmt.Sprintf("lane %d can't report for lane %d", who.Lane, n)
	case who.Run != "" && os.Getenv("SWIM_RUN") != "" && who.Run != os.Getenv("SWIM_RUN"):
		why = fmt.Sprintf("lane %d of run %s can't report for run %s", n, who.Run, os.Getenv("SWIM_RUN"))
	case who.Cell != lane.CurrentCell():
		why = fmt.Sprintf("lane %d [%s] can't report for [%s]", n, who.Cell, lane.CurrentCell())
	default:
		return nil
	}
	fmt.Fprintln(os.Stderr, "swim: refused: "+why)
	return exitError{2}
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
	// Chain of custody: the exact script this round runs (the launcher's
	// staged copy), kept by hash; and with signing: required, no round
	// starts unsigned.
	m := manifest.FromEnv(n)
	scriptSum, err := keepScript(root, n, m)
	if err != nil {
		lane.RemovePID(root, n)
		return err
	}
	if _, err := runnerKey(cfg); err != nil {
		lane.RemovePID(root, n)
		return fmt.Errorf("swim %d: %w (set signing_key, or signing: auto)", n, err)
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
	// A stop request left from an earlier run doesn't apply to this round.
	if !lane.StopRequested(root, n, run) {
		lane.ClearStop(root, n)
	}
	// The round's outputs directory ($SWIM_OUT), fresh for this run.
	if err := os.MkdirAll(outputs.Dir(root, job, run), 0o755); err != nil {
		lane.RemovePID(root, n)
		return err
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
	commit := os.Getenv("SWIM_COMMIT") // set by swim ci
	if commit != "" {
		ctx = append(ctx, logparse.KV{K: "commit", V: commit})
	}
	if info.FailHard {
		ctx = append(ctx, logparse.KV{K: "fail", V: "hard"})
	}
	if len(info.Handlers) > 0 {
		var names []string
		for _, h := range info.Handlers {
			names = append(names, h.Name)
		}
		ctx = append(ctx, logparse.KV{K: "handlers", V: strings.Join(names, " ") + " (" + info.HandlerBudget().String() + ")"})
	}
	if lane.CheckMode() {
		ctx = append(ctx, logparse.KV{K: "check", V: "read-only preview: stops where change begins"})
	}
	ctx = append(ctx, logparse.KV{K: "sha256", V: scriptSum[:12]})
	manSum := ""
	if m != nil {
		manSum = m.SHA256
		ctx = append(ctx, logparse.KV{K: "manifest", V: manSum[:12] + " (staged " + rel(root, m.Dir) + ")"})
	}
	// The env files that applied (their values are in the lane's env, and
	// masked), and their hash for the chain.
	envSum := ""
	if r, err := envfile.Resolve(root, cfg, n); err != nil {
		lane.RemovePID(root, n)
		return err
	} else if len(r.Sources) > 0 {
		envSum = r.SHA256()
		ctx = append(ctx, logparse.KV{K: "env-files", V: strings.Join(r.Sources, ", ")})
	}
	if _, err := os.Stat(lane.RCFile(root, n)); err == nil {
		info.Problems = append(info.Problems, fmt.Sprintf(".lane.%d.rc is deprecated: move its plain KEY=VALUE lines to a [lane %d] section of .env.swim (swim doctor --fix does it); a guard flag it sets no longer approves", n, n))
	}
	if cfg.Runtime != "" {
		ctx = append(ctx, logparse.KV{K: "runtime", V: step.RuntimeVersion(cfg.Runtime)})
	}
	mk := cfg.Masker(os.Environ())
	if env := step.EnvLine(cfg.HeaderEnv, mk); env != "" {
		ctx = append(ctx, logparse.KV{K: "env", V: env})
	}
	for _, kv := range info.Extra {
		ctx = append(ctx, logparse.KV{K: strings.ToLower(kv.K), V: mk.String(kv.V)})
	}
	if names := mk.Names(); len(names) > 0 {
		ctx = append(ctx, logparse.KV{K: "masked", V: fmt.Sprintf("%d vars (%s)", len(names), strings.Join(names, ", "))})
	}
	header := logparse.RoundHeader(ts, job, mk.String(round), ctx)
	for _, prob := range info.Problems {
		header += logparse.MarkLine(logparse.Warn, prob, "") + "\n"
	}
	if err := appendLog(logPath, header); err != nil {
		lane.RemovePID(root, n)
		return err
	}
	p := painter()
	fmt.Fprintln(os.Stderr, laneBanner(p, root, n, "")+"  "+p.Paint(ui.Bold, "Round: "+round)+"  "+p.Paint(ui.Dim, "job "+job))
	for _, prob := range info.Problems {
		fmt.Fprintln(os.Stderr, p.Paint(ui.Yellow, "WARN  "+prob))
	}
	if !lane.CheckMode() {
		history.Log(root, history.Entry{Event: history.Start, Lane: n, Job: job, Run: run, Detail: round})
	}
	stepTimeout := "0"
	if info.StepTimeout > 0 {
		stepTimeout = info.StepTimeoutText
	}
	failMode := "soft"
	if info.FailHard {
		failMode = "hard"
	}
	// stdout carries "<job> <deadline epoch or 0> <run> <step timeout or 0>
	// <fail mode> <handler budget, seconds> <timeout>" back to lane_init,
	// which exports SWIM_JOB, SWIM_DEADLINE, SWIM_RUN, SWIM_STEP_TIMEOUT,
	// SWIM_FAIL_MODE, SWIM_HANDLER_TIMEOUT and SWIM_TIMEOUT.
	defer fmt.Printf("%s %d %s %s %s %d %s\n", job, deadline, run, stepTimeout, failMode, int(info.HandlerBudget().Seconds()), timeoutText)
	return status.UpdateFile(root, cfg.Lanes, func(f *status.File) error {
		f.Branch = ref
		l := f.Mine(n)
		l.ResetRun()
		l.State = status.Running
		l.Round = info.Round
		l.Job = job
		l.Run = run
		l.Commit = commit
		l.ScriptSHA = scriptSum
		l.ManifestSHA = manSum
		l.EnvSHA = envSum
		l.Script = filepath.Base(lane.Script(root, n))
		l.Pending, l.PendingJob = info.Round, info.Job
		l.PID = pid
		l.StartedAt = status.Str(ts)
		l.Stage = syntax.Setup
		return nil
	})
}

// finishOutputs checks and records lane n's round outputs as it ends: a
// promised output that's missing is a FAIL line, undeclared files a WARN,
// over 1 GB a WARN. It returns the hashes, what the round read, and the
// END block's outputs line ("" if none).
func finishOutputs(root string, n int, logPath string) (map[string]string, []outputs.Input, string) {
	st, _ := status.Load(root)
	var l *status.Lane
	if st != nil {
		l = st.Mine(n)
	}
	if l == nil || l.Job == "" || l.Run == "" {
		return nil, nil, ""
	}
	dir := outputs.Dir(root, l.Job, l.Run)
	info, _ := lane.ReadScript(root, n)
	res, err := outputs.Check(dir, info.Outputs)
	if err != nil {
		appendLog(logPath, logparse.MarkLine(logparse.Warn, "outputs: "+err.Error(), "")+"\n")
		return nil, outputs.Inputs(dir), ""
	}
	var lines []string
	for _, m := range res.Missing {
		lines = append(lines, logparse.MarkLine(logparse.Fail, "declared output missing: "+m, "Outputs:"))
	}
	if len(res.Undeclared) > 0 {
		lines = append(lines, logparse.MarkLine(logparse.Warn, "undeclared files in $SWIM_OUT: "+strings.Join(res.Undeclared, ", "), "kept, not offered to other lanes; list them in Outputs:"))
	}
	if res.Size > 1<<30 {
		lines = append(lines, logparse.MarkLine(logparse.Warn, "outputs total "+outputs.HumanSize(res.Size), "over 1 GB; .swim/out/ is pruned by swim doctor --fix"))
	}
	if len(lines) > 0 {
		appendLog(logPath, strings.Join(lines, "\n")+"\n")
	}
	outputs.Freeze(dir)
	var parts []string
	for _, o := range res.Outputs {
		what := outputs.HumanSize(o.Size)
		if o.Files > 1 {
			what = fmt.Sprintf("%d files, %s", o.Files, what)
		}
		parts = append(parts, fmt.Sprintf("%s %s %s", o.Name, what, o.Sum[:12]))
	}
	line := ""
	if len(parts) > 0 {
		line = "   outputs: " + strings.Join(parts, " | ") + "\n"
	}
	return res.Sums(), outputs.Inputs(dir), line
}

func inputMap(in []outputs.Input) map[string]string {
	if len(in) == 0 {
		return nil
	}
	m := map[string]string{}
	for _, i := range in {
		m[i.Key] = i.Sum
	}
	return m
}

func inputRounds(in []outputs.Input) map[string]string {
	if len(in) == 0 {
		return nil
	}
	m := map[string]string{}
	for _, i := range in {
		m[strings.SplitN(i.Key, ":", 2)[0]] = i.Round
	}
	return m
}

// finishCheck ends a read-only check (swim plan --check): its END block in
// the check log, the check's fields in status (nothing else), and a check
// entry in the chain.
func finishCheck(root string, cfg *config.Config, n int, logPath, exitS string) error {
	r, err := logparse.ParseFile(logPath)
	if err != nil {
		return err
	}
	failed := r.Failed()
	code := 0
	if exitS != "" {
		code, _ = strconv.Atoi(exitS)
	}
	result, word, stop := "checked", "CHECKED", ""
	for _, f := range failed {
		if f.Kind == logparse.Stop && strings.HasPrefix(f.Label, "gate failed: ") {
			stop = strings.TrimPrefix(f.Label, "gate failed: ")
		}
	}
	switch {
	case stop != "":
		result, word = "stopped", "FAIL"
	case len(failed) > 0 || code != 0:
		result, word = "failed", "FAIL"
	}
	if code == 130 || code == 143 {
		result, word = "interrupted", "INTERRUPTED"
	}
	if result != "checked" && code == 0 {
		code = 1
	}
	var guards []string
	for _, res := range r.Results {
		if res.Kind == logparse.Skip {
			if m := dryRunRE.FindStringSubmatch(res.Detail); m != nil {
				guards = append(guards, m[1])
			}
		}
	}
	now := time.Now()
	var dur time.Duration
	if t, err := time.Parse(time.RFC3339, r.StartedAt); err == nil {
		dur = now.Sub(t)
	}
	if err := appendLog(logPath, logparse.EndBlock(word, r, code, fmt.Sprintf("%.1fs", dur.Seconds()), now.UTC().Format(time.RFC3339))); err != nil {
		return err
	}
	sha := scriptSHA(root, n)
	if rounds, _ := splitRounds(logPath); len(rounds) > 0 {
		exit := code
		if err := record(root, cfg, chain.Entry{Type: "check", Run: r.Run, Lane: n, Job: r.Job, Round: r.Title,
			Script: sha, Result: result, Exit: &exit, Log: chain.Hash([]byte(roundSection(rounds[len(rounds)-1])))}); err != nil {
			fmt.Fprintln(os.Stderr, "swim: "+err.Error())
		}
	}
	p := painter()
	detail := ""
	if stop != "" {
		detail = "  stops at gate \"" + stop + "\""
	}
	fmt.Fprintf(os.Stderr, "%s %s%s  pass=%d fail=%d skip=%d  %s\n", laneBanner(p, root, n, " check:"),
		p.Paint(ui.StateColor(word)+ui.Bold, word), detail, r.Pass, r.Fail, r.Skip, display.Elapsed(dur))
	status.UpdateCheck(root, n, cfg.Lanes, func(l *status.Lane) {
		l.CheckResult, l.CheckAt, l.CheckSHA, l.CheckStop, l.CheckGuards = result, now.UTC().Format(time.RFC3339), sha, stop, guards
	})
	if pid, ok := lane.Running(root, n); !ok || pid == os.Getppid() {
		lane.RemovePID(root, n)
	}
	history.Log(root, history.Entry{Event: "check", Lane: n, Job: r.Job, Run: r.Run, Detail: result + "  " + r.Title})
	if code != 0 {
		return exitError{code}
	}
	return nil
}

// ExitRollbackFailed is a failed round's exit code when its on_failure
// handler failed too.
const ExitRollbackFailed = 89

// handlerMap is the round's handler outcomes for the chain (nil if none ran).
func handlerMap(r *logparse.Round) map[string]string {
	var m map[string]string
	for _, h := range r.HandlerResults() {
		if m == nil {
			m = map[string]string{}
		}
		m[h.Name] = h.State
	}
	return m
}

// roundEvidence is the round's evidence hash ("" if it had none).
func roundEvidence(root string, r *logparse.Round) string {
	if ev := evidence.Load(root, r.Job); ev != nil && ev.Run == r.Run {
		return ev.Hash
	}
	return ""
}

// laneEnvSHA is the env files' hash lane n's round started with.
func laneEnvSHA(root string, n int) string {
	if st, _ := status.Load(root); st != nil {
		if l := st.Mine(n); l != nil {
			return l.EnvSHA
		}
	}
	return ""
}

// swim _handler N NAME: the round's outcome handler NAME starts (the
// library or the wrapper calls it as the round ends).
func cmdHandler(args []string) error {
	if len(args) != 2 {
		return usagef("usage: swim _handler N on_success|on_failure|on_exit")
	}
	name := args[1]
	known := false
	for _, h := range lane.HandlerNames {
		known = known || h == name
	}
	if !known {
		return fmt.Errorf("unknown handler %q (handlers: %s)", name, strings.Join(lane.HandlerNames, ", "))
	}
	root, cfg, n, err := laneCtx(args[0])
	if err != nil {
		return err
	}
	if err := appendLog(lane.Log(root, n), "\n"+logparse.HandlerLine(name, time.Now().Format("15:04:05"))+"\n"); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, painter().Paint(ui.Bold, "-- handler "+name+" ("+os.Getenv("SWIM_OUTCOME")+")"))
	return status.Update(root, n, cfg.Lanes, func(l *status.Lane) { l.Stage = name })
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
	if lane.CurrentCell() == "" {
		lane.ClearStop(root, n) // the round is over: a stop request is spent (a cell's lane may still be running)
	}
	// So is its status message; the latest goes to the log if it isn't there.
	if m, ok := lane.ReadMsg(root, n); ok && m.Display() != m.Logged && m.Run == os.Getenv("SWIM_RUN") {
		appendLog(logPath, "  "+logparse.MsgText(m.Text, m.Progress, time.Now().Format("15:04:05"))+"\n")
	}
	lane.ClearMsg(root, n)
	if lane.CheckMode() {
		return finishCheck(root, cfg, n, logPath, exitS)
	}
	// The round's outputs: promised ones must be there; all are hashed and
	// made read-only, then recorded for the lanes that read them.
	outSums, inputs, outLine := finishOutputs(root, n, logPath)
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
	// A failed round whose rollback (on_failure) failed too exits 89, so CI
	// and swim wait can tell it from one that rolled back. The result
	// stays FAIL either way.
	if code != 0 && code != 130 && code != 143 && code != 129 && code != 137 && r.HandlerState(lane.OnFailure) == "FAIL" {
		code = ExitRollbackFailed
	}
	state := status.Passed
	switch {
	case code == 130 || code == 143 || code == 129 || code == 137 || r.Interrupt:
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
	if err := appendLog(logPath, logparse.EndBlock(word, r, code, ds, ts)+outLine); err != nil {
		return err
	}
	// The round's link in the chain of custody: what ran (the script's
	// hash), what it wrote (its log section's hash), how it ended and which
	// approvals it used, signed by the runner.
	if rounds, _ := splitRounds(logPath); len(rounds) > 0 {
		exit := code
		approvals, auto := roundApprovals(root, r.Run, n)
		if err := record(root, cfg, chain.Entry{Type: "round", Run: r.Run, Lane: n, Job: r.Job, Round: r.Title,
			Script: scriptSHA(root, n), Manifest: manifestSHA(root, n), Result: state, Exit: &exit,
			Log: chain.Hash([]byte(roundSection(rounds[len(rounds)-1]))), Approvals: approvals, AutoApproved: auto,
			EnvSHA256: laneEnvSHA(root, n), Handlers: handlerMap(r), Outputs: outSums, Evidence: roundEvidence(root, r),
			Inputs: inputMap(inputs), InputRounds: inputRounds(inputs)}); err != nil {
			fmt.Fprintln(os.Stderr, "swim: "+err.Error())
		}
	}
	stages := r.StageResults()

	p := painter()
	fmt.Fprintf(os.Stderr, "%s %s  pass=%d fail=%d skip=%d drift=%d  exit %d  %s\n",
		laneBanner(p, root, n, " summary:"),
		p.Paint(ui.StateColor(word)+ui.Bold, word), r.Pass, r.Fail, r.Skip, r.Drift, code, display.Span(now.Add(-dur), now))
	fmt.Fprintln(os.Stderr, "  "+p.Paint(ui.Dim, "stages: "+logparse.StagesText(stages)))
	if hs := r.HandlerResults(); len(hs) > 0 {
		fmt.Fprintln(os.Stderr, "  "+p.Paint(ui.Dim, "handlers: "+logparse.StagesText(hs)))
	}
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
		l.Message, l.Progress, l.MessageAt = "", "", ""
		l.Outputs = outSums
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
		l.Handlers = nil
		for _, h := range r.HandlerResults() {
			l.Handlers = append(l.Handlers, h.Name+" "+h.State)
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

var dryRunRE = regexp.MustCompile(`^dry run: (?:.*; )?set ([A-Za-z_][A-Za-z0-9_]*)=1 to approve$`)

// swim _mark N KIND label [detail] [--request ID --guard FLAG] [--confirm]
//
// --request names the launcher's approval an APPROVED mark records: it is
// consumed (once, by this lane process, for FLAG) before anything is
// written, and refused if another lane got it or it was already used.
// Under swim run that is the only APPROVED mark accepted: the launcher
// records the approval in the chain as it is consumed, and a lane can't
// approve itself. --confirm marks a confirm answered at the lane's own
// terminal: logged, but under swim run not an approval in the chain (swim
// didn't see it given).
func cmdMark(args []string) error {
	var request, guard string
	confirmed := false
	if i := slices.Index(args, "--confirm"); i >= 3 {
		confirmed = true
		args = slices.Delete(slices.Clone(args), i, i+1)
	}
	if i := slices.Index(args, "--request"); i >= 3 {
		rest, err := flags{strs: map[string]*string{"request": &request, "guard": &guard}}.parse(args[i:])
		if err != nil || len(rest) > 0 || request == "" || guard == "" {
			return usagef("usage: swim _mark N KIND label [detail] [--request ID --guard FLAG] [--confirm]")
		}
		args = args[:i]
	}
	if len(args) < 3 {
		return usagef("usage: swim _mark N KIND label [detail] [--request ID --guard FLAG] [--confirm]")
	}
	if (request != "" || confirmed) && args[1] != logparse.Approved {
		return usagef("--request and --confirm are for APPROVED marks")
	}
	recorded := false // the launcher put the approval in the chain
	if args[1] == logparse.Approved && request == "" && !confirmed && launched() {
		fmt.Fprintf(os.Stderr, "swim: refused: an APPROVED mark under swim run needs the launcher's approval (guard asks for it): a lane can't approve itself\n")
		return exitError{2}
	}
	if request != "" {
		sock := os.Getenv(approval.EnvVar)
		if sock == "" {
			return fmt.Errorf("approval %s: no swim run to check it with", request)
		}
		hash, err := approval.Consume(sock, os.Getenv(approval.TokenEnv), request, guard)
		if err != nil {
			return fmt.Errorf("refused: %v", err)
		}
		recorded = hash != "" || launched()
	}
	kind, label, detail := args[1], args[2], ""
	if len(args) > 3 {
		detail = args[3]
	}
	if _, cfg, err := repoNoMigrate(); err == nil { // guard reasons, drift, stop text: no secrets
		mk := cfg.Masker(os.Environ())
		label, detail = mk.String(label), mk.String(detail)
	}
	switch kind {
	case logparse.Skip, logparse.Drift, logparse.Approved, logparse.Stop, logparse.Warn, logparse.Blocked, logparse.Fail, logparse.Checked:
	default:
		return usagef("unknown mark kind %q", kind)
	}
	// The terminal, the log and status.yml get the text made safe (it's
	// the lane's: a guard's description, a stop reason); an approval's
	// chain entry keeps it as given, as evidence.
	shownLabel, shownDetail := ui.SafeField(label), ui.SafeField(detail)
	line := logparse.ResultLine(kind, shownLabel, shownDetail)
	logLine := logparse.MarkLine(kind, shownLabel, shownDetail)
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
	if kind == logparse.Approved && !approveRecorded && !recorded && !(confirmed && launched()) {
		// guard with its flag set (FLAG=1, or a queued batch's approval):
		// the runner attests it, as the operator who set it.
		method := "flag"
		if strings.Contains(detail, "approved in queue") {
			method = "queue"
		}
		approver := chain.Approver{Name: operator()}
		if k, _ := runnerKey(cfg); k != nil {
			approver.Key = k.Public
		}
		if err := record(root, cfg, chain.Entry{Type: "approve", Run: os.Getenv("SWIM_RUN"), Lane: n, Job: os.Getenv("SWIM_JOB"),
			Script: scriptSHA(root, n), Guard: guardFlag(detail), Action: label, Method: method, Statement: detail,
			Approvers: []chain.Approver{approver}, Lookup: lookupSHA(root)}); err != nil {
			return err
		}
	}
	return status.Update(root, n, cfg.Lanes, func(l *status.Lane) {
		switch kind {
		case logparse.Skip:
			l.Skip++
		case logparse.Drift:
			l.Drift++
		case logparse.Stop:
			l.FailedSteps = append(l.FailedSteps, line)
		case logparse.Blocked, logparse.Fail:
			l.Fail++
			l.FailedSteps = append(l.FailedSteps, line)
		}
	})
}

// guardFlag is FLAG from an approval detail "FLAG=1..." ("" if none).
func guardFlag(detail string) string {
	if i := strings.Index(detail, "=1"); i > 0 && !strings.ContainsAny(detail[:i], " ,(") {
		return detail[:i]
	}
	return ""
}

// laneName is how this process's round reads in its own banners: "swim 2",
// or for one job of a lane of jobs "swim 1.3".
func laneName(root string, n int) string {
	if cell := lane.CurrentCell(); lane.IsJobPath(cell) {
		if _, ok := lane.LaneJobs(root, n); ok {
			return fmt.Sprintf("swim %d.%s", n, cell)
		}
	}
	return fmt.Sprintf("swim %d", n)
}

// laneBanner is "swim 1.3<suffix>" in bold, "swim" in the lane's colour and
// each part of the id in its own (ui.Painter.Job).
func laneBanner(p ui.Painter, root string, n int, suffix string) string {
	id := strings.TrimPrefix(laneName(root, n), "swim ")
	return p.Paint(ui.LaneColor(n)+ui.Bold, "swim ") + p.Job(id, ui.Bold) + p.Paint(ui.LaneColor(n)+ui.Bold, suffix)
}
