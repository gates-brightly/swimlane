package launcher

import (
	"sort"
	"sync"
)

// slots caps how many lanes run at once (max_parallel / --parallel). A lane
// asks for a slot once its dependencies have passed; when none is free it
// waits, and free slots go to the waiting lane with the longest chain of
// lanes still below it (so the critical path keeps moving), then the lowest
// lane number. A cap of 0 means unlimited.
type slots struct {
	mu      sync.Mutex
	cond    *sync.Cond
	cap     int
	running int
	waiting map[int]bool
	chain   map[int]int // lanes on the longest path below each lane, itself included
	closed  bool        // interrupted: waiters give up
}

func newSlots(cap int, chain map[int]int) *slots {
	s := &slots{cap: cap, waiting: map[int]bool{}, chain: chain}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// order lists the waiting lanes, best first.
func (s *slots) order() []int {
	var ws []int
	for n := range s.waiting {
		ws = append(ws, n)
	}
	sort.Slice(ws, func(a, b int) bool {
		if s.chain[ws[a]] != s.chain[ws[b]] {
			return s.chain[ws[a]] > s.chain[ws[b]]
		}
		return ws[a] < ws[b]
	})
	return ws
}

// acquire takes a slot for lane n, waiting if none is free. queued is called
// (without the lock held) with the number of lanes ahead whenever that
// changes while n waits. It returns false if the run was interrupted first.
func (s *slots) acquire(n int, queued func(ahead int)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	if s.cap <= 0 {
		s.running++
		return true
	}
	s.waiting[n] = true
	last := -1
	for {
		if s.closed {
			delete(s.waiting, n)
			return false
		}
		ord := s.order()
		ahead := 0
		for i, w := range ord {
			if w == n {
				ahead = i
				break
			}
		}
		if s.running < s.cap && ahead == 0 {
			delete(s.waiting, n)
			s.running++
			s.cond.Broadcast() // the next waiter may be first now
			return true
		}
		if ahead != last && queued != nil {
			// Report outside the lock (the callback does I/O), then look
			// again: the state may have changed meanwhile.
			last = ahead
			s.mu.Unlock()
			queued(ahead)
			s.mu.Lock()
			continue
		}
		s.cond.Wait()
	}
}

// release frees a slot taken by acquire.
func (s *slots) release() {
	s.mu.Lock()
	s.running--
	s.mu.Unlock()
	s.cond.Broadcast()
}

// close wakes every waiter and makes them give up (Ctrl-C).
func (s *slots) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cond.Broadcast()
}

// chainBelow returns, for each selected lane, how many lanes lie on the
// longest dependency path from it down through the selection (itself
// included).
func chainBelow(sel []int, deps map[int][]Dep) map[int]int {
	kids := map[int][]int{}
	in := map[int]bool{}
	for _, n := range sel {
		in[n] = true
	}
	for _, n := range sel {
		for _, d := range deps[n] {
			if in[d.Lane] {
				kids[d.Lane] = append(kids[d.Lane], n)
			}
		}
	}
	memo := map[int]int{}
	var depth func(n int) int
	depth = func(n int) int {
		if v, ok := memo[n]; ok {
			return v
		}
		memo[n] = 1 // cycles were rejected by ResolveDeps
		best := 0
		for _, k := range kids[n] {
			best = max(best, depth(k))
		}
		memo[n] = 1 + best
		return memo[n]
	}
	out := map[int]int{}
	for _, n := range sel {
		out[n] = depth(n)
	}
	return out
}
