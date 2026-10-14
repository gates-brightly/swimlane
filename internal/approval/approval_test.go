package approval

import (
	"fmt"
	"strings"
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

// The question shows the evidence it approves, cut to 20 lines.
func TestQuestionShowsEvidence(t *testing.T) {
	var lines []string
	for i := 0; i < 25; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	q := strings.Join(Question(Request{Lane: 3, Flag: "F", Desc: "apply", Evidence: strings.Join(lines, "\n")}), "\n")
	for _, want := range []string{"evidence (what it will do):", "  line 0", "  line 19", "... 5 more lines", "Y/N [N]"} {
		if !strings.Contains(q, want) {
			t.Errorf("question lacks %q:\n%s", want, q)
		}
	}
	if strings.Contains(q, "line 20") {
		t.Error("more than 20 evidence lines")
	}
}

// A lane can't rewrite its own question: escape sequences, \r, \b and
// bidi overrides in the description and evidence are removed.
func TestQuestionIsSafe(t *testing.T) {
	q := strings.Join(Question(Request{Lane: 3, Flag: "F", Desc: "drop prod\x1b[2K\rlist files\b‮",
		Evidence: "plan: destroy 1\x1b[1A\x1b[2K\r\nplan: ‮add 0\r\n"}), "\n")
	for _, bad := range []string{"\x1b", "\r", "\b", "‮"} {
		if strings.Contains(q, bad) {
			t.Errorf("question carries %q: %q", bad, q)
		}
	}
	for _, want := range []string{"approval of task: drop prodlist files\n", "  plan: destroy 1\n", "  plan: add 0\n"} {
		if !strings.Contains(q, want) {
			t.Errorf("question lacks %q: %q", want, q)
		}
	}
}
