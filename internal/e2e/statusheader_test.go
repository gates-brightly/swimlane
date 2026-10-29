package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// swim status shows each pending lane's header: what it waits on and
// where that stands, each guard and how swim run would decide it now,
// and the rest of what the header sets; --yaml carries the same.
func TestStatusShowsPendingHeader(t *testing.T) {
	r := newRepo(t, "")
	keys := people(t, r, "groups:\n  dba: [alice, bob]\nrequire:\n  - guards: [DROP_*]\n    group: dba\n    count: 2\n", "alice", "bob")
	os.MkdirAll(filepath.Join(r.root, "lib"), 0o755)
	r.write("lib/tables.sh", "TABLE=old_orders\n", 0o644)
	r.script(1, "drop the old table", `stage change
if guard DROP_OK "drop table old_orders"; then run "drop" true; fi`)
	withHeader(t, r, 1, "# Timeout: 30m\n# Locks: orders-db\n# Includes: lib/tables.sh")
	r.script(2, "verify after the drop", `run "check" true`)
	withHeader(t, r, 2, "# After: 1")
	r.mustSwim("approve", "1", "--guards", "DROP_OK", "--key", keys["alice"])

	out := r.mustSwim("status")
	contains(t, "status, one of two", out,
		" swim 1  pending                drop the old table",
		"          guard DROP_OK  drop table old_orders",
		"                 not approved: approval needs 2 signature(s) from dba, has 1; swim run will ask (a dry run if no one answers)",
		"          timeout 30m · locks orders-db · includes lib/tables.sh",
		" swim 2  pending                verify after the drop",
		"          after swim 1 (pending: a dry run or failure there skips this)")
	if strings.Contains(out, "pending: drop the old table") || strings.Contains(out, "guards DROP_OK") {
		t.Errorf("the old pending lines are still there:\n%s", out)
	}
	y := r.mustSwim("status", "--yaml")
	contains(t, "status --yaml", y, "schema: swim.status/v1", "pending_header:", "flag: DROP_OK", "description: drop table old_orders",
		"approved: false", "asks: true", "why: approval needs 2 signature(s) from dba, has 1", "timeout: 30m", "locks: [orders-db]",
		"includes: [lib/tables.sh]", "after: [\"1\"]", "deps:", "state: pending")

	// The second signature: the guard is approved, as swim run will see it.
	r.mustSwim("approve", "1", "--key", keys["bob"])
	contains(t, "status, two of two", r.mustSwim("status", "1"), "                 approved by alice (dba), bob (dba) (signed)")

	// Once lane 1 has passed, lane 2's dependency says so; lane 1 shows
	// its run, not a header.
	r.mustSwim("run", "1", "--plain", "--no-prompt")
	out = r.mustSwim("status")
	contains(t, "after the run", out, "after swim 1 (passed)")
	if strings.Contains(out, "guard DROP_OK") {
		t.Errorf("a passed round still shows its header:\n%s", out)
	}
}

// A dependency with no Job: line that failed says failed, not pending:
// its round is matched by its Round: line, as swim run matches it.
func TestStatusDependencyFailedWithoutJob(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "deploy worker", `gate "deploy" false`)
	r.script(2, "smoke test", `run "check" true`)
	withHeader(t, r, 2, "# After: 1")
	if out, code := r.swim("run", "1", "--plain"); code != 1 {
		t.Fatalf("lane 1 should fail: exit %d\n%s", code, out)
	}
	contains(t, "status", r.mustSwim("status", "2"), "after swim 1 (failed: it must pass first: run both to retry it)")
}
