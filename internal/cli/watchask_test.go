package cli

import (
	"net"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/queue"
	"github.com/gates-brightly/swimlane/internal/testwait"
)

// askPairForTest is a batch's channel: the batch's asker and swim watch's
// questions, served as swim watch serves a batch.
func askPairForTest(t *testing.T, seq int) (*askClient, *watchAsks, func()) {
	t.Helper()
	mine, theirs := net.Pipe()
	c := newAskClient(theirs)
	sig := &testwait.Signal{}
	a := &watchAsks{changed: sig.Notify}
	asksChanged[a] = sig
	done := make(chan struct{})
	go func() { a.serve(&queue.Entry{ID: "q-test", Seq: seq}, mine); close(done) }()
	return c, a, func() { theirs.Close(); <-done }
}

// asksChanged is each test's signal: a question came or went.
var asksChanged = map[*watchAsks]*testwait.Signal{}

func waitPending(t *testing.T, a *watchAsks, n int) []watchAsk {
	t.Helper()
	testwait.Until(t, "questions waiting", asksChanged[a], func() bool {
		p, _ := a.pending()
		return len(p) == n
	})
	p, _ := a.pending()
	return p
}

func TestWatchAskGuard(t *testing.T) {
	c, a, stop := askPairForTest(t, 7)
	defer stop()
	got := make(chan approval.Response, 1)
	go func() {
		got <- c.ask(approval.Request{Lane: 3, Flag: "ALLOW_DROP", Desc: "drop orders"}, nil)
	}()
	p := waitPending(t, a, 1)
	if p[0].msg.Lane != 3 || p[0].seq != 7 || p[0].msg.Choices != 0 {
		t.Fatalf("question = %+v", p[0])
	}
	if !strings.Contains(strings.Join(p[0].msg.Question, "\n"), "the lane says: drop orders") {
		t.Errorf("question lines: %q", p[0].msg.Question)
	}
	if msg := a.answer(p[0].key, '2'); !strings.Contains(msg, "y approves") {
		t.Errorf("a number on a guard: %q", msg)
	}
	if msg := a.answer(p[0].key, 'y'); !strings.Contains(msg, "swim 3 (#7): ALLOW_DROP approved") {
		t.Errorf("y: %q", msg)
	}
	r := <-got
	if !r.Approved || r.By == "" {
		t.Errorf("response = %+v", r)
	}
	waitPending(t, a, 0)
}

func TestWatchAskDecision(t *testing.T) {
	c, a, stop := askPairForTest(t, 1)
	defer stop()
	got := make(chan approval.Response, 1)
	go func() {
		got <- c.ask(approval.Request{Lane: 2, Flag: "ROLLOUT", Desc: "how?", Choices: []string{"canary", "all"}}, nil)
	}()
	waitPending(t, a, 1)
	for _, k := range []rune{'y', '3'} {
		if msg := a.answer("", k); !strings.Contains(msg, "press 1-2") {
			t.Errorf("%c on a decision: %q", k, msg)
		}
	}
	a.answer("", '2') // no row key: the oldest
	if r := <-got; !r.Approved || r.Choice != 2 {
		t.Errorf("response = %+v", r)
	}
}

// A run that stops withdraws its question; a batch that exits takes its
// questions with it.
func TestWatchAskCancelAndGone(t *testing.T) {
	c, a, stop := askPairForTest(t, 1)
	cancel := make(chan struct{})
	got := make(chan approval.Response, 1)
	go func() { got <- c.ask(approval.Request{Lane: 1, Flag: "F", Desc: "d"}, cancel) }()
	waitPending(t, a, 1)
	close(cancel)
	if r := <-got; r.Approved {
		t.Errorf("approved after cancel: %+v", r)
	}
	waitPending(t, a, 0)
	if msg := a.answer("", 'y'); msg != "nothing waits for an answer" {
		t.Errorf("y with nothing waiting: %q", msg)
	}

	go func() { got <- c.ask(approval.Request{Lane: 1, Flag: "F", Desc: "d"}, nil) }()
	waitPending(t, a, 1)
	stop()
	waitPending(t, a, 0)
	if r := <-got; r.Approved || r.Why == "" {
		t.Errorf("after the channel closed: %+v", r)
	}
}
