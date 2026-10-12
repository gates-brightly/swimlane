package approval

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRoundTripConcurrent(t *testing.T) {
	var mu sync.Mutex
	active, most := 0, 0
	s, err := Listen(func(req Request, cancel <-chan struct{}) Response {
		mu.Lock()
		active++
		most = max(most, active)
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
		return Response{Approved: req.Flag == "YES", By: "op"}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var wg sync.WaitGroup
	results := make([]Response, 4)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			flag := "NO"
			if i%2 == 0 {
				flag = "YES"
			}
			results[i], _ = Ask(s.Path(), Request{Lane: i + 1, Flag: flag, Desc: "d"}, nil, time.Time{})
		}(i)
	}
	wg.Wait()
	for i, r := range results {
		if r.Approved != (i%2 == 0) || r.By != "op" {
			t.Errorf("request %d: %+v", i, r)
		}
	}
	// Lanes wait at the same time; the asker decides how to queue them
	// (the interactive view shows them all, the line prompt asks in turn).
	if most < 2 {
		t.Errorf("questions from parallel lanes should reach the asker together; at most %d at once", most)
	}
}

func TestCancelDeclines(t *testing.T) {
	asked := make(chan struct{})
	s, err := Listen(func(req Request, cancel <-chan struct{}) Response {
		close(asked)
		<-cancel
		return Response{}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	go func() { <-asked; s.CancelAll() }()
	r, err := Ask(s.Path(), Request{Lane: 1}, nil, time.Time{})
	if err != nil || r.Approved || r.Why != "the run is stopping" {
		t.Fatalf("%+v %v", r, err)
	}
	// Later requests are declined without asking.
	r, _ = Ask(s.Path(), Request{Lane: 2}, nil, time.Time{})
	if r.Approved || r.Why != "the run is stopping" {
		t.Fatalf("after cancel: %+v", r)
	}
}

func TestClientGivesUp(t *testing.T) {
	s, err := Listen(func(req Request, cancel <-chan struct{}) Response {
		<-cancel // the lane hung up
		return Response{}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var stop atomic.Bool
	go func() { time.Sleep(100 * time.Millisecond); stop.Store(true) }()
	r, err := Ask(s.Path(), Request{Lane: 1}, stop.Load, time.Time{})
	if err != ErrNoAnswer || r.Why != "the lane is stopping" {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = Ask(s.Path(), Request{Lane: 1}, nil, time.Now().Add(150*time.Millisecond))
	if err != ErrNoAnswer || r.Why != "the round's Timeout ran out" {
		t.Fatalf("deadline: %+v %v", r, err)
	}
}
