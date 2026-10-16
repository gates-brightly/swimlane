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
	// wait blocks until lane d ends (with setup, until an Always lane's
	// setup part is done) and returns how it ended (nil: it didn't finish).
	wait func(d int, setup bool) *Outcome
	// outside says why a dependency outside the run blocks the lane ("":
	// it doesn't); cells, why a dependency on cells of a lane does.
	outside func(Dep) string
	cells   func(Dep) string
	// waiting is told what the lane still waits for, before each wait.
	waiting func(remaining []int)
}

// await waits for deps in order. It returns why the lane is skipped (""
// if it runs), and for a finally lane how each lane it waited for ended,
// and their order.
func (w depWaiter) await(deps []Dep) (skip string, saw map[int]*Outcome, finOrder []int) {
	// Dependencies outside this run must already be satisfied; those
	// inside it are waited for.
	var waitOn []int
	kind := map[int]Dep{}
	saw = map[int]*Outcome{}
	for _, d := range deps {
		if d.Cell == "" {
			d.Cell = kind[d.Lane].Cell // a cell named by an earlier entry stays
		}
		kind[d.Lane] = d
		if d.Finally {
			finOrder = append(finOrder, d.Lane)
		}
		switch {
		case w.selected[d.Lane]:
			waitOn = append(waitOn, d.Lane)
		case d.Finally:
			// Not in this run: it has ended.
		default:
			if why := w.outside(d); why != "" {
				return why, nil, nil
			}
		}
	}
	for len(waitOn) > 0 {
		if w.waiting != nil {
			w.waiting(append([]int(nil), waitOn...))
		}
		d := waitOn[0]
		k := kind[d]
		dep := w.wait(d, k.Setup)
		if why := depVerdict(k, dep, w.cells); why != "" {
			return why, nil, nil
		}
		if k.Finally {
			saw[d] = dep // ended, however: a finally lane runs on
		}
		waitOn = waitOn[1:]
	}
	return "", saw, finOrder
}

// depVerdict decides, once dependency d of a lane has ended as dep,
// whether the lane still runs: "" if it does, else why it's skipped.
//   - an Always lane it only waits for (Setup): its result is for the lanes
//     that name it;
//   - a finally dependency: it ended, however;
//   - cells of a matrix or jobs lane: those cells must have passed,
//     whatever the others did (cells says, from their statuses);
//   - otherwise the lane must have passed.
func depVerdict(d Dep, dep *Outcome, cells func(Dep) string) string {
	switch {
	case d.Setup, d.Finally:
		return ""
	case d.Cell != "" && dep != nil && dep.Cells != nil:
		return cells(Dep{Lane: d.Lane, Cell: d.Cell})
	case dep == nil:
		return fmt.Sprintf("swim %d did not finish", d.Lane)
	case dep.State != status.Passed:
		return fmt.Sprintf("swim %d %s", d.Lane, dep.State)
	}
	return ""
}
