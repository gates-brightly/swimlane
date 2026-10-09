package launcher

import (
	"sort"
	"sync"
)

// lockTable hands out named resource locks (# Locks:) between the lanes of
// one run. A lane takes all of its locks at once or none, so lanes holding
// one lock each can't deadlock waiting for each other's. Waiters are served
// in the order they became ready: a lane may take its locks only when they
// are all free and no earlier waiter wants any of them, so a lane needing
// two locks doesn't starve behind a stream of lanes needing one.
type lockTable struct {
	mu     sync.Mutex
	cond   *sync.Cond
	held   map[string]int // lock -> lane holding it
	queue  []lockWaiter   // waiting lanes, oldest first
	closed bool
}

type lockWaiter struct {
	lane  int
	names []string
}

func newLockTable() *lockTable {
	t := &lockTable{held: map[string]int{}}
	t.cond = sync.NewCond(&t.mu)
	return t
}

// acquire takes every lock in names for lane, waiting as needed. blocked is
// called with a lock that's in the way and the lane holding it (0 when an
// earlier waiter, not a holder, is in the way) whenever that changes. It
// returns false if the run was interrupted first.
func (t *lockTable) acquire(lane int, names []string, blocked func(name string, holder int)) bool {
	names = append([]string(nil), names...)
	sort.Strings(names)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.queue = append(t.queue, lockWaiter{lane, names})
	lastName, lastHolder := "", -1
	for {
		if t.closed {
			t.remove(lane)
			return false
		}
		name, holder := t.obstacle(lane, names)
		if name == "" {
			for _, n := range names {
				t.held[n] = lane
			}
			t.remove(lane)
			t.cond.Broadcast()
			return true
		}
		if blocked != nil && (name != lastName || holder != lastHolder) {
			lastName, lastHolder = name, holder
			t.mu.Unlock() // the callback does I/O
			blocked(name, holder)
			t.mu.Lock()
			continue
		}
		t.cond.Wait()
	}
}

// obstacle returns the first lock keeping lane from proceeding: one held by
// another lane, or one wanted by a waiter ahead of it in the queue.
func (t *lockTable) obstacle(lane int, names []string) (string, int) {
	for _, n := range names {
		if h, ok := t.held[n]; ok && h != lane {
			return n, h
		}
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	for _, w := range t.queue {
		if w.lane == lane {
			break
		}
		for _, n := range w.names {
			if want[n] {
				return n, 0
			}
		}
	}
	return "", 0
}

func (t *lockTable) remove(lane int) {
	for i, w := range t.queue {
		if w.lane == lane {
			t.queue = append(t.queue[:i], t.queue[i+1:]...)
			return
		}
	}
}

// release frees lane's locks.
func (t *lockTable) release(lane int, names []string) {
	t.mu.Lock()
	for _, n := range names {
		if t.held[n] == lane {
			delete(t.held, n)
		}
	}
	t.mu.Unlock()
	t.cond.Broadcast()
}

// close wakes every waiter and makes them give up (Ctrl-C).
func (t *lockTable) close() {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
	t.cond.Broadcast()
}
