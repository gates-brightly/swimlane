package e2e

import (
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"testing/quick"

	"github.com/gates-brightly/swimlane/internal/status"
)

// propDAG is a random lane graph for the scheduler property test: up to
// six lanes, each naming earlier lanes in After:, taking locks from a pool
// of two, some failing.
type propDAG struct {
	Lanes []propLane
}

type propLane struct {
	After []int    // earlier lanes
	Locks []string // from {a, b}
	Fail  bool
	Work  int // milliseconds of sleep in its step
}

func (propDAG) Generate(rnd *rand.Rand, _ int) reflect.Value {
	n := 2 + rnd.Intn(5)
	d := propDAG{Lanes: make([]propLane, n)}
	for i := range d.Lanes {
		l := &d.Lanes[i]
		for j := 0; j < i; j++ {
			if rnd.Intn(3) == 0 {
				l.After = append(l.After, j+1)
			}
		}
		for _, lk := range []string{"a", "b"} {
			if rnd.Intn(3) == 0 {
				l.Locks = append(l.Locks, lk)
			}
		}
		l.Fail = rnd.Intn(5) == 0
		l.Work = 10 + rnd.Intn(150)
	}
	return reflect.ValueOf(d)
}

func (d propDAG) String() string {
	var b strings.Builder
	for i, l := range d.Lanes {
		fmt.Fprintf(&b, "swim %d after %v locks %v fail %v work %dms; ", i+1, l.After, l.Locks, l.Fail, l.Work)
	}
	return b.String()
}

// want is what each lane must end as: failed if it fails, skipped if a
// lane it waits for didn't pass, passed otherwise.
func (d propDAG) want() []string {
	out := make([]string, len(d.Lanes))
	for i, l := range d.Lanes {
		out[i] = status.Passed
		if l.Fail {
			out[i] = status.Failed
		}
		for _, p := range l.After {
			if out[p-1] != status.Passed {
				out[i] = status.Skipped
			}
		}
	}
	return out
}

// TestSchedulerProperties runs random lane graphs through swim run and
// checks, from timestamps the lanes write themselves: a lane never starts
// before every lane in its After: has finished; lanes sharing a lock never
// overlap; a failing lane is never reported passed, its dependents are
// skipped, and every other lane passes. SWIM_QUICK_COUNT sets how many
// graphs (default 15).
func TestSchedulerProperties(t *testing.T) {
	count := 15
	if n, err := strconv.Atoi(os.Getenv("SWIM_QUICK_COUNT")); err == nil && n > 0 {
		count = n
	}
	check := func(d propDAG) bool {
		ok := true
		t.Run(fmt.Sprintf("%d lanes", len(d.Lanes)), func(t *testing.T) {
			r := newRepo(t, "")
			ledger, stampLine := withLedger(t, r)
			var lanes []string
			for i, l := range d.Lanes {
				n := i + 1
				body := stampLine(n, "start") + fmt.Sprintf("\nrun \"work\" sleep %.3f\n", float64(l.Work)/1000)
				if l.Fail {
					body += `run "fails" false` + "\n"
				}
				body += stampLine(n, "end")
				r.script(n, "prop", body)
				if len(l.After) > 0 {
					withHeader(t, r, n, "# After: "+joinInts(l.After, ", "))
				}
				if len(l.Locks) > 0 {
					withLocks(t, r, n, strings.Join(l.Locks, ", "))
				}
				lanes = append(lanes, strconv.Itoa(n))
			}
			out, _ := r.swim(append([]string{"run"}, lanes...)...)
			want := d.want()
			st := r.status()
			ivs := map[int]interval{}
			for _, iv := range intervals(t, readLedger(t, ledger)) {
				ivs[iv.lane] = iv
			}
			for i := range d.Lanes {
				n := i + 1
				if got := st.Get(n).State; got != want[i] {
					t.Errorf("swim %d: %s, want %s", n, got, want[i])
				}
				iv, ran := ivs[n]
				if ran != (want[i] != status.Skipped) {
					t.Errorf("swim %d: ran %v, but it should end %s", n, ran, want[i])
				}
				for _, p := range d.Lanes[i].After {
					if pv, ok := ivs[p]; ran && ok && iv.start < pv.end {
						t.Errorf("swim %d started %.3fs before swim %d (its After:) finished", n, float64(pv.end-iv.start)/1e9, p)
					}
				}
			}
			var all []interval
			for _, iv := range ivs {
				all = append(all, iv)
			}
			noOverlap(t, all, func(a, b int) bool { return sharesLock(d.Lanes[a-1].Locks, d.Lanes[b-1].Locks) })
			if t.Failed() {
				t.Logf("graph: %s\n%s", d, out)
				ok = false
			}
		})
		return ok
	}
	if err := quick.Check(check, &quick.Config{MaxCount: count}); err != nil {
		t.Error(err)
	}
}

func sharesLock(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

func joinInts(ns []int, sep string) string {
	s := make([]string, len(ns))
	for i, n := range ns {
		s[i] = strconv.Itoa(n)
	}
	return strings.Join(s, sep)
}
