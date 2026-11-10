package e2e

import (
	"strings"
	"testing"
)

// guard FLAG takes what it does from its # Guards: line, and swim status
// shows why from the line under it: in the dry run, the approval and the
// pending lane's header, in a full lane and a short-form one.
func TestGuardTextFromHeader(t *testing.T) {
	r := newRepo(t)
	shortLane(t, r, 1, `# Round: drop the old table
# Guards: DROP_ALLOW  drop table orders_old (12 rows)
#   why: merged into orders on 2026-11-02
stage check
step "count" true
stage change
if guard DROP_ALLOW; then
  step "drop it" true
fi
`)
	contains(t, "lint", r.mustSwim("lint", "1"), "no problems found")
	contains(t, "status", r.mustSwim("status", "1"), "guard DROP_ALLOW  drop table orders_old (12 rows)", "why: merged into orders on 2026-11-02")

	r.mustDryRun("run", "1", "--plain", "--no-prompt")
	contains(t, "dry run", r.log(1), "SKIP  drop table orders_old (12 rows) (dry run")
	r.mustSwimEnv([]string{"DROP_ALLOW=1"}, "run", "1", "--plain")
	contains(t, "approved", r.log(1), "APPROVED  drop table orders_old (12 rows) (DROP_ALLOW=1", "PASS  drop it")

	// A full lane run directly with bash asks the same.
	r.script(2, "two", `stage change
if guard TWO_ALLOW; then step "two" true; fi`)
	withHeader(t, r, 2, "# Guards: TWO_ALLOW  delete the two scratch files\n#   why: the test made them")
	out, _ := r.swimEnv([]string{"TWO_ALLOW=0"}, "run", "2", "--plain", "--no-prompt")
	if !strings.Contains(r.log(2), "SKIP  delete the two scratch files (dry run") {
		t.Errorf("lane 2's dry run:\n%s\n%s", out, r.log(2))
	}
}
