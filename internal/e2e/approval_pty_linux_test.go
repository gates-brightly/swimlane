package e2e

import (
	"regexp"
	"strings"
	"testing"
)

func approvalLanes(r *repo) {
	r.script(1, "guarded", `if guard ALLOW_DROP "drop table orders - 2026-10-12"; then run "dropped" true; fi
run "after" true`)
	r.script(2, "busy", `run "work" bash -c 'for i in 1 2 3 4 5 6 7 8; do echo busy-$i; sleep 0.1; done'`)
}

func TestPromptInTUI(t *testing.T) {
	r := newRepo(t, "")
	approvalLanes(r)
	tr := r.startTUI(nil, "all")
	s := tr.waitScreen("the approval box", func(s string) bool {
		return strings.Contains(s, "swim 1 requires human approval of task: drop table orders - 2026-10-12") &&
			strings.Contains(s, "Y/N [N]")
	})
	contains(t, "panel", s, "awaiting approval: drop")
	tr.send("l") // other keys don't answer
	tr.send("y")
	code, _ := tr.wait()
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	contains(t, "agent1.log", r.log(1), "APPROVED  drop table orders - 2026-10-12 (approved at the prompt by ", "PASS  dropped")
	if got := tr.termiosNow(); got != tr.before {
		t.Errorf("terminal not restored")
	}
}

func TestPromptLineDeclines(t *testing.T) {
	r := newRepo(t, "")
	approvalLanes(r)
	tr := r.startTUI(nil, "all", "--no-tui")
	tr.waitFor("swim 1 requires human approval of task: drop table orders - 2026-10-12")
	tr.send("\r") // Enter: the default, no
	code, out := tr.wait()
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	log := r.log(1)
	contains(t, "agent1.log", log, "SKIP  drop table orders - 2026-10-12 (dry run: declined at the prompt by ", "; set ALLOW_DROP=1 to approve)", "PASS  after")
	if strings.Contains(log, "PASS  dropped") {
		t.Error("declined step ran")
	}
	// Lane 2's output, held while the question was up, still arrives.
	plain := regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\x1b[78]`).ReplaceAllString(out, "")
	contains(t, "output", plain, "Y/N [N] no", "[2] busy-8")
}

func TestPromptSingleLane(t *testing.T) {
	r := newRepo(t, "")
	approvalLanes(r)
	tr := r.startTUI(nil, "run", "1")
	tr.waitFor("requires human approval of task: drop table orders")
	tr.send("Y")
	if code, out := tr.wait(); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	contains(t, "agent1.log", r.log(1), "APPROVED  drop table orders", "PASS  dropped")
}

func TestPromptOffWithFlag(t *testing.T) {
	r := newRepo(t, "")
	approvalLanes(r)
	tr := r.startTUI(nil, "all", "--no-prompt")
	code, out := tr.wait()
	if code != 0 || strings.Contains(out, "requires human approval") {
		t.Fatalf("--no-prompt: exit %d\n%s", code, out)
	}
	contains(t, "agent1.log", r.log(1), "SKIP  drop table orders - 2026-10-12 (dry run: set ALLOW_DROP=1 to approve)")
}

func TestPromptCtrlCStops(t *testing.T) {
	r := newRepo(t, "")
	approvalLanes(r)
	tr := r.startTUI(nil, "all")
	tr.waitFor("requires human approval of task: drop table orders")
	tr.send("\x03")
	code, _ := tr.wait()
	if code == 0 {
		t.Error("an interrupted run exits non-zero")
	}
	log := r.log(1)
	contains(t, "agent1.log", log, "SKIP  drop table orders - 2026-10-12 (dry run: ", "STOP  interrupted by operator", "== END INTERRUPTED")
	if strings.Contains(log, "PASS  dropped") || strings.Contains(log, "PASS  after") {
		t.Errorf("the round went on after Ctrl-C at the prompt:\n%s", log)
	}
}
