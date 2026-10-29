package launcher

import (
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/quick"

	"github.com/gates-brightly/swimlane/internal/status"
)

// schedDAG is a random lane graph: up to eight lanes, each naming earlier
// lanes in After:, taking locks from a pool of two, some
// failing.
type schedDAG struct {
	Lanes []schedLane
}

type schedLane struct {
	After []int // earlier lanes it needs to pass
	Locks []string
	Fail  bool
}

func (schedDAG) Generate(rnd *rand.Rand, _ int) reflect.Value {
	d := schedDAG{Lanes: make([]schedLane, 2+rnd.Intn(7))}
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
		l.Fail = rnd.Intn(4) == 0
	}
	return reflect.ValueOf(d)
}

func (d schedDAG) String() string {
	var b strings.Builder
	for i, l := range d.Lanes {
		fmt.Fprintf(&b, "swim %d after %v locks %v fail %v; ", i+1, l.After, l.Locks, l.Fail)
	}
	return b.String()
}

// deps are lane n's dependencies, as ResolveDeps gives them.
func (d schedDAG) deps(n int) []Dep {
	var out []Dep
	l := d.Lanes[n-1]
	for _, p := range l.After {
		out = append(out, Dep{Lane: p})
	}
	return out
}

// want is what each lane must end as: skipped if a lane in its After:
// didn't pass, else failed if it fails, else passed.
func (d schedDAG) want() []string {
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

// span is when a lane ran, on a logical clock: every start and end takes
// the next tick, so "before" is exact, whatever the machine's load.
type span struct{ start, end int64 }

// runSched runs d through the scheduler's own parts, in memory: each lane
// waits with depWaiter.await (as runLane does), takes its locks from a
// lockTable, runs, and ends. No files, no processes, no wall clock.
func runSched(d schedDAG) (states []string, spans map[int]span) {
	n := len(d.Lanes)
	selected := map[int]bool{}
	done := map[int]chan struct{}{}
	for i := 1; i <= n; i++ {
		selected[i] = true
		done[i] = make(chan struct{})
	}
	var mu sync.Mutex
	outcomes := map[int]*Outcome{}
	spans = map[int]span{}
	var clock atomic.Int64
	lt := newLockTable()
	var wg sync.WaitGroup
	for i := 1; i <= n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			oc := &Outcome{N: i, State: status.Passed}
			defer func() {
				mu.Lock()
				outcomes[i] = oc
				mu.Unlock()
				close(done[i])
			}()
			w := depWaiter{
				selected: selected,
				wait: func(dep int) *Outcome {
					<-done[dep]
					mu.Lock()
					defer mu.Unlock()
					return outcomes[dep]
				},
				outside: func(Dep) string { return "outside the run" },
			}
			if why := w.await(d.deps(i)); why != "" {
				oc.State, oc.Reason = status.Skipped, why
				return
			}
			locks := d.Lanes[i-1].Locks
			lt.acquire(i, locks, nil)
			sp := span{start: clock.Add(1)}
			for k := rand.Intn(50); k > 0; k-- {
				runtime.Gosched() // let others interleave
			}
			sp.end = clock.Add(1)
			lt.release(i, locks)
			mu.Lock()
			spans[i] = sp
			mu.Unlock()
			if d.Lanes[i-1].Fail {
				oc.State = status.Failed
			}
		}(i)
	}
	wg.Wait()
	for i := 1; i <= n; i++ {
		states = append(states, outcomes[i].State)
	}
	return states, spans
}

// The scheduler's properties, over random graphs, with no repo, process
// or wall clock: a lane never starts before every lane in its After: has
// ended; lanes sharing a lock never overlap; a failing lane is never
// reported passed, the lanes after it are skipped, and every other lane
// passes.
// SWIM_QUICK_COUNT sets how many graphs (default 500).
func TestSchedulerProperties(t *testing.T) {
	count := 500
	if testing.Short() {
		count = 50 // the short batch (make test); the long batch checks 500
	}
	if n, err := strconv.Atoi(os.Getenv("SWIM_QUICK_COUNT")); err == nil && n > 0 {
		count = n
	}
	check := func(d schedDAG) bool {
		states, spans := runSched(d)
		want := d.want()
		ok := true
		fail := func(format string, a ...any) {
			t.Errorf(format+"\ngraph: %s", append(a, d)...)
			ok = false
		}
		for i, l := range d.Lanes {
			n := i + 1
			if states[i] != want[i] {
				fail("swim %d: %s, want %s", n, states[i], want[i])
			}
			sp, ran := spans[n]
			if ran != (want[i] != status.Skipped) {
				fail("swim %d: ran %v, but it should end %s", n, ran, want[i])
			}
			for _, p := range l.After {
				if pv, ok := spans[p]; ran && ok && sp.start < pv.end {
					fail("swim %d started before swim %d, which it waits for, ended", n, p)
				}
			}
			for j := i + 1; j < len(d.Lanes); j++ {
				a, aok := spans[n]
				b, bok := spans[j+1]
				if aok && bok && sharesLock(l.Locks, d.Lanes[j].Locks) && a.start < b.end && b.start < a.end {
					fail("swim %d and swim %d share a lock and overlapped", n, j+1)
				}
			}
		}
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

// depVerdict's rules, one by one.
func TestDepVerdict(t *testing.T) {
	passed, failed := &Outcome{State: status.Passed}, &Outcome{State: status.Failed}
	for _, c := range []struct {
		dep  Dep
		out  *Outcome
		want string
	}{
		{Dep{Lane: 2}, passed, ""},
		{Dep{Lane: 2}, failed, "swim 2 failed"},
		{Dep{Lane: 2}, nil, "swim 2 did not finish"},
	} {
		if got := depVerdict(c.dep, c.out); got != c.want {
			t.Errorf("%+v ended %+v: %q, want %q", c.dep, c.out, got, c.want)
		}
	}
}

// A dependency outside the run is checked, never waited for.
func TestAwaitOutside(t *testing.T) {
	waited := false
	w := depWaiter{
		selected: map[int]bool{1: true},
		wait:     func(int) *Outcome { waited = true; return &Outcome{State: status.Passed} },
		outside:  func(d Dep) string { return fmt.Sprintf("swim %d hasn't passed", d.Lane) },
	}
	if why := w.await([]Dep{{Lane: 1}}); why != "" || !waited {
		t.Errorf("in the run: %q waited %v", why, waited)
	}
	waited = false
	if why := w.await([]Dep{{Lane: 9}, {Lane: 1}}); why != "swim 9 hasn't passed" || waited {
		t.Errorf("outside the run: %q waited %v", why, waited)
	}
}

// selectedCycle finds a cycle exactly when the lanes in the run wait on
// each other in one, and names it in order.
func TestSelectedCycleProperty(t *testing.T) {
	check := func(seed int64) bool {
		rnd := rand.New(rand.NewSource(seed))
		n := 2 + rnd.Intn(6)
		deps := map[int][]Dep{}
		for i := 1; i <= n; i++ {
			deps[i] = nil
			for j := 1; j <= n; j++ {
				if j != i && rnd.Intn(4) == 0 {
					deps[i] = append(deps[i], Dep{Lane: j})
				}
			}
			if rnd.Intn(5) == 0 {
				deps[i] = append(deps[i], Dep{Lane: 50 + i}) // outside the run: never part of a cycle
			}
		}
		got := selectedCycle(deps)
		if (got != "") != hasCycle(deps) {
			t.Errorf("deps %v: cycle %q, want one: %v", deps, got, hasCycle(deps))
			return false
		}
		if got != "" {
			// "swim a -> swim b -> ... -> swim a", each step a real edge.
			parts := strings.Split(got, " -> ")
			if parts[0] != parts[len(parts)-1] {
				t.Errorf("cycle %q doesn't close", got)
				return false
			}
			for k := 0; k+1 < len(parts); k++ {
				var a, b int
				fmt.Sscanf(parts[k], "swim %d", &a)
				fmt.Sscanf(parts[k+1], "swim %d", &b)
				edge := false
				for _, d := range deps[a] {
					edge = edge || d.Lane == b
				}
				if !edge {
					t.Errorf("cycle %q: swim %d doesn't wait on swim %d", got, a, b)
					return false
				}
			}
		}
		return true
	}
	if err := quick.Check(check, &quick.Config{MaxCount: 1000}); err != nil {
		t.Error(err)
	}
}

// hasCycle is the plain answer: some lane in the run reaches itself.
func hasCycle(deps map[int][]Dep) bool {
	for start := range deps {
		seen := map[int]bool{}
		stack := []int{start}
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for _, d := range deps[n] {
				if _, in := deps[d.Lane]; !in {
					continue
				}
				if d.Lane == start {
					return true
				}
				if !seen[d.Lane] {
					seen[d.Lane] = true
					stack = append(stack, d.Lane)
				}
			}
		}
	}
	return false
}
