package cli

import (
	"fmt"
	"syscall"

	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/status"
)

// cmdInterrupt asks one running lane to stop at its next step boundary,
// as the first Ctrl-C does for every lane: its current step finishes, no
// other step starts, and the round ends interrupted.
func cmdInterrupt(args []string) error {
	rest, err := flags{}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return usagef("usage: swim interrupt N|JOB")
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	n, err := laneRef(root, cfg, rest[0], true)
	if err != nil {
		return err
	}
	pid, ok := lane.Running(root, n)
	if !ok {
		return fmt.Errorf("swim %d isn't running", n)
	}
	run, job, step := "", "", ""
	if st, _ := status.Load(root); st != nil {
		if l := st.Get(n); l != nil {
			run, job, step = l.Run, l.Job, l.CurrentStep
		}
	}
	if err := lane.RequestStop(root, n, run); err != nil {
		return err
	}
	// SIGUSR2 to the lane's bash only: the step command never sees it.
	if err := syscall.Kill(pid, syscall.SIGUSR2); err != nil {
		return fmt.Errorf("signal swim %d (pid %d): %w", n, pid, err)
	}
	history.Log(root, history.Entry{Event: history.StopReq, Lane: n, Job: job, Run: run, Detail: "stop requested after: " + step})
	if step == "" {
		step = "the current step"
	}
	fmt.Printf("swim %d: stopping after %s; no further step will start\n", n, step)
	return nil
}
