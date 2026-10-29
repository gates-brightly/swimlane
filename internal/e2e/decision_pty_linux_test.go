package e2e

import (
	"strings"
	"testing"
)

// At the prompt, only a listed number answers a decision: other keys are
// ignored, and the number picks the branch.
func TestDecisionPromptLine(t *testing.T) {
	r := newSerialRepo(t, "")
	decisionLane(r, 1)
	tr := r.startTUI(nil, "run", "1")
	tr.waitFor("choose 1-3 (or set ROLLOUT=N to answer without asking):")
	s := tr.screen()
	contains(t, "the question", s, "swim 1 asks for a decision: ROLLOUT", "the lane asks: How should it roll out?",
		"1. canary", "2. all at once", "3. don't deploy")
	tr.send("5") // not a choice
	tr.send("y") // nor is this
	tr.send("2")
	code, out := tr.wait()
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	log := r.log(1)
	contains(t, "agent1.log", log, "DECIDED  ROLLOUT: 2. all at once (chosen at the prompt by ", "PASS  all", "PASS  after")
	if strings.Contains(log, "PASS  canary") {
		t.Errorf("another branch ran:\n%s", log)
	}
	contains(t, "history", r.mustSwim("history", "--oneline"), "decide ROLLOUT=2 for swim 1 by", "via prompt")
}

// In the live view, a lane waiting on a decision shows in the banner; on
// its view the choices sit under its log, and a number key answers.
func TestDecisionPromptInTUI(t *testing.T) {
	r := newSerialRepo(t, "")
	decisionLane(r, 1)
	r.script(2, "busy", `run "work" bash -c 'for i in 1 2 3 4 5 6 7 8; do echo busy-$i; done'`)
	tr := r.startTUI(nil, "all")
	tr.waitScreen("the decision banner", func(s string) bool {
		return strings.Contains(s, "▶ swim 1 needs a decision · Enter to view it and answer · How should it roll out?")
	})
	tr.send("\r")
	tr.waitScreen("lane 1 with its choices", func(s string) bool {
		return strings.Contains(s, "1-3 choose") && strings.Contains(s, "3. don't deploy")
	})
	tr.send("y") // not an answer to a decision
	tr.send("1")
	tr.waitFor("run finished · q to close")
	tr.send("q")
	if code, out := tr.wait(); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	contains(t, "agent1.log", r.log(1), "DECIDED  ROLLOUT: 1. canary (chosen at the prompt by ", "PASS  canary")
}
