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
	toks := make([]string, 4)
	for i := range toks {
		toks[i] = s.Register(Caller{Lane: i + 1, Guards: []string{"YES", "NO"}})
	}
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
			results[i], _ = Ask(s.Path(), Request{Lane: i + 1, Flag: flag, Desc: "d", Token: toks[i]}, nil, time.Time{})
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
	tok := s.Register(Caller{Lane: 1, Guards: []string{"F"}})
	go func() { <-asked; s.CancelAll() }()
	r, err := Ask(s.Path(), Request{Lane: 1, Flag: "F", Token: tok}, nil, time.Time{})
	if err != nil || r.Approved || r.Why != "the run is stopping" {
		t.Fatalf("%+v %v", r, err)
	}
	// Later requests are declined without asking.
	r, _ = Ask(s.Path(), Request{Lane: 2, Flag: "F", Token: tok}, nil, time.Time{})
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
	tok := s.Register(Caller{Lane: 1, Guards: []string{"F"}})
	var stop atomic.Bool
	go func() { time.Sleep(100 * time.Millisecond); stop.Store(true) }()
	r, err := Ask(s.Path(), Request{Lane: 1, Flag: "F", Token: tok}, stop.Load, time.Time{})
	if err != ErrNoAnswer || r.Why != "the lane is stopping" {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = Ask(s.Path(), Request{Lane: 1, Flag: "F", Token: tok}, nil, time.Now().Add(150*time.Millisecond))
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
	for _, want := range []string{"the lane says: drop prodlist files\n", "  plan: destroy 1\n", "  plan: add 0\n"} {
		if !strings.Contains(q, want) {
			t.Errorf("question lacks %q: %q", want, q)
		}
	}
}

// The launcher's facts, not the request's, say who asks: a request names
// another lane and job, and the question shows the registered ones; an
// undeclared flag and a request without a token are refused unasked.
func TestServerUsesRegisteredLane(t *testing.T) {
	var asked []Request
	var mu sync.Mutex
	s, err := Listen(func(req Request, cancel <-chan struct{}) Response {
		mu.Lock()
		asked = append(asked, req)
		mu.Unlock()
		return Response{Approved: true, By: "op"}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var refused []string
	s.Refused = func(c Caller, req Request, why string) { refused = append(refused, why) }
	tok := s.Register(Caller{Lane: 3, Job: "j-real", Manifest: "abcdef0123456789", Guards: []string{"DROP"}})
	r, err := Ask(s.Path(), Request{Lane: 5, Job: "j-fake", Flag: "DROP", Desc: "harmless \x1b[2Jcleanup", Token: tok}, nil, time.Time{})
	if err != nil || !r.Approved || r.Lane != 3 || r.Job != "j-real" || r.ID == "" {
		t.Fatalf("declared guard: %+v %v", r, err)
	}
	q := strings.Join(Question(asked[0]), "\n")
	for _, want := range []string{"swim 3 requires human approval of guard DROP", "job j-real · manifest abcdef012345", "the lane says: harmless cleanup (sent as swim 5)"} {
		if !strings.Contains(q, want) {
			t.Errorf("question lacks %q:\n%s", want, q)
		}
	}
	if strings.Contains(q, "j-fake") || strings.Contains(q, "\x1b") {
		t.Errorf("question shows the lane's claims as facts:\n%s", q)
	}
	r, _ = Ask(s.Path(), Request{Lane: 3, Flag: "WIPE", Desc: "d", Token: tok}, nil, time.Time{})
	if r.Approved || !strings.Contains(r.Why, "guard WIPE isn't declared") {
		t.Errorf("undeclared flag: %+v", r)
	}
	r, _ = Ask(s.Path(), Request{Lane: 3, Flag: "DROP", Desc: "d"}, nil, time.Time{})
	if r.Approved || !strings.Contains(r.Why, "token") {
		t.Errorf("no token: %+v", r)
	}
	if len(asked) != 1 || len(refused) != 2 {
		t.Errorf("asked %d, refused %v", len(asked), refused)
	}
}

// An approval is consumed once, by the lane process it was given to, for
// its guard.
func TestConsumeOnce(t *testing.T) {
	s, err := Listen(func(req Request, cancel <-chan struct{}) Response { return Response{Approved: true} })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := s.Register(Caller{Lane: 1, Guards: []string{"F", "G"}})
	b := s.Register(Caller{Lane: 2, Guards: []string{"F"}})
	r, _ := Ask(s.Path(), Request{Lane: 1, Flag: "F", Token: a}, nil, time.Time{})
	if err := Consume(s.Path(), b, r.ID, "F"); err == nil || !strings.Contains(err.Error(), "another lane") {
		t.Errorf("another lane consumed it: %v", err)
	}
	if err := Consume(s.Path(), a, r.ID, "G"); err == nil {
		t.Error("consumed for another guard")
	}
	if err := Consume(s.Path(), a, r.ID, "F"); err != nil {
		t.Errorf("first use: %v", err)
	}
	if err := Consume(s.Path(), a, r.ID, "F"); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Errorf("second use: %v", err)
	}
}
