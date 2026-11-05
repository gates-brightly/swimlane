package cli

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gates-brightly/swimlane/internal/chars"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
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
//
// They read the lane from SWIM_LANE, which swim's wrapper (swim _wrap)
// exports. Ending the round (a failed gate, a bad stage, a refused step)
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
		case a == "--no-retry-timeout" || a == "--expect-fail" || strings.HasPrefix(a, "--expect-fail="):
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

// laneGate and laneSnapshot are swim gate / swim snapshot.
func laneGate(args []string) error     { return laneStep("gate", args) }
func laneSnapshot(args []string) error { return laneStep("snapshot", args) }
