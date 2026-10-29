package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/chars"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/outputs"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/step"
)

// In-lane commands: the lane library's functions as swim commands, for
// lane scripts in any language (lane.N.py, lane.N.ts, a binary, ...):
//
//	swim run [options] "<label>" -- cmd [args...]
//	swim gate [options] "<label>" -- cmd [args...]
//	swim snapshot [options] "<label>" -- cmd [args...]
//	swim guard FLAG "<action - date, reason>"
//	swim stage NAME
//	swim drift "<what differs>"
//	swim stop "<reason>"
//
// They read the lane from SWIM_LANE, which swim's wrapper (swim _wrap)
// exports. Ending the round (a failed gate, swim stop, a refused step)
// signals the wrapper, which stops the program even if it ignores the
// exit code. Once a round's end is claimed, the command that claimed it
// doesn't return to the program until the wrapper has stopped it, and a
// later step command refuses to run: no step starts after a failed gate.

// laneEnv is the lane number from SWIM_LANE.
func laneEnv(cmd string) (string, error) {
	n := os.Getenv("SWIM_LANE")
	if n == "" {
		return "", fmt.Errorf("swim %s runs inside a lane's round (SWIM_LANE is unset): swim run N starts one", cmd)
	}
	return n, nil
}

// hasDashDash reports whether args has a "--" separator.
func hasDashDash(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return true
		}
	}
	return false
}

// laneStep runs one step for swim run/gate/snapshot inside a lane: the
// options and label, then the command after "--".
func laneStep(cmd string, args []string) error {
	n, err := laneEnv(cmd)
	if err != nil {
		return err
	}
	var opts []string
	soft := false // in a Fail: hard round, this step's failure doesn't end it
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--timeout" || a == "--retry" || a == "--backoff" || a == "--retry-on" || a == "--key" || a == "--target":
			if i+1 >= len(args) {
				return usagef("%s needs a value", a)
			}
			opts = append(opts, a, args[i+1])
			i++
			continue
		case a == "--no-retry-timeout" || a == "--expect-fail" || strings.HasPrefix(a, "--expect-fail=") || a == "--checkpoint" || strings.HasPrefix(a, "--checkpoint="):
			opts = append(opts, a)
			continue
		case a == "--soft":
			soft = true
			continue
		}
		break
	}
	rest := args[i:]
	if len(rest) > 0 && rest[0] == "--" {
		rest = rest[1:]
	}
	if len(rest) < 2 {
		return usagef(`usage: swim %s [options] "<label>" -- cmd [args...]`, cmd)
	}
	label, command := rest[0], rest[1:]
	if command[0] == "--" {
		command = command[1:]
	}
	if len(command) == 0 {
		return usagef(`usage: swim %s [options] "<label>" -- cmd [args...]`, cmd)
	}
	if err := refuseEnded(cmd, n, label); err != nil {
		return err
	}
	stepArgs := append([]string{"--label", label}, opts...)
	switch cmd {
	case "snapshot":
		stepArgs = append(stepArgs, "--snapshot")
	}
	code := 0
	if err := cmdStep(append(append(stepArgs, "--"), command...)); err != nil {
		var ee exitError
		if !errors.As(err, &ee) {
			return err
		}
		code = ee.code
	}
	root, _, ln, err := laneCtx(n)
	if err != nil {
		return err
	}
	logPath := os.Getenv("STEP_LOG")
	switch {
	case code == step.ExitStopped:
		// The operator asked the lane to stop: the round ends here.
		after := "before the first step"
		if st, _ := status.Load(root); st != nil {
			if l := st.Get(ln); l != nil && l.CurrentStep != "" {
				after = "after: " + l.CurrentStep
			}
		}
		return endRound(root, ln, 130, "interrupted by operator ("+after+")")
	case code == 87:
		return endRound(root, ln, 1, "blocked command: "+label)
	case code == 124 && logPath != "" && exists(step.RoundTimeoutPath(logPath)):
		os.Remove(step.RoundTimeoutPath(logPath))
		return endRound(root, ln, 1, "timeout: the round's Timeout ("+os.Getenv("SWIM_TIMEOUT")+") ran out")
	case cmd == "gate" && code != 0:
		return endRound(root, ln, 1, "gate failed: "+label)
	case code != 0 && os.Getenv("SWIM_FAIL_MODE") == "hard" && !soft:
		return endRound(root, ln, 1, "hard failure: "+label)
	}
	if code != 0 {
		return exitError{code}
	}
	return nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// endRound records STOP reason and ends lane n's round with code: inside
// swim's wrapper, by claiming the end and signalling it (it stops the
// program); in a bash lane script, which ends its own round, or in an
// outcome handler, which nothing stops, by recording the STOP only. Either
// way the command exits with code.
func endRound(root string, n, code int, reason string) error {
	ns := strconv.Itoa(n)
	if os.Getenv("SWIM_WRAPPED") == "" {
		// A bash lane ends its own round; in a handler, nothing ends it.
		cmdMark([]string{ns, logparse.Stop, reason})
		return exitError{code}
	}
	if lane.ClaimEnd(root, n, code) {
		cmdMark([]string{ns, logparse.Stop, reason})
	}
	signalEnd()
	return exitError{code}
}

// refuseEnded keeps an in-lane step command (swim run/gate/snapshot/
// guard) from running once the round's end is claimed: the
// wrapper is stopping the program, and a step must never start after a
// failed gate or a stop, however quickly the program got to it. It returns
// nil if the round goes on (or the command runs in a handler, after the
// round's end, or in a bash lane, which ends its own round).
func refuseEnded(cmd, ns, label string) error {
	if os.Getenv("SWIM_WRAPPED") == "" {
		return nil
	}
	root, _, n, err := laneCtx(ns)
	if err != nil {
		return nil // the command itself reports it
	}
	code, ended := lane.EndCode(root, n)
	if !ended {
		return nil
	}
	fmt.Fprintf(os.Stderr, "swim %s: the round has ended; %q not run\n", cmd, label)
	signalEnd()
	return exitError{max(code, 1)} // not run is never a pass
}

// signalEnd tells the wrapper (SWIM_PID) that the round's end is claimed,
// then waits for it to stop the program that ran this command, so the
// program can't go on to its next line meanwhile. Run by the wrapper
// itself (its lane socket, for an SDK), it returns at once: the SDK raises
// RoundEnded and unwinds. The wait is bounded, should the program outlive
// its stop.
func signalEnd() {
	pid, err := strconv.Atoi(os.Getenv("SWIM_PID"))
	if err != nil || pid <= 0 {
		return
	}
	syscall.Kill(pid, syscall.SIGUSR1)
	parent := os.Getppid()
	if parent == pid || parent == 1 {
		return
	}
	// The wrapper's stop reaches this command too when it leads the
	// program's process group; otherwise the program's exit reparents it.
	for deadline := time.Now().Add(endWait); time.Now().Before(deadline) && os.Getppid() == parent; {
		time.Sleep(10 * time.Millisecond)
	}
}

// endWait bounds signalEnd's wait: past the wrapper's SIGTERM-to-SIGKILL
// window.
const endWait = 10 * time.Second

// swim guard FLAG "<action>": exit 0 if the operator approves the action,
// 1 if not (recorded as a dry run). As the library's guard: FLAG=1 approves
// up front; unset, swim asks the operator (or uses signed approvals).
func cmdGuard(args []string) error {
	n, err := laneEnv("guard")
	if err != nil {
		return err
	}
	if len(args) != 2 {
		return usagef(`usage: swim guard FLAG "<action - date, reason>"`)
	}
	flag, desc := args[0], args[1]
	if !chars.Ident(flag) {
		return usagef("bad guard flag name: %s", flag)
	}
	if err := refuseEnded("guard", n, desc); err != nil {
		return err
	}
	val, set := os.LookupEnv(flag)
	switch {
	case val == "1" && launched():
		// Under swim run the launcher says whether the flag counts.
		return cmdApproveHook([]string{n, flag, desc, "--flag-set"})
	case val == "1":
		// Run directly: a guard whose lookup.yml rule asks for
		// evidence-bound approvals can't be approved, the flag included.
		if root, _, _, err := laneCtx(n); err == nil {
			if lk, err := lookup.Load(root); err == nil {
				if why := lk.EvidenceWhy(flag); why != "" {
					cmdMark([]string{n, logparse.Warn, "approval refused: " + flag, why})
					cmdMark([]string{n, logparse.Skip, desc, "dry run: " + why})
					return exitError{1}
				}
			}
		}
		detail := flag + "=1"
		if strings.Contains(" "+os.Getenv("SWIM_QUEUE_APPROVED")+" ", " "+flag+" ") {
			detail += ", approved in " + os.Getenv("SWIM_QUEUE")
		}
		return cmdMark([]string{n, logparse.Approved, desc, detail})
	case !set:
		return cmdApproveHook([]string{n, flag, desc})
	}
	if err := cmdMark([]string{n, logparse.Skip, desc, "dry run: set " + flag + "=1 to approve"}); err != nil {
		return err
	}
	return exitError{1}
}

// swim stage NAME: start a stage (snapshot, check, change, verify).
func cmdStagePublic(args []string) error {
	n, err := laneEnv("stage")
	if err != nil {
		return err
	}
	if len(args) != 1 {
		return usagef("usage: swim stage snapshot|check|change|verify")
	}
	if err := cmdStage([]string{n, args[0]}); err != nil {
		root, _, ln, cerr := laneCtx(n)
		if cerr != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "swim stage: "+err.Error())
		return endRound(root, ln, 1, "bad stage: "+args[0]+" (stages are snapshot, check, change, verify)")
	}
	return nil
}

// swim drift "<what differs>": record DRIFT (deployed state differs from code).
func cmdDrift(args []string) error {
	n, err := laneEnv("drift")
	if err != nil {
		return err
	}
	if len(args) != 1 {
		return usagef(`usage: swim drift "<what differs>"`)
	}
	return cmdMark([]string{n, logparse.Drift, args[0]})
}

// swim stop "<reason>": record STOP and end the round now (exit 1). It
// runs only inside a lane.
func cmdStop(args []string) error {
	n, err := laneEnv("stop")
	if err != nil {
		return err
	}
	if len(args) > 1 {
		return usagef(`usage: swim stop "<reason>"`)
	}
	reason := "stopped"
	if len(args) == 1 && args[0] != "" {
		reason = args[0]
	}
	root, _, ln, err := laneCtx(n)
	if err != nil {
		return err
	}
	return endRound(root, ln, 1, reason)
}

// laneGate and laneSnapshot are swim gate / swim snapshot.
func laneGate(args []string) error     { return laneStep("gate", args) }
func laneSnapshot(args []string) error { return laneStep("snapshot", args) }

// swim msg [--progress N/M] "<text>": post the lane's status message (the
// latest wins; "" clears it). It shows in the live view and swim status,
// and goes to the log throttled (msg_log_interval). It never fails
// the round: a message that can't be posted warns and exits 0.
func cmdMsg(args []string) error {
	var progress string
	rest, err := flags{strs: map[string]*string{"progress": &progress}}.parse(args)
	if err != nil {
		return err
	}
	warn := func(format string, a ...any) error {
		fmt.Fprintf(os.Stderr, "swim msg: warning: "+format+"\n", a...)
		return nil
	}
	ns := os.Getenv("SWIM_LANE")
	if ns == "" {
		return warn("not inside a lane's round (SWIM_LANE is unset); nothing posted")
	}
	root, cfg, n, err := laneCtx(ns)
	if err != nil {
		return warn("%v", err)
	}
	if progress != "" && !lane.ValidProgress(progress) {
		warn("--progress %q is not N/M; ignored", progress)
		progress = ""
	}
	text := cfg.Masker(os.Environ()).String(lane.CleanMsg(strings.Join(rest, " ")))
	if text == "" {
		progress = ""
	}
	now := time.Now()
	m, _ := lane.ReadMsg(root, n)
	if run := os.Getenv("SWIM_RUN"); m.Run != run {
		m = lane.Msg{Run: run} // an earlier round's message (and log state) is spent
	}
	m.Text, m.Progress, m.At = text, progress, now.UTC().Format(time.RFC3339)

	// The log: changes only, at most one line per msg_log_interval. A
	// message posted during a step waits for the step, which writes it into
	// its own block when it ends (nothing else writes into a running step).
	if text != "" && m.Display() != m.Logged {
		last, _ := time.Parse(time.RFC3339, m.LoggedAt)
		if every := cfg.MsgLogEvery(); every == 0 || now.Sub(last) >= every {
			line := logparse.MsgText(text, progress, now.Format("15:04:05"))
			logPath := lane.Log(root, n)
			if _, err := os.Stat(step.SpoolPath(logPath)); err == nil {
				if len(m.Pending) < 100 {
					m.Pending = append(m.Pending, line)
				}
			} else if err := appendLog(logPath, "  "+line+"\n"); err != nil {
				return warn("%v", err)
			}
			m.Logged, m.LoggedAt = m.Display(), m.At
		}
	}
	if err := lane.WriteMsg(root, n, m); err != nil {
		return warn("%v", err)
	}
	status.Update(root, n, cfg.Lanes, func(l *status.Lane) {
		l.Message, l.Progress, l.MessageAt = m.Text, m.Progress, m.At
		if m.Text == "" {
			l.MessageAt = ""
		}
	})
	// Nothing printed: inside a step it would land in the step's output.
	// swim run shows it ([3] … migrating billing (7/25)) as it changes.
	return nil
}

// swim output KEY VALUE: record a small value among the round's outputs
// (read by dependent lanes as `input N KEY`).
func cmdOutput(args []string) error {
	if _, err := laneEnv("output"); err != nil {
		return err
	}
	if len(args) != 2 {
		return usagef("usage: swim output KEY VALUE")
	}
	dir := os.Getenv("SWIM_OUT")
	if dir == "" {
		return fmt.Errorf("swim output: SWIM_OUT is unset (a round started by an older swim?)")
	}
	// A value is kept in .swim/out/ (and hashed in the chain): masked, like
	// everything else swim writes. Files a lane writes to $SWIM_OUT itself
	// are its own.
	value := args[1]
	if _, cfg, err := repoNoMigrate(); err == nil {
		value = cfg.Masker(os.Environ()).String(value)
	}
	return outputs.SetValue(dir, args[0], value)
}

// swim input N|JOB NAME: print the path of an output (or a small value) of
// a round this lane depends on: lane N's round this run waited on, or the
// exact round of job JOB. Only lanes and jobs in this round's After: (or
// config deps) may be read. What was read, and its hash, is recorded for
// the chain. A failure is recorded as FAIL and exits 1.
func cmdInput(args []string) error {
	ns, err := laneEnv("input")
	if err != nil {
		return err
	}
	if len(args) != 2 {
		return usagef("usage: swim input N|JOB NAME")
	}
	ref, name := args[0], strings.TrimSuffix(args[1], "/")
	root, cfg, n, err := laneCtx(ns)
	if err != nil {
		return err
	}
	fail := func(format string, a ...any) error {
		why := fmt.Sprintf(format, a...)
		cmdMark([]string{ns, logparse.Fail, "input " + ref + " " + name, why})
		return exitError{1}
	}
	info, _ := lane.ReadScript(root, n)
	job, run, sums, err := inputRound(root, cfg, n, info, ref)
	if err != nil {
		return fail("%v", err)
	}
	dir := outputs.Dir(root, job, run)
	var path, sum, value string
	isValue := false
	if s, ok := sums[outputs.ValuePrefix+name]; ok && outputs.ValidKey(name) {
		v, ok := outputs.Value(dir, name)
		if !ok {
			return fail("its value %s is gone from %s", name, rel(root, dir))
		}
		isValue, value, sum = true, v, s
		if got := chain.Hash([]byte(v)); got != s {
			return fail("value %s changed after the round recorded it", name)
		}
	} else if _, ok := sums[name]; ok {
		path = filepath.Join(dir, name)
		got, _, _, err := outputs.Hash(path)
		if err != nil {
			return fail("%s is gone from %s", name, rel(root, dir))
		}
		sum = got
	} else {
		var names []string
		for k := range sums {
			names = append(names, k)
		}
		sort.Strings(names)
		return fail("that round declared no output %s (its outputs: %s)", name, or(strings.Join(names, ", "), "none"))
	}
	if out := os.Getenv("SWIM_OUT"); out != "" {
		outputs.AddInput(out, outputs.Input{Key: ref + ":" + name, Sum: sum, Round: job + " " + run})
	}
	if isValue {
		fmt.Print(value)
		return nil
	}
	fmt.Println(path)
	return nil
}

// inputRound finds the round lane n may read ref's outputs from: lane ref's
// last round (which must have passed), or job ref's latest passed round.
// ref must be one of the round's dependencies.
func inputRound(root string, cfg *config.Config, n int, info lane.Info, ref string) (job, run string, sums map[string]string, err error) {
	st, _ := status.Load(root)
	if k, numErr := strconv.Atoi(ref); numErr == nil {
		dep := slices.Contains(cfg.DepsOf(n), k)
		for _, a := range info.After {
			dep = dep || a == ref
		}
		if !dep {
			return "", "", nil, fmt.Errorf("swim %d is not in this round's After: (or config deps); only dependencies' outputs can be read", k)
		}
		var l *status.Lane
		if st != nil {
			l = st.Get(k)
		}
		if l == nil || l.State != status.Passed {
			return "", "", nil, fmt.Errorf("swim %d's last round hasn't passed", k)
		}
		return l.Job, l.Run, l.Outputs, nil
	}
	dep := false
	for _, a := range info.After {
		dep = dep || lane.MatchJob(a, ref) || lane.MatchJob(ref, a)
	}
	if !dep {
		return "", "", nil, fmt.Errorf("job %s is not in this round's After:; only dependencies' outputs can be read", ref)
	}
	if st != nil {
		for _, l := range st.Lanes {
			if lane.MatchJob(l.Job, ref) && l.State == status.Passed && l.Outputs != nil {
				return l.Job, l.Run, l.Outputs, nil
			}
		}
	}
	links, _ := chain.Read(root)
	for i := len(links) - 1; i >= 0; i-- {
		e := links[i]
		if e.Type == "round" && lane.MatchJob(e.Job, ref) && e.Result == status.Passed {
			return e.Job, e.Run, e.Outputs, nil
		}
	}
	return "", "", nil, fmt.Errorf("no passed round of job %s found", ref)
}
