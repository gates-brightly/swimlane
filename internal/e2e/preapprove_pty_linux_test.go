package e2e

import (
	"os"
	"strings"
	"testing"
)

// In the live view, y on a lane that hasn't asked yet answers its guards
// ahead: it runs its guarded step without asking, and the log and chain
// say who pre-approved it. n pre-declines another lane's (dry runs).
func TestPreapproveInTUI(t *testing.T) {
	r := newSerialRepo(t)
	r.script(1, "first", `step "hold" bash -c 'rf="$SWIM_ROOT/release"; [ -p "$rf" ] || mkfifo "$rf" 2>/dev/null; cat "$rf" >/dev/null'`)
	r.script(2, "guarded", `if guard ALLOW_DROP "drop table orders"; then step "dropped" true; fi`)
	withHeader(t, r, 2, "# After: 1")
	withHeader(t, r, 2, "# Guards: ALLOW_DROP  drop table orders")
	r.script(3, "declined", `if guard ALLOW_PURGE "purge cache"; then step "purged" true; fi`)
	withHeader(t, r, 3, "# After: 1")
	r.script(4, "no guards", `step "plain" true`)
	withHeader(t, r, 4, "# After: 1")

	tr := r.startTUI(nil, "all")
	tr.waitScreen("lane 2 marked as asking", func(s string) bool { return strings.Contains(s, "? 1 guard") })
	tr.send("\x1b[C\x1b[C") // → → : lane 2
	tr.waitFor("guards: ALLOW_DROP (y pre-approve · n pre-decline)")
	tr.send("y")
	tr.waitFor("lane 2: pre-approved (1 guards)")
	tr.waitFor("✓ pre-approved")
	tr.send("\x1b[C") // lane 3
	tr.send("n")
	tr.waitFor("✗ dry run")
	tr.send("\x1b[C") // lane 4
	tr.send("y")
	tr.waitFor("lane 4 has no guards to approve")
	os.WriteFile(r.root+"/release", nil, 0o644)
	tr.waitFor("run ended · q to close")
	tr.send("q")
	tr.wait()

	contains(t, "lane 2", r.log(2), "APPROVED  drop table orders (pre-approved by ", " in the live view)", "PASS  dropped")
	contains(t, "lane 3", r.log(3), "SKIP  purge cache (dry run: pre-declined by ")
	chain, _ := os.ReadFile(r.root + "/.swim/chain.jsonl")
	contains(t, "chain", string(chain), `"method":"preapproved"`)
}

// u clears a pre-answer, and a script changed after it was given asks
// again (here, with no one to ask: a dry run), listed as unused.
func TestPreapprovePinnedToScript(t *testing.T) {
	r := newSerialRepo(t)
	r.script(1, "first", `step "hold" bash -c 'rf="$SWIM_ROOT/release"; [ -p "$rf" ] || mkfifo "$rf" 2>/dev/null; cat "$rf" >/dev/null'`)
	r.script(2, "guarded", `if guard ALLOW_DROP "drop table orders"; then step "dropped" true; fi`)
	withHeader(t, r, 2, "# After: 1")
	r.script(3, "cleared", `if guard ALLOW_X "do x"; then step "x" true; fi`)
	withHeader(t, r, 3, "# After: 1")

	tr := r.startTUI([]string{"SWIM_APPROVAL_TIMEOUT=1s"}, "all", "--no-prompt")
	tr.waitScreen("lanes listed", func(s string) bool { return strings.Contains(s, "lane 3") })
	tr.send("\x1b[C\x1b[C")
	tr.send("y")
	tr.waitFor("✓ pre-approved")
	tr.send("\x1b[C")
	tr.send("y")
	tr.waitFor("lane 3: pre-approved")
	tr.send("u")
	tr.waitFor("lane 3: pre-answer cleared")
	// Edit lane 2's script before it starts: its pre-approval no longer applies.
	withHeader(t, r, 2, "# Owner: someone else")
	os.WriteFile(r.root+"/release", nil, 0o644)
	tr.waitFor("run ended")
	tr.send("q")
	_, out := tr.wait()
	if strings.Contains(r.log(2), "PASS  dropped") || strings.Contains(r.log(3), "PASS  x") {
		t.Errorf("a stale or cleared pre-answer approved:\n%s\n%s", r.log(2), r.log(3))
	}
	contains(t, "summary", out, "unused pre-answers", "lane 2")
}

// Precedence: a pre-decline is the answer (the lane never asks), and
// FLAG=0 beats a pre-approval.
func TestPreanswerPrecedence(t *testing.T) {
	r := newSerialRepo(t)
	r.script(1, "first", `step "hold" bash -c 'rf="$SWIM_ROOT/release"; [ -p "$rf" ] || mkfifo "$rf" 2>/dev/null; cat "$rf" >/dev/null'`)
	r.script(2, "declined", `if guard ALLOW_A "do a"; then step "did a" true; fi`)
	withHeader(t, r, 2, "# After: 1")
	r.script(3, "flag zero", `if guard ALLOW_B "do b"; then step "did b" true; fi`)
	withHeader(t, r, 3, "# After: 1")

	tr := r.startTUI([]string{"ALLOW_B=0"}, "all")
	tr.waitScreen("lanes listed", func(s string) bool { return strings.Contains(s, "lane 3") })
	tr.send("\x1b[C\x1b[C")
	tr.send("n")
	tr.waitFor("lane 2: pre-declined")
	tr.send("\x1b[C")
	tr.send("y")
	tr.waitFor("lane 3: pre-approved")
	os.WriteFile(r.root+"/release", nil, 0o644)
	tr.waitFor("run ended")
	tr.send("q")
	tr.wait()
	contains(t, "pre-declined", r.log(2), "SKIP  do a (dry run: pre-declined by ")
	if strings.Contains(r.log(2), "PASS  did a") {
		t.Error("a pre-declined guard ran")
	}
	if strings.Contains(r.log(3), "PASS  did b") {
		t.Error("a pre-approval overrode ALLOW_B=0")
	}
	contains(t, "flag zero", r.log(3), "SKIP  do b (dry run: set ALLOW_B=1 to approve)")
}
