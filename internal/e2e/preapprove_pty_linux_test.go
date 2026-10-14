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
	r := newRepo(t, "")
	r.script(1, "first", `run "hold" bash -c 'while [ ! -e release ]; do sleep 0.05; done'`)
	r.script(2, "guarded", `if guard ALLOW_DROP "drop table orders"; then run "dropped" true; fi`)
	withHeader(t, r, 2, "# After: 1")
	withHeader(t, r, 2, "# Guards: ALLOW_DROP  drop table orders")
	r.script(3, "declined", `if guard ALLOW_PURGE "purge cache"; then run "purged" true; fi`)
	withHeader(t, r, 3, "# After: 1")
	r.script(4, "no guards", `run "plain" true`)
	withHeader(t, r, 4, "# After: 1")

	tr := r.startTUI(nil, "all")
	tr.waitScreen("lane 2 marked as asking", func(s string) bool { return strings.Contains(s, "? 1 guard") })
	tr.send("\x1b[C\x1b[C") // → → : swim 2
	tr.waitFor("guards: ALLOW_DROP (y pre-approve · n pre-decline)")
	tr.send("y")
	tr.waitFor("swim 2: pre-approved (1 guards)")
	tr.waitFor("✓ pre-approved")
	tr.send("\x1b[C") // swim 3
	tr.send("n")
	tr.waitFor("✗ dry run")
	tr.send("\x1b[C") // swim 4
	tr.send("y")
	tr.waitFor("swim 4 has no guards to approve")
	os.WriteFile(r.root+"/release", nil, 0o644)
	tr.waitFor("run finished · q to close")
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
	r := newRepo(t, "")
	r.script(1, "first", `run "hold" bash -c 'while [ ! -e release ]; do sleep 0.05; done'`)
	r.script(2, "guarded", `if guard ALLOW_DROP "drop table orders"; then run "dropped" true; fi`)
	withHeader(t, r, 2, "# After: 1")
	r.script(3, "cleared", `if guard ALLOW_X "do x"; then run "x" true; fi`)
	withHeader(t, r, 3, "# After: 1")

	tr := r.startTUI([]string{"SWIM_APPROVAL_TIMEOUT=1s"}, "all", "--no-prompt")
	tr.waitScreen("lanes listed", func(s string) bool { return strings.Contains(s, "swim 3") })
	tr.send("\x1b[C\x1b[C")
	tr.send("y")
	tr.waitFor("✓ pre-approved")
	tr.send("\x1b[C")
	tr.send("y")
	tr.waitFor("swim 3: pre-approved")
	tr.send("u")
	tr.waitFor("swim 3: pre-answer cleared")
	// Edit lane 2's script before it starts: its pre-approval no longer applies.
	withHeader(t, r, 2, "# Owner: someone else")
	os.WriteFile(r.root+"/release", nil, 0o644)
	tr.waitFor("run finished")
	tr.send("q")
	_, out := tr.wait()
	if strings.Contains(r.log(2), "PASS  dropped") || strings.Contains(r.log(3), "PASS  x") {
		t.Errorf("a stale or cleared pre-answer approved:\n%s\n%s", r.log(2), r.log(3))
	}
	contains(t, "summary", out, "unused pre-answers", "swim 2")
}
