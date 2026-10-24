package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func exists(t *testing.T, r *repo, name string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(r.root, name))
	return err == nil
}

// swim plan --check runs snapshot and check for real, stops every round
// where change begins, never approves a guard, and leaves lane status alone;
// the plan shows each lane's result.
func TestPlanCheck(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "fetch", `stage snapshot
snapshot "state" echo hi
stage check
run "probe" touch "$SWIM_ROOT/probed"
stage change
run "the change" touch "$SWIM_ROOT/changed"
stage verify
run "verify" touch "$SWIM_ROOT/verified"`)
	r.script(2, "cutover", `stage check
gate "DeletionPolicy is Retain" false
stage change
run "x" touch "$SWIM_ROOT/x"`)
	withHeader(t, r, 2, "# After: 1")
	r.script(3, "unstaged", `run "something" touch "$SWIM_ROOT/unstaged"`)
	r.script(4, "early guard", `stage check
if guard ALLOW_EARLY "an early change"; then run "early" touch "$SWIM_ROOT/early"; fi
stage change`)
	withHeader(t, r, 4, "# After: 1")
	r.env = append(r.env, "ALLOW_EARLY=1")

	out, code := r.swim("plan", "--check")
	if code != 1 {
		t.Errorf("exit %d, want 1 (a check stopped)", code)
	}
	contains(t, "plan", out, "swim 1", "check PASS", `stops at gate "DeletionPolicy is Retain"`, "cannot check: no stages",
		"check ~ (depends on unrun changes in 1)", "Checks: 1 would stop at a gate.")
	if !exists(t, r, "probed") {
		t.Error("the check stage didn't run")
	}
	for _, name := range []string{"changed", "verified", "x", "unstaged", "early"} {
		if exists(t, r, name) {
			t.Errorf("%s: a check ran past where change begins (or approved a guard)", name)
		}
	}
	contains(t, "check log", readFile(t, r, ".swim/logs/agent1.check.log"), "check: read-only preview", "PASS  probe", "CHECKED  stopped before stage change", "== END CHECKED")
	contains(t, "guard in a check", readFile(t, r, ".swim/logs/agent4.check.log"), "SKIP  an early change (dry run: swim plan --check never approves")
	if r.log(1) != "" {
		t.Error("the check wrote to the round log")
	}
	st := r.status()
	if l := st.Get(1); l.State == "passed" || l.Pass != 0 || l.CheckResult != "checked" || l.CheckSHA == "" {
		t.Errorf("lane 1 status: %+v", l)
	}
	if l := st.Get(2); l.CheckResult != "stopped" || l.CheckStop != "DeletionPolicy is Retain" {
		t.Errorf("lane 2 status: %+v", l)
	}
	if l := st.Get(4); strings.Join(l.CheckGuards, ",") != "ALLOW_EARLY" {
		t.Errorf("lane 4 check guards %v", l.CheckGuards)
	}
	contains(t, "plan --yaml", r.mustSwim("plan", "--yaml"), "check:", "result: checked", "stopped_at: DeletionPolicy is Retain")

	// The script changed since: the plan says the check is stale.
	withHeader(t, r, 1, "# Owner: someone")
	contains(t, "stale", r.mustSwim("plan", "1"), "check PASS (stale: the script changed since)")
	// And swim all still runs lane 1: a check never makes a lane done.
	contains(t, "swim all", must(r.swim("all", "--plain")), "swim 1")
	if !exists(t, r, "changed") {
		t.Error("swim all after a check didn't run the change")
	}
}

// A lane in another language stops at swim stage change too.
func TestPlanCheckWrapped(t *testing.T) {
	r := newRepo(t, "")
	r.write("lane.1.sh", "#!/usr/bin/env bash\n# Round: wrapped\nswim stage check\nswim run \"probe\" -- touch \"$SWIM_ROOT/probed\"\nswim stage change\ntouch \"$SWIM_ROOT/changed\"\nsleep 2\n", 0o755)
	r.mustSwim("plan", "--check")
	if !exists(t, r, "probed") || exists(t, r, "changed") {
		t.Errorf("probed=%v changed=%v", exists(t, r, "probed"), exists(t, r, "changed"))
	}
	contains(t, "check log", readFile(t, r, ".swim/logs/agent1.check.log"), "CHECKED  stopped before stage change", "== END CHECKED")
}

// Lint warns about a change before stage change; swim ci --check previews.
func TestPlanCheckLintAndCI(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "unsafe", `stage check
run "apply too early" terraform apply -auto-approve
stage change`)
	contains(t, "lint", must(r.swim("lint", "1")), `"terraform apply" runs before`, "[check-unsafe]")
	r.script(1, "ok", `stage check
run "probe" touch "$SWIM_ROOT/probed"
stage change
run "change" touch "$SWIM_ROOT/changed"`)
	r.env = append(r.env, "CI=true", "GITHUB_ACTIONS=true")
	r.swim("ci", "--check", "--provider", "generic")
	if !exists(t, r, "probed") || exists(t, r, "changed") {
		t.Errorf("ci --check: probed=%v changed=%v", exists(t, r, "probed"), exists(t, r, "changed"))
	}
}
