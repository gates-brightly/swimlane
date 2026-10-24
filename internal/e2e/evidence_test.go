package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// evidenceLane: a round whose check stage shows its plan (from plan.txt)
// as evidence, and applies it behind a guard.
func evidenceLane(t *testing.T, r *repo) {
	t.Helper()
	r.write("plan.txt", "# aws_dynamodb_table.orders will be destroyed   \r\nPlan: 0 to add, 1 to change, 1 to destroy.\n", 0o644)
	r.script(1, "apply orders plan", `stage check
evidence "tf plan" cat "$SWIM_ROOT/plan.txt"
stage change
if guard ORDERS_ALLOW_APPLY "apply orders plan"; then run "tf apply" touch "$SWIM_ROOT/applied"; fi`)
}

// An approval covers the plan: swim approve shows the evidence from
// swim plan --check and signs its hash; the guard counts it while the run's
// evidence matches, and not once the plan changed (a WARN and a diff, then
// a dry run with nobody to ask).
func TestEvidenceBoundApproval(t *testing.T) {
	r := newRepo(t, "")
	keys := people(t, r, "", "alice")
	evidenceLane(t, r)

	out, code := r.swim("approve", "1", "--guards", "ORDERS_ALLOW_APPLY", "--key", keys["alice"])
	if code == 0 || !strings.Contains(out, "no evidence yet: run swim plan --check 1 first") {
		t.Fatalf("approve without evidence: exit %d\n%s", code, out)
	}
	r.swim("plan", "--check", "1")
	out = r.mustSwim("approve", "1", "--guards", "ORDERS_ALLOW_APPLY", "--key", keys["alice"])
	contains(t, "approve", out, "evidence from swim plan --check", "== tf plan", "aws_dynamodb_table.orders will be destroyed", "evidence ")
	matches, _ := filepath.Glob(filepath.Join(r.root, "approvals", "lane.1.*.e*.signature"))
	if len(matches) != 1 {
		t.Fatalf("approval files: %v", matches)
	}

	// Same plan (trailing whitespace and line endings differ): it counts.
	r.write("plan.txt", "# aws_dynamodb_table.orders will be destroyed\nPlan: 0 to add, 1 to change, 1 to destroy.\n", 0o644)
	r.mustSwim("run", "1", "--plain", "--no-prompt")
	contains(t, "matching evidence", r.log(1), "APPROVED  apply orders plan (approved by alice", "; evidence ", "PASS  tf apply")
	chain, _ := os.ReadFile(filepath.Join(r.root, ".swim", "chain.jsonl"))
	contains(t, "chain", string(chain), `"evidence_sha256":"`)

	// The plan changed: the approval doesn't count.
	os.Remove(filepath.Join(r.root, "applied"))
	r.write("plan.txt", "# aws_dynamodb_table.orders will be destroyed\n# aws_s3_bucket.orders will be destroyed\nPlan: 0 to add, 1 to change, 2 to destroy.\n", 0o644)
	r.swim("run", "1", "--plain", "--no-prompt")
	last := r.log(1)[strings.LastIndex(r.log(1), "== ROUND"):]
	contains(t, "changed evidence", last, "WARN  evidence changed since approval: apply orders plan", "| + # aws_s3_bucket.orders will be destroyed",
		"| - Plan: 0 to add, 1 to change, 1 to destroy.", "SKIP  apply orders plan (dry run")
	if _, err := os.Stat(filepath.Join(r.root, "applied")); err == nil {
		t.Error("applied with a stale plan approval")
	}

	// --no-evidence approves the script only (and counts, with no rule
	// requiring evidence).
	r.mustSwim("approve", "1", "--guards", "ORDERS_ALLOW_APPLY", "--no-evidence", "--key", keys["alice"])
	r.mustSwim("run", "1", "--plain", "--no-prompt")
	contains(t, "script-only", r.log(1)[strings.LastIndex(r.log(1), "== ROUND"):], "PASS  tf apply")
	r.mustSwim("verify", "--levels", "--allow-unsigned") // old-style and evidence-bound entries verify together
}

// A lookup.yml rule with evidence: true ignores script-only approvals at a
// terminal or in CI; swim watch doesn't compare evidence.
func TestEvidenceRuleAndWatch(t *testing.T) {
	r := newRepo(t, "")
	keys := people(t, r, "require:\n  - guards: [ORDERS_*]\n    evidence: true\n", "alice")
	evidenceLane(t, r)
	r.mustSwim("approve", "1", "--guards", "ORDERS_ALLOW_APPLY", "--no-evidence", "--key", keys["alice"])
	r.swim("run", "1", "--plain", "--no-prompt")
	if strings.Contains(r.log(1), "PASS  tf apply") {
		t.Error("a script-only approval counted under evidence: true")
	}

	// Approve with evidence, then change the plan and queue it: watch
	// applies the approval on the script alone, and says so.
	r.swim("plan", "--check", "1")
	r.mustSwim("approve", "1", "--guards", "ORDERS_ALLOW_APPLY", "--key", keys["alice"])
	r.write("plan.txt", "something else entirely\n", 0o644)
	r.mustSwim("queue", "add", "1")
	r.cmd(bin, "watch", "--once").CombinedOutput()
	contains(t, "watch", r.log(1)[strings.LastIndex(r.log(1), "== ROUND"):], "not checked (watch)", "PASS  tf apply")
}
