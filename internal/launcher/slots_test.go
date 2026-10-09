package launcher

import (
	"sync"
	"testing"
	"time"
)

func TestChainBelow(t *testing.T) {
	// 1 -> 2 -> 4, 1 -> 3, 5 alone
	deps := map[int][]Dep{2: {{Lane: 1}}, 3: {{Lane: 1}}, 4: {{Lane: 2}}}
	got := chainBelow([]int{1, 2, 3, 4, 5}, deps)
	want := map[int]int{1: 3, 2: 2, 3: 1, 4: 1, 5: 1}
	for n, w := range want {
		if got[n] != w {
			t.Errorf("chain[%d] = %d, want %d", n, got[n], w)
		}
	}
}

func TestSlotsCapAndPriority(t *testing.T) {
	// One slot; lanes 5 (chain 1), 3 (chain 4) and 4 (chain 4) queue behind
	// lane 1. The free slot goes to 3 (longest chain, lower number), then 4, then 5.
	s := newSlots(1, map[int]int{1: 1, 3: 4, 4: 4, 5: 1})
	if !s.acquire(1, nil) {
		t.Fatal("first acquire")
	}
	var mu sync.Mutex
	var order []int
	var wg sync.WaitGroup
	queued := make(chan int, 3)
	for _, n := range []int{5, 4, 3} {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if s.acquire(n, func(int) { queued <- n }) {
				mu.Lock()
				order = append(order, n)
				mu.Unlock()
				time.Sleep(10 * time.Millisecond)
				s.release()
			}
		}(n)
	}
	for i := 0; i < 3; i++ { // all three are waiting before the slot frees
		<-queued
	}
	s.release()
	wg.Wait()
	if len(order) != 3 || order[0] != 3 || order[1] != 4 || order[2] != 5 {
		t.Fatalf("order = %v, want [3 4 5]", order)
	}
}

func TestSlotsUnlimitedAndClose(t *testing.T) {
	s := newSlots(0, nil)
	for n := 1; n <= 50; n++ {
		if !s.acquire(n, nil) {
			t.Fatal("unlimited acquire failed")
		}
	}
	c := newSlots(1, map[int]int{})
	c.acquire(1, nil)
	done := make(chan bool)
	go func() { done <- c.acquire(2, nil) }()
	time.Sleep(20 * time.Millisecond)
	c.close()
	if <-done {
		t.Fatal("a waiter got a slot after close")
	}
}
