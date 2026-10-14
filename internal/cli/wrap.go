package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/outputs"
	"github.com/gates-brightly/swimlane/internal/redact"
	"github.com/gates-brightly/swimlane/internal/step"
)

// swim _wrap N -- CMD...: run a lane script that doesn't load swim's bash
// library (lane.N.py, lane.N.ts, a binary, a plain bash script) as lane N's
// round. The wrapper does what lane_init and the library's traps do for a
// bash lane: it opens the round (swim _start, with its own pid as the
// lane's), puts the git shim first on PATH, exports the SWIM_* variables the
// in-lane commands (swim run/gate/guard/stage/...) read, and closes the
// round (swim _finish) however the program ends.
//
//   - SIGUSR1 ends the round now: an in-lane command claimed an end (a
//     failed gate, swim stop, a refused step; .swim/laneN.end has the exit
//     code), or the git shim refused a push/commit/pull. The program is
//     stopped (SIGTERM, then SIGKILL after 5s) even if it ignored the
//     failure.
//   - SIGUSR2 is the operator's graceful stop: the stop file already keeps
//     swim run from starting another step, which ends the round there.
//   - A program that never called swim run is recorded as one step: the
//     whole run, with its output (masked), passed or failed by its exit code.
func cmdWrap(args []string) error {
	if len(args) < 3 || args[1] != "--" {
		return usagef("usage: swim _wrap N -- CMD [ARGS...]")
	}
	root, cfg, n, err := laneCtx(args[0])
	if err != nil {
		return err
	}
	argv := args[2:]
	script := argv[len(argv)-1]
	pid := os.Getpid()

	// Signals before _start marks the lane running: the launcher only sends
	// USR2 once status shows this pid.
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	lane.ClearEnd(root, n)
	defer lane.ClearEnd(root, n)

	bin := os.Getenv("SWIM_BIN")
	if bin == "" {
		bin = self()
	}
	env := setEnv(os.Environ(), "SWIM_BIN", bin)
	start := exec.Command(bin, "_start", strconv.Itoa(n), "--pid", strconv.Itoa(pid), "--script", script)
	start.Env, start.Stderr, start.Dir = env, os.Stderr, root
	out, err := start.Output()
	if err != nil {
		return exitError{1} // _start said why
	}
	// "<job> <deadline epoch, 0 for none> <run> <step timeout, 0 for none>
	// <fail mode> <handler budget, seconds> <timeout text>"
	f := strings.SplitN(strings.TrimSpace(string(out)), " ", 7)
	for len(f) < 7 {
		f = append(f, "")
	}
	logPath := lane.Log(root, n)
	runLanes := os.Getenv("SWIM_RUN_LANES")
	if runLanes == "" {
		runLanes = strconv.Itoa(n)
	}
	env = setEnv(env,
		"SWIM_LANE", strconv.Itoa(n), "SWIM_ROOT", root, "STEP_LOG", logPath,
		"SWIM_PID", strconv.Itoa(pid), "SWIM_WRAPPED", "1",
		"PATH", filepath.Join(root, ".swim", "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"SWIM_JOB", f[0], "SWIM_DEADLINE", f[1], "SWIM_RUN", f[2], "SWIM_STEP_TIMEOUT", f[3],
		"SWIM_FAIL_MODE", f[4], "SWIM_HANDLER_TIMEOUT", f[5], "SWIM_TIMEOUT", f[6],
		"SWIM_RUN_LANES", runLanes, "SWIM_OUT", outputs.Dir(root, f[0], f[2]))

	// The program's output goes to the terminal as it is, and (plain,
	// masked) to a capture file: the round's one step if it never calls
	// swim run.
	capPath := logPath + ".wrap"
	capF, err := os.Create(capPath)
	if err != nil {
		return err
	}
	defer os.Remove(capPath)
	mw := cfg.Masker(os.Environ()).Writer(capF)
	capture := &syncWriter{w: &step.StripWriter{W: mw}}

	child := exec.Command(argv[0], argv[1:]...)
	child.Dir, child.Env, child.Stdin = root, env, os.Stdin
	child.Stdout = io.MultiWriter(os.Stdout, capture)
	child.Stderr = io.MultiWriter(os.Stderr, capture)
	began := time.Now()
	code := 0
	if err := child.Start(); err != nil {
		fmt.Fprintf(capture, "swim: cannot start %s: %v\n", step.Quote(argv), err)
		fmt.Fprintf(os.Stderr, "swim: cannot start %s: %v\n", step.Quote(argv), err)
		code = 127
	} else {
		done := make(chan error, 1)
		go func() { done <- child.Wait() }()
		code = waitChild(root, n, child, done, sigs)
	}
	dur := time.Since(began)
	// A step the program started may still be writing its block (it was
	// stopped along with the program): give it a moment.
	for i := 0; i < 30; i++ {
		if _, err := os.Stat(step.SpoolPath(logPath)); err != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	capture.mu.Lock()
	mw.Flush()
	capture.mu.Unlock()
	capF.Close()
	if c, ok := lane.EndCode(root, n); ok {
		code = c
	}

	// Never called swim run: the whole program is the round's one step.
	if r, err := logparse.ParseFile(logPath); err == nil && r.Pass+r.Fail == 0 {
		if in, err := os.Open(capPath); err == nil {
			childCode := code
			if child.ProcessState != nil {
				childCode = exitStatus(child.ProcessState)
			}
			shown := append(append([]string(nil), argv[:len(argv)-1]...), "./"+filepath.Base(script))
			if _, err := step.RecordOutput(logPath, filepath.Base(script), step.Quote(shown), began, dur, childCode, in); err != nil {
				fmt.Fprintln(os.Stderr, "swim: "+err.Error())
			}
			in.Close()
		}
	}

	// The round's outcome handlers (On-Success:, On-Failure:, On-Exit:).
	if info, err := lane.ReadScript(root, n); err == nil && len(info.Handlers) > 0 && !lane.CheckMode() {
		budget, _ := strconv.Atoi(f[5])
		runHandlers(root, cfg.Masker(env), n, bin, env, info, code, time.Duration(budget)*time.Second, sigs)
	}

	fin := []string{"_finish", strconv.Itoa(n)}
	if code != 0 {
		fin = append(fin, "--exit", strconv.Itoa(code))
	}
	finish := exec.Command(bin, fin...)
	finish.Env, finish.Stdout, finish.Stderr, finish.Dir = env, os.Stdout, os.Stderr, root
	if err := finish.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return exitError{ee.ExitCode()}
		}
		return err
	}
	return nil
}

// waitChild waits for the lane's program, acting on signals meanwhile, and
// returns its exit code (128+signal if a signal killed it).
func waitChild(root string, n int, child *exec.Cmd, done chan error, sigs chan os.Signal) int {
	var kill <-chan time.Time
	ending := false
	for {
		select {
		case <-done:
			return exitStatus(child.ProcessState)
		case <-kill:
			child.Process.Kill()
			kill = nil
		case s := <-sigs:
			switch s {
			case syscall.SIGUSR1:
				if ending {
					continue
				}
				ending = true
				if _, claimed := lane.EndCode(root, n); !claimed && lane.ClaimEnd(root, n, 1) {
					// Not an in-lane command: the git shim refused a command.
					cmdMark([]string{strconv.Itoa(n), logparse.Stop, "blocked command: git push/commit/pull (swim never writes to git)"})
				}
				terminate(child)
				kill = time.After(5 * time.Second)
			case syscall.SIGUSR2:
				// Graceful stop: the stop file refuses the next step.
			case syscall.SIGTERM, syscall.SIGHUP:
				if !ending {
					child.Process.Signal(s) // may be aimed at the wrapper alone
				}
			case syscall.SIGINT:
				// The terminal (or the launcher, to the process group) sent
				// it to the program too; wait for it to end.
			}
		}
	}
}

// terminate asks the lane's program to end: its whole process group when
// the wrapper leads one (the launcher starts lanes in their own), so the
// steps it started end too; otherwise the program alone.
func terminate(child *exec.Cmd) {
	if pg, err := syscall.Getpgid(0); err == nil && pg == os.Getpid() {
		syscall.Kill(-pg, syscall.SIGTERM) // the wrapper's own TERM is caught and ignored
		return
	}
	child.Process.Signal(syscall.SIGTERM)
}

func exitStatus(ps *os.ProcessState) int {
	if ps == nil {
		return 1
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ps.ExitCode()
}

// setEnv returns env with each KEY, VALUE pair in kv set (replacing any
// earlier value).
func setEnv(env []string, kv ...string) []string {
	out := make([]string, 0, len(env)+len(kv)/2)
	set := map[string]bool{}
	for i := 0; i+1 < len(kv); i += 2 {
		set[kv[i]] = true
	}
	for _, e := range env {
		if k, _, ok := strings.Cut(e, "="); ok && set[k] {
			continue
		}
		out = append(out, e)
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, kv[i]+"="+kv[i+1])
	}
	return out
}

// syncWriter serialises the program's stdout and stderr copies.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// wrappedOutcome is SWIM_OUTCOME for a wrapped lane's round that ended with
// code: from its last STOP (stopped, interrupted, timeout), else fail or pass.
func wrappedOutcome(r *logparse.Round, code int) (outcome, reason, failedStep, failedStage string) {
	for _, res := range r.Failed() {
		if res.Kind == logparse.Fail && failedStep == "" {
			failedStep, failedStage = res.Label, res.Stage
		}
		if res.Kind == logparse.Stop {
			reason = res.Label
		}
	}
	switch {
	case strings.HasPrefix(reason, "interrupted by operator"):
		outcome = "interrupted"
	case strings.HasPrefix(reason, "timeout:"):
		outcome = "timeout"
	case reason != "":
		outcome = "stopped"
	case code == 130 || code == 143 || code == 129:
		outcome = "interrupted"
	case code != 0 || r.Fail > 0:
		outcome = "fail"
	default:
		outcome = "pass"
	}
	return outcome, reason, failedStep, failedStage
}

// runHandlers runs a wrapped lane's handler commands: on_success if the
// round passed, else on_failure; then on_exit. Each runs with sh -c, soft
// (an in-lane gate or swim stop records a failure and nothing more), with
// the round's outcome in SWIM_OUTCOME and friends, inside one budget. A
// command that never calls swim run is recorded as one step.
func runHandlers(root string, mk *redact.Masker, n int, bin string, env []string, info lane.Info, code int, budget time.Duration, sigs chan os.Signal) {
	logPath := lane.Log(root, n)
	r, err := logparse.ParseFile(logPath)
	if err != nil {
		return
	}
	outcome, reason, failedStep, failedStage := wrappedOutcome(r, code)
	if budget <= 0 {
		budget = lane.DefaultHandlerTimeout
	}
	deadline := time.Now().Add(budget)
	env = setEnv(env, "SWIM_OUTCOME", outcome, "SWIM_STOP_REASON", reason, "SWIM_FAILED_STEP", failedStep,
		"SWIM_FAILED_STAGE", failedStage, "SWIM_IN_HANDLER", "1",
		"SWIM_DEADLINE", strconv.FormatInt(deadline.Unix(), 10), "SWIM_TIMEOUT", "Handler-Timeout "+budget.String())
	first := lane.OnFailure
	if outcome == "pass" {
		first = lane.OnSuccess
	}
	aborted := ""
	for _, name := range []string{first, lane.OnExit} {
		h, ok := info.Handler(name)
		if !ok || h.Cmd == "" {
			continue
		}
		mark := exec.Command(bin, "_handler", strconv.Itoa(n), name)
		mark.Env, mark.Stderr, mark.Dir = env, os.Stderr, root
		mark.Run()
		if aborted != "" {
			cmdMark([]string{strconv.Itoa(n), logparse.Skip, name, "not run: " + aborted})
			continue
		}
		before, _ := logparse.ParseFile(logPath)
		capPath := logPath + ".wrap"
		capF, err := os.Create(capPath)
		if err != nil {
			continue
		}
		mw := mk.Writer(capF)
		capture := &syncWriter{w: &step.StripWriter{W: mw}}
		c := exec.Command("/bin/sh", "-c", h.Cmd)
		c.Dir, c.Env, c.Stdin = root, env, nil
		c.Stdout, c.Stderr = io.MultiWriter(os.Stdout, capture), io.MultiWriter(os.Stderr, capture)
		began := time.Now()
		hcode := 127
		if err := c.Start(); err == nil {
			done := make(chan error, 1)
			go func() { done <- c.Wait() }()
			hcode, aborted = waitHandler(c, done, sigs, deadline)
		} else {
			fmt.Fprintf(capture, "swim: cannot start %s: %v\n", h.Cmd, err)
		}
		capture.mu.Lock()
		mw.Flush()
		capture.mu.Unlock()
		capF.Close()
		after, _ := logparse.ParseFile(logPath)
		if before != nil && after != nil && countSteps(after, name) == countSteps(before, name) {
			if in, err := os.Open(capPath); err == nil {
				step.RecordOutput(logPath, name+": "+mk.String(h.Cmd), mk.String(h.Cmd), began, time.Since(began), hcode, in)
				in.Close()
			}
		}
		os.Remove(capPath)
		if aborted != "" {
			cmdMark([]string{strconv.Itoa(n), logparse.Stop, name + " stopped: " + aborted})
		}
	}
}

// countSteps counts the step results in handler name's section(s).
func countSteps(r *logparse.Round, name string) int {
	k := 0
	for _, res := range r.Results {
		if res.Handler == name && res.Step {
			k++
		}
	}
	return k
}

// waitHandler waits for a handler command within the handlers' deadline;
// Ctrl-C (which the command gets too) skips the remaining handlers. It
// returns the exit code and why the handlers stop ("" if they don't).
func waitHandler(c *exec.Cmd, done chan error, sigs chan os.Signal, deadline time.Time) (int, string) {
	budget := time.NewTimer(time.Until(deadline))
	defer budget.Stop()
	var kill <-chan time.Time
	why := ""
	for {
		select {
		case <-done:
			return exitStatus(c.ProcessState), why
		case <-budget.C:
			why = "the handlers' budget (Handler-Timeout) ran out"
			terminate(c)
			kill = time.After(5 * time.Second)
		case <-kill:
			c.Process.Kill()
			kill = nil
		case s := <-sigs:
			switch s {
			case syscall.SIGINT:
				why = "interrupted"
			case syscall.SIGTERM, syscall.SIGHUP:
				why = "interrupted"
				c.Process.Signal(s)
			}
			// SIGUSR1 (the git shim refused a command) and SIGUSR2 (a stop
			// request): a handler isn't stopped by either.
		}
	}
}
