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
	// <fail mode> <timeout text>"
	f := strings.SplitN(strings.TrimSpace(string(out)), " ", 6)
	for len(f) < 6 {
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
		"SWIM_FAIL_MODE", f[4], "SWIM_TIMEOUT", f[5],
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
	// Its workspace: the staged copy under swim run (SWIM_WORKSPACE).
	child.Dir, child.Env, child.Stdin = or(os.Getenv("SWIM_WORKSPACE"), root), env, os.Stdin
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
				terminate(child, sigs)
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
// steps it started end too; otherwise the program alone. The wrapper is in
// that group: it ignores SIGTERM while it sends it, so its own copy is
// discarded, then catches it on sigs again. Caught, the copy could arrive
// after the program had ended and be taken by the handlers that run next
// for an operator's SIGTERM, stopping them before they began.
func terminate(child *exec.Cmd, sigs chan os.Signal) {
	if pg, err := syscall.Getpgid(0); err == nil && pg == os.Getpid() {
		signal.Ignore(syscall.SIGTERM)
		syscall.Kill(-pg, syscall.SIGTERM)
		signal.Notify(sigs, syscall.SIGTERM)
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
