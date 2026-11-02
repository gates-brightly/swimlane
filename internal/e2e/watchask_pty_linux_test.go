package e2e

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/ui"
)

// waitLog waits for lane n's log to say want, and returns it.
func (r *repo) waitLog(n int, want string) string {
	r.t.Helper()
	waitFor(r.t, func() bool { return strings.Contains(r.log(n), want) })
	return r.log(n)
}

// Two batches side by side, each with a lane at a guard: the watch screen
// asks for both in yellow, naming the lane and batch each blocks, and y
// or n answers the one selected (the newest), then the other, with no
// swim run in between.
func TestWatchAsksForBatchGuards(t *testing.T) {
	long(t)
	r := newSerialRepo(t, "")
	r.script(1, "one", `if guard ALLOW_ONE "change one"; then run "one changed" true; fi`)
	r.script(2, "two", `if guard ALLOW_TWO "change two"; then run "two changed" true; fi`)
	r.mustSwim("queue", "add", "1")
	r.mustSwim("queue", "add", "2")
	tr := r.startTUI(nil, "watch", "--batches", "2")
	s := tr.waitScreen("both questions", func(s string) bool {
		return strings.Contains(s, "⚠ swim 1 is blocked: waits for approval of ALLOW_ONE") &&
			strings.Contains(s, "⚠ swim 2 is blocked: waits for approval of ALLOW_TWO")
	})
	contains(t, "the questions", s, "batch #1", "batch #2", "y approve  n decline", "the lane says: change one", "the lane says: change two")
	// Under their own bar, in yellow, over the jobs and the queue.
	if !strings.Contains(s, "── ⚠ Waiting for you (2) ─") || strings.Index(s, "Waiting for you") > strings.Index(s, "── Jobs") {
		t.Errorf("no section bar over the questions:\n%s", s)
	}
	if !strings.Contains(tr.output(), ui.Yellow+ui.Bold+"⚠ Waiting for you (2)") {
		t.Error("the questions' bar isn't yellow")
	}
	// The newest is selected: y answers it, then n the one left.
	newest, other := 2, 1
	if strings.Contains(s, "▸ ⚠ swim 1") {
		newest, other = 1, 2
	}
	tr.send("y")
	tr.waitFor(fmt.Sprintf("swim %d (#%d): ALLOW_", newest, newest))
	tr.send("n")
	tr.waitFor(fmt.Sprintf("swim %d (#%d): ALLOW_", other, other))
	tr.waitScreen("the bar gone with the questions", func(s string) bool { return !strings.Contains(s, "Waiting for you") })
	contains(t, "approved lane", r.waitLog(newest, "PASS  "), "APPROVED  change ", "approved at the prompt by ")
	contains(t, "declined lane", r.waitLog(other, "SKIP  "), "SKIP  change ", "declined at the prompt")
	tr.send("\x03")
	tr.wait()
}

// A decision in a batch: a number answers it on the watch screen.
func TestWatchAsksForBatchDecision(t *testing.T) {
	r := newSerialRepo(t, "")
	decisionLane(r, 1)
	r.mustSwim("queue", "add", "1")
	tr := r.startTUI(nil, "watch", "--batches", "2")
	s := tr.waitFor("⚠ swim 1 is blocked: waits for a decision: ROLLOUT")
	contains(t, "the decision", s, "1-3 choose", "1. canary", "3. don't deploy")
	tr.send("y") // not an answer to a decision
	tr.waitFor("press 1-3")
	tr.send("1")
	tr.waitFor("swim 1 (#1): ROLLOUT = 1")
	contains(t, "agent1.log", r.waitLog(1, "PASS  canary"), "DECIDED  ROLLOUT: 1. canary (chosen at the prompt by ", "PASS  canary")
	tr.send("\x03")
	tr.wait()
}

// A lane whose script changes while swim watch is up gets (~) next to it,
// until the operator acts on it (here q, then q again to take it back
// out of the next batch). Lanes untouched since stay unmarked.
func TestWatchMarksChangedLanes(t *testing.T) {
	r := newSerialRepo(t, "")
	r.script(1, "first job", `run "one" true`)
	r.script(2, "second job", `run "two" true`)
	tr := r.startTUI(nil, "watch")
	s := tr.waitFor("▸ 1 ")
	if strings.Contains(s, "(~)") {
		t.Fatalf("a lane marked before anything changed:\n%s", s)
	}
	r.script(2, "second job, rewritten", `run "two again" true`)
	s = tr.waitFor("2  (~)")
	if strings.Contains(s, "1  (~)") {
		t.Errorf("swim 1 marked, but only swim 2 changed:\n%s", s)
	}
	tr.send("\x1b[B")
	tr.waitFor("▸ 2 ")
	tr.send("q")
	tr.waitFor("next batch: swim 2")
	tr.send("q")
	s = tr.waitFor("out of the next batch")
	if strings.Contains(s, "(~)") {
		t.Errorf("swim 2 still marked after q:\n%s", s)
	}
	tr.send("\x03")
	tr.wait()
}
