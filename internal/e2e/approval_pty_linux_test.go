package e2e

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/launcher"
)

func approvalLanes(r *repo) {
	r.script(1, "guarded", `if guard ALLOW_DROP "drop table orders - 2026-10-12"; then run "dropped" true; fi
run "after" true`)
	r.script(2, "busy", `run "work" bash -c 'for i in 1 2 3 4 5 6 7 8; do echo busy-$i; sleep 0.1; done'`)
}

func TestPromptInTUI(t *testing.T) {
	r := newSerialRepo(t, "")
	approvalLanes(r)
	tr := r.startTUI(nil, "all")
	// From the all view: a banner, not a question that takes the keys.
	s := tr.waitScreen("the approval banner", func(s string) bool {
		return strings.Contains(s, "▶ swim 1 needs approval · Enter to view it and answer · drop table orders - 2026-10-12")
	})
	contains(t, "panel", s, "awaiting approval: drop", "all lanes")
	tr.send("y") // not on lane 1's view: doesn't answer
	// Enter opens lane 1: its log, with the question under it.
	tr.send("\r")
	s = tr.waitScreen("lane 1 with its question", func(s string) bool {
		return strings.Contains(s, "swim 1 · ") && strings.Contains(s, "y approve   n decline") && strings.Contains(s, "Y/N [N]")
	})
	contains(t, "lane view", s, "== ROUND ", "▶swim 1")
	if strings.Contains(r.log(1), "APPROVED") {
		t.Fatal("y on the all view answered the question")
	}
	tr.send("y")
	tr.waitFor("run finished · q to close") // still on lane 1's view: the screen holds
	tr.send("q")
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
	r := newSerialRepo(t, "")
	approvalLanes(r)
	tr := r.startTUI(nil, "all", "--no-tui")
	tr.waitFor("the lane says: drop table orders - 2026-10-12")
	tr.send("\r") // Enter: the default, no
	code, out := tr.wait()
	if code != launcher.ExitDryRun {
		t.Fatalf("exit %d\n%s", code, out)
	}
	log := r.log(1)
	contains(t, "agent1.log", log, "SKIP  drop table orders - 2026-10-12 (dry run: declined at the prompt by ", "; set ALLOW_DROP=1 to approve)", "PASS  after")
	if strings.Contains(log, "PASS  dropped") {
		t.Error("declined step ran")
	}
	// Lane 2's output, held while the question was up, still arrives.
	// The panel redraws between a save and a restore of the cursor (ESC 7 ...
	// ESC 8), at the top of the screen: drop those, as the screen does, so
	// one landing while the question is up doesn't come between it and its
	// answer here.
	plain := regexp.MustCompile(`(?s)\x1b7.*?\x1b8`).ReplaceAllString(out, "")
	plain = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`).ReplaceAllString(plain, "")
	contains(t, "output", plain, "Y/N [N] no", "[2] busy-8")
}

func TestPromptSingleLane(t *testing.T) {
	r := newSerialRepo(t, "")
	approvalLanes(r)
	tr := r.startTUI(nil, "run", "1")
	tr.waitFor("requires human approval of guard ALLOW_DROP")
	tr.send("Y")
	if code, out := tr.wait(); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	contains(t, "agent1.log", r.log(1), "APPROVED  drop table orders", "PASS  dropped")
}

func TestPromptOffWithFlag(t *testing.T) {
	r := newSerialRepo(t, "")
	approvalLanes(r)
	tr := r.startTUI(nil, "all", "--no-prompt")
	code, out := tr.wait()
	if code != launcher.ExitDryRun || strings.Contains(out, "requires human approval") {
		t.Fatalf("--no-prompt: exit %d\n%s", code, out)
	}
	contains(t, "agent1.log", r.log(1), "SKIP  drop table orders - 2026-10-12 (dry run: set ALLOW_DROP=1 to approve)")
}

func TestPromptCtrlCStops(t *testing.T) {
	r := newSerialRepo(t, "")
	approvalLanes(r)
	tr := r.startTUI(nil, "all")
	tr.waitFor("swim 1 needs approval · Enter to view")
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

func TestPromptSeveralLanes(t *testing.T) {
	long(t)
	r := newSerialRepo(t, "")
	r.script(1, "one", `if guard ALLOW_ONE "change one"; then run "one changed" true; fi`)
	r.script(2, "two", `if guard ALLOW_TWO "change two"; then run "two changed" true; fi`)
	r.script(3, "busy", `run "work" bash -c 'for i in $(seq 1 30); do echo busy-$i; sleep 0.1; done'`)
	tr := r.startTUI(nil, "all")
	tr.waitScreen("both waiting", func(s string) bool {
		return strings.Contains(s, "need approval · Enter to view swim")
	})
	tr.send("\r")
	s := tr.waitScreen("a waiting lane", func(s string) bool { return strings.Contains(s, "y approve   n decline") })
	first := 1
	if strings.Contains(s, "swim 2 requires human approval") {
		first = 2
	}
	second := 3 - first
	tr.send("n") // decline the first...
	tr.waitScreen("only the other still waiting", func(s string) bool {
		return strings.Contains(s, fmt.Sprintf("▶ swim %d needs approval · Enter to view", second))
	})
	tr.send("\x1b") // Esc: all lanes; the other question still waits
	tr.waitFor("all lanes")
	tr.send("\r") // ...then approve the second
	tr.waitScreen("the second lane", func(s string) bool {
		return strings.Contains(s, fmt.Sprintf("swim %d requires human approval", second)) && strings.Contains(s, "y approve")
	})
	tr.send("y")
	tr.waitFor("run finished · q to close") // we're on a lane view: the screen holds
	tr.send("q")
	if code, out := tr.wait(); code != launcher.ExitDryRun {
		t.Fatalf("exit %d\n%s", code, out)
	}
	contains(t, "first lane", r.log(first), "SKIP  change ", "declined at the prompt")
	contains(t, "second lane", r.log(second), "APPROVED  change ", "PASS  ")
}

// A guard's description can't rewrite its own question: the erase, the
// carriage return, the backspace and the right-to-left override never
// reach the terminal, at the prompt line or in the live view.
func TestPromptIsSafe(t *testing.T) {
	for _, mode := range []string{"--no-tui", "tui"} {
		t.Run(mode, func(t *testing.T) {
			r := newSerialRepo(t, "")
			r.script(1, "hostile", `if guard ALLOW_X `+payload+`; then run "changed" true; fi`)
			args := []string{"all"}
			if mode != "tui" {
				args = append(args, mode)
			}
			tr := r.startTUI(nil, args...)
			tr.waitFor("the lane says: " + payloadSafe)
			tr.send("n")
			if mode == "tui" {
				// The view closes by itself (swim exits, the main screen
				// back), or holds on the lane's view: which one depends on
				// how fast the round ends after the answer.
				s := tr.waitScreen("the run's end", func(s string) bool {
					return tr.exited.Load() || strings.Contains(s, "q to close") || strings.Contains(s, "stages: setup SKIP")
				})
				if strings.Contains(s, "q to close") {
					tr.send("q")
				}
			}
			_, out := tr.wait()
			for _, bad := range []string{"\x1b[2K\rlist", "\b", "\u202e"} {
				if strings.Contains(out, bad) {
					t.Errorf("the terminal got %q:\n%q", bad, out)
				}
			}
			contains(t, "log", r.log(1), "SKIP  "+payloadSafe+" (dry run: declined at the prompt by ")
		})
	}
}
