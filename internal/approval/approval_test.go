package approval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/peer"
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
	// An unattended worker's server fails it instead (the round ends
	// BLOCKED); a refusal for anything else is still a plain decline.
	s.FailUndeclared = true
	if r, _ = Ask(s.Path(), Request{Lane: 3, Flag: "WIPE", Desc: "d", Token: tok}, nil, time.Time{}); r.Approved || !r.Fail {
		t.Errorf("undeclared flag on an unattended worker: %+v", r)
	}
	if r, _ = Ask(s.Path(), Request{Lane: 3, Flag: "DROP", Desc: "d"}, nil, time.Time{}); r.Fail {
		t.Errorf("no token fails the round: %+v", r)
	}
	s.FailUndeclared = false
	// A script that names a guard's flag at run time may ask for any.
	dyn := s.Register(Caller{Lane: 4, Guards: []string{"DROP"}, AnyGuard: true})
	if r, _ = Ask(s.Path(), Request{Lane: 4, Flag: "WIPE", Desc: "d", Token: dyn}, nil, time.Time{}); !r.Approved {
		t.Errorf("a run-time flag of a script with dynamic guards: %+v", r)
	}
	r, _ = Ask(s.Path(), Request{Lane: 3, Flag: "DROP", Desc: "d"}, nil, time.Time{})
	if r.Approved || !strings.Contains(r.Why, "token") {
		t.Errorf("no token: %+v", r)
	}
	if len(asked) != 2 || len(refused) != 4 {
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
	if _, err := Consume(s.Path(), b, r.ID, "F"); err == nil || !strings.Contains(err.Error(), "another lane") {
		t.Errorf("another lane consumed it: %v", err)
	}
	if _, err := Consume(s.Path(), a, r.ID, "G"); err == nil {
		t.Error("consumed for another guard")
	}
	if _, err := Consume(s.Path(), a, r.ID, "F"); err != nil {
		t.Errorf("first use: %v", err)
	}
	if _, err := Consume(s.Path(), a, r.ID, "F"); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Errorf("second use: %v", err)
	}
}

// whoami names the lane, cell, job and run a token was registered for;
// an unknown token is refused.
func TestWhoami(t *testing.T) {
	s, err := Listen(func(Request, <-chan struct{}) Response { return Response{} })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tok := s.Register(Caller{Lane: 2, Cell: "orders", Job: "j-1", Run: "r-1"})
	r, err := Whoami(s.Path(), tok)
	if err != nil || r.Lane != 2 || r.Cell != "orders" || r.Job != "j-1" || r.Run != "r-1" {
		t.Fatalf("whoami: %+v %v", r, err)
	}
	if _, err := Whoami(s.Path(), "bogus"); err == nil {
		t.Error("an unknown token was answered")
	}
	s.Revoke(tok)
	if _, err := Whoami(s.Path(), tok); err == nil {
		t.Error("a revoked token was answered")
	}
}

// A token works only from its lane's process tree: here the lane is
// another process, so this test's requests come from outside it.
func TestPeerOutsideTheLane(t *testing.T) {
	if !peer.Supported {
		t.Skip("no peer credentials on this platform")
	}
	asked := false
	s, err := Listen(func(Request, <-chan struct{}) Response { asked = true; return Response{Approved: true} })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var refused []string
	s.Refused = func(c Caller, req Request, why string) { refused = append(refused, why) }
	other := exec.Command("sleep", "30")
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	defer other.Process.Kill()
	tok := s.Register(Caller{Lane: 1, Guards: []string{"F"}, PID: AwaitPID})
	s.SetPID(tok, other.Process.Pid)
	r, _ := Ask(s.Path(), Request{Flag: "F", Token: tok}, nil, time.Time{})
	if r.Approved || asked || !strings.Contains(r.Why, "outside swim 1's lane") {
		t.Errorf("a request from outside the lane: %+v (asked %v)", r, asked)
	}
	if _, err := Whoami(s.Path(), tok); err == nil {
		t.Error("whoami answered a process outside the lane")
	}
	// The lane's own process (this one, as the lane) is answered.
	mine := s.Register(Caller{Lane: 1, Guards: []string{"F"}, PID: AwaitPID})
	s.SetPID(mine, os.Getpid())
	if r, _ := Ask(s.Path(), Request{Flag: "F", Token: mine}, nil, time.Time{}); !r.Approved {
		t.Errorf("the lane's own request: %+v", r)
	}
	if len(refused) == 0 {
		t.Error("the refusal wasn't reported")
	}
}

// A request must be one JSON message of at most MaxRequest bytes, sent
// within the read timeout; anything else is declined, unasked.
func TestHostileRequests(t *testing.T) {
	asked := 0
	var mu sync.Mutex
	s, err := Listen(func(Request, <-chan struct{}) Response {
		mu.Lock()
		asked++
		mu.Unlock()
		return Response{Approved: true}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tok := s.Register(Caller{Lane: 1, Guards: []string{"F"}})
	send := func(t *testing.T, payload []byte) Response {
		t.Helper()
		c, err := net.Dial("unix", s.Path())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		go c.Write(payload)
		var r Response
		c.SetReadDeadline(time.Now().Add(15 * time.Second))
		json.NewDecoder(bufio.NewReader(c)).Decode(&r)
		return r
	}
	big, _ := json.Marshal(Request{Flag: "F", Token: tok, Desc: strings.Repeat("x", MaxRequest+10)})
	if r := send(t, big); r.Approved || !strings.Contains(r.Why, "request too large") {
		t.Errorf("oversized: %+v", r)
	}
	if r := send(t, []byte("{not json\n")); r.Approved || r.Why != "malformed request" {
		t.Errorf("malformed: %+v", r)
	}
	// A client that never finishes its request is cut off.
	defer SetReadTimeout(SetReadTimeout(200 * time.Millisecond))
	if r := send(t, []byte(`{"flag":"F","token":"`)); r.Approved || !strings.Contains(r.Why, "too long") {
		t.Errorf("slow: %+v", r)
	}
	if asked != 0 {
		t.Errorf("asked %d hostile requests", asked)
	}
}

// A lane that hangs up while its question is open is no longer asked for.
func TestDisconnectCancels(t *testing.T) {
	cancelled := make(chan struct{})
	s, err := Listen(func(_ Request, cancel <-chan struct{}) Response {
		<-cancel
		close(cancelled)
		return Response{}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tok := s.Register(Caller{Lane: 1, Guards: []string{"F"}})
	c, err := net.Dial("unix", s.Path())
	if err != nil {
		t.Fatal(err)
	}
	json.NewEncoder(c).Encode(Request{Flag: "F", Token: tok})
	time.Sleep(100 * time.Millisecond)
	c.Close()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the question stayed open after the lane hung up")
	}
}

// Under swim run the approval is recorded by the launcher as the lane
// consumes it (Record), once; a replay records nothing.
func TestConsumeRecords(t *testing.T) {
	s, err := Listen(func(req Request, cancel <-chan struct{}) Response {
		return Response{Approved: true, Grant: &Grant{Entry: chain.Entry{Type: "approve", Guard: req.Flag}}}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var recorded []string
	s.Record = func(c Caller, g *Grant) (string, error) {
		recorded = append(recorded, fmt.Sprintf("%d %s", c.Lane, g.Entry.Guard))
		return "hash-1", nil
	}
	a := s.Register(Caller{Lane: 1, Guards: []string{"F"}})
	b := s.Register(Caller{Lane: 2, Guards: []string{"F"}})
	r, _ := Ask(s.Path(), Request{Flag: "F", Token: a}, nil, time.Time{})
	if _, err := Consume(s.Path(), b, r.ID, "F"); err == nil {
		t.Error("lane 2 replayed lane 1's approval")
	}
	if h, err := Consume(s.Path(), a, r.ID, "F"); err != nil || h != "hash-1" {
		t.Errorf("first use: %q %v", h, err)
	}
	if _, err := Consume(s.Path(), a, r.ID, "F"); err == nil {
		t.Error("used twice")
	}
	if len(recorded) != 1 || recorded[0] != "1 F" {
		t.Errorf("recorded %v", recorded)
	}
}

// A lane process hands its round's entry to the launcher (op record),
// which knows it by its token and process: a made-up token, or a token
// used from outside its lane, is refused before the launcher sees it.
func TestRecordFromTheLane(t *testing.T) {
	s, err := Listen(func(Request, <-chan struct{}) Response { return Response{} })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var got []string
	s.Chain = func(c Caller, e chain.Entry) (string, error) {
		got = append(got, fmt.Sprintf("%d %s %s", c.Lane, c.Run, e.Type))
		return "hash-1", nil
	}
	if _, err := Record(s.Path(), "made-up", chain.Entry{Type: "round"}); err == nil {
		t.Error("recorded for a made-up token")
	}
	tok := s.Register(Caller{Lane: 2, Run: "r1"})
	if h, err := Record(s.Path(), tok, chain.Entry{Type: "round", Lane: 1, Run: "other"}); err != nil || h != "hash-1" {
		t.Errorf("the lane's own entry: %q %v", h, err)
	}
	if peer.Supported {
		other := exec.Command("sleep", "30")
		if err := other.Start(); err != nil {
			t.Fatal(err)
		}
		defer other.Process.Kill()
		away := s.Register(Caller{Lane: 3, Run: "r1", PID: AwaitPID})
		s.SetPID(away, other.Process.Pid)
		if _, err := Record(s.Path(), away, chain.Entry{Type: "round"}); err == nil || !strings.Contains(err.Error(), "outside swim 3's lane") {
			t.Errorf("a token used from outside its lane: %v", err)
		}
	}
	if len(got) != 1 || got[0] != "2 r1 round" {
		t.Errorf("recorded %v", got)
	}
}
