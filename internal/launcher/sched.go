package launcher

import (
	"fmt"

	"github.com/gates-brightly/swimlane/internal/status"
)

// depWaiter is how a lane waits for its dependencies in a run, with what
// it needs from the run passed in, so the decisions are the same whether
// swim run makes them or a test does: which dependencies it waits for,
// in what order, and, as each ends, whether the lane still runs.
type depWaiter struct {
	// selected are the lanes in this run.
	selected map[int]bool
	// wait blocks until lane d ends and returns how it ended (nil: it
	// didn't finish).
	wait func(d int) *Outcome
	// outside says why a dependency outside the run blocks the lane ("":
	// it doesn't).
	outside func(Dep) string
	// waiting is told what the lane still waits for, before each wait.
	waiting func(remaining []int)
}

// await waits for deps in order. It returns why the lane is skipped (""
// if it runs).
func (w depWaiter) await(deps []Dep) string {
	// Dependencies outside this run must already be satisfied; those
	// inside it are waited for.
	var waitOn []int
	for _, d := range deps {
		if w.selected[d.Lane] {
			waitOn = append(waitOn, d.Lane)
		} else if why := w.outside(d); why != "" {
			return why
		}
	}
	for len(waitOn) > 0 {
		if w.waiting != nil {
			w.waiting(append([]int(nil), waitOn...))
		}
		d := waitOn[0]
		if why := depVerdict(Dep{Lane: d}, w.wait(d)); why != "" {
			return why
		}
		waitOn = waitOn[1:]
	}
	return ""
}

// depVerdict decides, once dependency d of a lane has ended as dep,
// whether the lane still runs: "" if it does (d passed), else why it's
// skipped.
func depVerdict(d Dep, dep *Outcome) string {
	switch {
	case dep == nil:
		return fmt.Sprintf("swim %d did not finish", d.Lane)
	case dep.State != status.Passed:
		return fmt.Sprintf("swim %d %s", d.Lane, dep.State)
	}
	return ""
}
