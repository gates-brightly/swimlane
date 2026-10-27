package launcher

import (
	"os"
	"strconv"
	"testing"
	"testing/quick"

	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/status"
)

// Plan and run agree, over random graphs, with no repo: the lanes swim
// plan predicts are skipped (decidePlan, from the facts alone) are exactly
// the lanes the scheduler skips (runSched, its own parts), when the lanes
// plan says are refused before they start are the ones that fail. Every
// lane gets one action; a refused lane's is skip; an Always lane that runs
// is always. SWIM_QUICK_COUNT sets how many graphs (default 500).
func TestPlanAgreesWithRun(t *testing.T) {
	count := 500
	if testing.Short() {
		count = 50 // the short batch (make test); the long batch checks 500
	}
	if n, err := strconv.Atoi(os.Getenv("SWIM_QUICK_COUNT")); err == nil && n > 0 {
		count = n
	}
	check := func(d schedDAG) bool {
		f := planFacts{lanes: len(d.Lanes), scripts: map[int]lane.Info{}, status: map[int]*status.Lane{},
			deps: map[int][]Dep{}, blocked: map[int]string{}, outside: map[Dep]string{}}
		for i, l := range d.Lanes {
			n := i + 1
			f.sel = append(f.sel, n)
			f.scripts[n] = lane.Info{Exists: true, Round: "r"}
			f.deps[n] = d.deps(n)
			if l.Fail {
				f.blocked[n] = "blocked: refused before it starts"
			}
		}
		pl := decidePlan(f)
		states, _ := runSched(d)
		ok := true
		fail := func(format string, a ...any) {
			t.Errorf(format+"\ngraph: %s", append(a, d)...)
			ok = false
		}
		total := 0
		for _, c := range pl.counts {
			total += c
		}
		if total != len(d.Lanes) {
			fail("%d actions for %d lanes", total, len(d.Lanes))
		}
		for i := range d.Lanes {
			n := i + 1
			refused := f.blocked[n] != ""
			// runSched fails a refused lane only once its waits are over
			// (swim refuses it as it stages): it ends failed, or skipped
			// when a lane it needs didn't pass; never passed.
			if !refused {
				predicted := pl.skip[n] != ""
				if skipped := states[i] == status.Skipped; predicted != skipped {
					fail("swim %d: plan predicts skipped %v (%q), the run skipped it %v", n, predicted, pl.skip[n], skipped)
				}
			}
			switch {
			case refused && (pl.actions[n] != actSkip || states[i] == status.Passed):
				fail("swim %d is refused: plan %s, run %s", n, pl.actions[n].name(), states[i])
			case !refused && pl.skip[n] == "" && pl.actions[n] != actRun:
				fail("swim %d runs, never ran before: plan %s", n, pl.actions[n].name())
			}
		}
		return ok
	}
	if err := quick.Check(check, &quick.Config{MaxCount: count}); err != nil {
		t.Error(err)
	}
}

// From the facts alone: a lane waiting on a lane outside the run that
// hasn't passed is skipped; a lane that last passed this job is a rerun,
// one that last ran it otherwise a retry, and a new job a run.
func TestDecidePlanActions(t *testing.T) {
	r := lane.Info{Exists: true, Round: "r", Job: "j"}
	f := planFacts{lanes: 5, sel: []int{1, 2, 3, 4},
		scripts: map[int]lane.Info{1: r, 2: r, 3: r, 4: r, 5: r},
		status: map[int]*status.Lane{
			3: {Job: "j", Round: "r", State: status.Passed},
			4: {Job: "j", Round: "r", State: status.Failed},
		},
		deps:    map[int][]Dep{2: {{Lane: 5}}},
		blocked: map[int]string{},
		outside: map[Dep]string{{Lane: 5}: "swim 5's pending round has not passed; run it too: swim run 5 ..."},
	}
	pl := decidePlan(f)
	for n, want := range map[int]planAction{1: actRun, 2: actSkip, 3: actRerun, 4: actRetry} {
		if got := pl.actions[n]; got != want {
			t.Errorf("swim %d: %s, want %s (skip %q)", n, got.name(), want.name(), pl.skip[n])
		}
	}
}
