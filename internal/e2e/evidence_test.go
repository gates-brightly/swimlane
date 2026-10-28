package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/sigfile"
	"github.com/gates-brightly/swimlane/internal/sign"
)

// Evidence-bound approvals are gone. What an older swim left behind still
// reads: its signed approvals verify, and count as approvals of the script;
// a lookup.yml rule that asked for them fails closed; an evidence step is a
// lint error.

// planLane: a round whose check stage saves its plan (a snapshot), and
// applies it behind a guard.
func planLane(r *repo) {
	r.write("plan.txt", "Plan: 0 to add, 1 to change, 1 to destroy.\n", 0o644)
	r.script(1, "apply orders plan", `stage check
snapshot "tf plan" cat "$SWIM_ROOT/plan.txt"
stage change
if guard ORDERS_ALLOW_APPLY "apply orders plan"; then run "tf apply" touch "$SWIM_ROOT/applied"; fi`)
}

// oldEvidenceApproval re-signs lane 1's approval (approvals/lane.1.*) as
// an older swim's evidence-bound one: the same statement plus an
// evidence-sha256 line, in a file named .e<evidence8>, signed by key.
func oldEvidenceApproval(t *testing.T, r *repo, keyPath, name string) string {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(r.root, "approvals", "lane.1.*.signature"))
	if len(matches) != 1 {
		t.Fatalf("approval files: %v", matches)
	}
	old, err := sigfile.Load(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	ev := strings.Repeat("e", 64)
	f := &sigfile.File{Kind: old.Kind, Script: old.Script, SHA256: old.SHA256, Lane: old.Lane, Job: old.Job, Round: old.Round,
		Evidence: ev, Manifest: old.Manifest, Pinned: old.Pinned, Approves: old.Approves, All: old.All, Expires: old.Expires,
		Path: strings.TrimSuffix(old.Path, ".signature") + ".e" + ev[:8] + ".signature"}
	k, err := sign.LoadKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Add(k, sigfile.Approve, name); err != nil {
		t.Fatal(err)
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	os.Remove(old.Path)
	return f.Path
}

// An approval an older swim bound to evidence still verifies, and counts
// as an approval of the script where the guard's rule never asked for
// evidence.
func TestOldEvidenceApprovalCounts(t *testing.T) {
	r := newRepo(t, "")
	keys := people(t, r, "", "alice")
	planLane(r)
	r.mustSwim("approve", "1", "--guards", "ORDERS_ALLOW_APPLY", "--key", keys["alice"])
	path := oldEvidenceApproval(t, r, keys["alice"], "alice")
	contains(t, "swim approve", r.mustSwim("approve"), filepath.Base(path), "alice")
	r.mustSwim("run", "1", "--plain", "--no-prompt")
	contains(t, "old evidence approval", r.log(1), "APPROVED  apply orders plan (approved by alice", "PASS  tf apply")
	if strings.Contains(r.log(1), "evidence") {
		t.Errorf("the log still talks of evidence:\n%s", r.log(1))
	}
	chain, _ := os.ReadFile(filepath.Join(r.root, ".swim", "chain.jsonl"))
	if strings.Contains(string(chain), `"evidence_sha256"`) {
		t.Error("the chain still records evidence hashes")
	}
	contains(t, "chain", string(chain), "evidence-sha256: eeeeeeee") // the approver's signed statement, as signed
	r.mustSwim("verify", "--levels", "--allow-unsigned")
}

// A lookup.yml rule with evidence: true fails closed: nothing approves
// its guards (swim approve, a signed approval an older swim bound to
// evidence, FLAG=1, -y, queue --approve), swim plan says why, and swim
// doctor warns.
func TestEvidenceRuleFailsClosed(t *testing.T) {
	r := newRepo(t, "")
	keys := people(t, r, "require:\n  - guards: [ORDERS_*]\n    evidence: true\n", "alice")
	planLane(r)
	const why = "lookup.yml rule for ORDERS_ALLOW_APPLY requires evidence-bound approvals, which swim no longer has: remove `evidence: true`"

	out, code := r.swim("approve", "1", "--guards", "ORDERS_ALLOW_APPLY", "--key", keys["alice"])
	if code == 0 || !strings.Contains(out, why) {
		t.Fatalf("approve --guards: exit %d\n%s", code, out)
	}
	out = r.mustSwim("approve", "1", "--all", "--key", keys["alice"])
	contains(t, "approve --all", out, "never:    ORDERS_ALLOW_APPLY: "+why)
	oldEvidenceApproval(t, r, keys["alice"], "alice")

	contains(t, "plan", r.mustSwim("plan", "1"), "not approved: a dry run, whatever answers it; "+why)

	applied := filepath.Join(r.root, "applied")
	notApplied := func(what string) {
		t.Helper()
		last := r.log(1)[strings.LastIndex(r.log(1), "== ROUND"):]
		contains(t, what, last, "SKIP  apply orders plan (dry run: "+why)
		if _, err := os.Stat(applied); err == nil {
			t.Fatalf("%s: applied", what)
		}
	}
	r.swim("run", "1", "--plain", "--no-prompt")
	notApplied("signed")
	c := r.cmd(bin, "run", "1", "--plain", "--no-prompt")
	c.Env = append(c.Env, "ORDERS_ALLOW_APPLY=1")
	c.CombinedOutput()
	notApplied("FLAG=1")
	r.swim("run", "1", "--plain", "-y")
	notApplied("-y")

	out, code = r.swim("queue", "add", "1", "--approve", "ORDERS_ALLOW_APPLY")
	if code == 0 || !strings.Contains(out, "--approve ORDERS_ALLOW_APPLY: "+why) {
		t.Fatalf("queue --approve: exit %d\n%s", code, out)
	}

	out, _ = r.swim("doctor")
	contains(t, "doctor", out, "evidence-rule", "a require: rule for ORDERS_* says evidence: true")
}

// An evidence step is a lint error, so swim run refuses the lane until
// it's edited.
func TestEvidenceStepRemoved(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "show the plan", `stage check
evidence "tf plan" echo "Plan: 1 to destroy."`)
	out, code := r.swim("lint", "1")
	if code == 0 {
		t.Fatalf("lint passed:\n%s", out)
	}
	contains(t, "lint", out, "evidence steps are gone", "use snapshot (saves read-only output, without an approval binding)")
	out, code = r.swim("run", "1", "--plain")
	if code == 0 || !strings.Contains(out, "evidence steps are gone") {
		t.Fatalf("run: exit %d\n%s", code, out)
	}
}
