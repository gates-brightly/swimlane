package launcher

import (
	"sync"
	"testing"
	"time"
)

func TestLockTableAllAtOnceAndFIFO(t *testing.T) {
	lt := newLockTable()
	if !lt.acquire(1, []string{"a"}, nil) {
		t.Fatal("lane 1")
	}
	var mu sync.Mutex
	var order []int
	var wg sync.WaitGroup
	waiting := make(chan int, 3)
	start := func(lane int, names ...string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			first := true
			if lt.acquire(lane, names, func(string, int) {
				if first {
					waiting <- lane
					first = false
				}
			}) {
				mu.Lock()
				order = append(order, lane)
				mu.Unlock()
				time.Sleep(20 * time.Millisecond)
				lt.release(lane, names)
			}
		}()
		<-waiting // queue in a known order
	}
	start(2, "a", "b") // needs a (held) and b
	start(3, "b")      // b is free, but lane 2 (ahead) wants it: must wait
	start(4, "a")
	lt.release(1, []string{"a"})
	wg.Wait()
	// FIFO among conflicting waiters: 2 takes a+b, then 3 (b) and 4 (a).
	if len(order) != 3 || order[0] != 2 {
		t.Fatalf("order = %v, want lane 2 first (it needs two locks and was first in line)", order)
	}
}

func TestLockTableIndependentLocksDontWait(t *testing.T) {
	lt := newLockTable()
	lt.acquire(1, []string{"a"}, nil)
	done := make(chan bool)
	go func() { done <- lt.acquire(2, []string{"b"}, nil) }()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("lane 2 refused")
		}
	case <-time.After(time.Second):
		t.Fatal("a lane waited for a lock nobody holds")
	}
}

func TestLockTableClose(t *testing.T) {
	lt := newLockTable()
	lt.acquire(1, []string{"a"}, nil)
	done := make(chan bool)
	go func() { done <- lt.acquire(2, []string{"a"}, nil) }()
	time.Sleep(20 * time.Millisecond)
	lt.close()
	if <-done {
		t.Fatal("got a lock after close")
	}
}
