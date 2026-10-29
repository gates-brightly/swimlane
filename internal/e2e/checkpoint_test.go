package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/checkpoint"
)

// countLane is lane 1: n checkpointed steps that each count their runs in
// dir (outside the repo, so the staged copy doesn't matter), then a step
// whose exit code is last's, then one more.
func countLane(r *repo, dir string, n int, last string) {
	var b strings.Builder
	b.WriteString("stage change\n")
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "run --checkpoint --key step-%d \"step %d\" bash -c 'echo ran >> %s/step-%d'\n", i, i, dir, i)
	}
	fmt.Fprintf(&b, "run \"point the api at v2\" bash -c 'exit %s'\n", last)
	b.WriteString(`run "announce" true`)
	r.script(1, "migrate orders to v2", b.String())
}

// runs is how many times each checkpointed step ran, in all.
func runs(t *testing.T, dir string, n int) []int {
	t.Helper()
	out := make([]int, n)
	for i := 1; i <= n; i++ {
		data, _ := os.ReadFile(filepath.Join(dir, fmt.Sprintf("step-%d", i)))
		out[i-1] = strings.Count(string(data), "ran")
	}
	return out
}

// The headline: a 20-step lane fails at step 19; fixed and run again, it
// keeps the 18 checkpointed steps and runs 19 and 20. Each ran once.
func TestCheckpointResumesAfterAFailure(t *testing.T) {
	r := newRepo(t, "")
	dir := t.TempDir()
	countLane(r, dir, 18, "1")
	if out, code := r.swim("run", "1", "--plain"); code != 1 {
		t.Fatalf("first run: exit %d, want 1\n%s", code, out)
	}
	contains(t, "status after the failure", r.mustSwim("status", "1"), "checkpoints: the next run keeps 18 steps")
	countLane(r, dir, 18, "0") // the fix: the script changed, the round didn't
	out, code := r.swim("run", "1", "--plain")
	if code != 0 {
		t.Fatalf("after the fix: exit %d\n%s", code, out)
	}
	log := r.log(1)
	contains(t, "log", log, "KEPT  step 1 (passed in run r-", "KEPT  step 18 (passed in run r-", "PASS  point the api at v2", "PASS  announce", "kept=18")
	contains(t, "summary", out, "kept=18")
	for i, n := range runs(t, dir, 18) {
		if n != 1 {
			t.Errorf("step %d ran %d times, want once", i+1, n)
		}
	}
	contains(t, "verify", r.mustSwim("verify", "--allow-unsigned"), "VERIFIED")
	y := r.mustSwim("status", "1", "--yaml")
	contains(t, "status --yaml", y, "kept: 18", "checkpoints: [step-1,")

	// --fresh runs every one again.
	r.mustSwim("run", "1", "--plain", "--fresh")
	for i, n := range runs(t, dir, 18) {
		if n != 2 {
			t.Errorf("--fresh: step %d ran %d times in all, want 2", i+1, n)
		}
	}
}

// A checkpoint is kept only for the same command; swim checkpoint rm
// forgets one; a new round starts with none; a kept gate doesn't stop.
func TestCheckpointWhatIsKept(t *testing.T) {
	r := newRepo(t, "")
	dir := t.TempDir()
	countLane(r, dir, 3, "0")
	r.mustSwim("run", "1", "--plain")
	contains(t, "ls", r.mustSwim("checkpoint", "ls", "1"), "swim 1: 3 checkpoints (the next run keeps them", "step-1", "step-3")

	// step 2's command changes: it runs again; the others are kept.
	r.script(1, "migrate orders to v2", strings.Replace(scriptBody(r), "step-2'", "step-2; true'", 1))
	r.mustSwim("run", "1", "--plain")
	if got := runs(t, dir, 3); fmt.Sprint(got) != "[1 2 1]" {
		t.Errorf("after step 2 changed: runs %v, want [1 2 1]", got)
	}

	contains(t, "rm", r.mustSwim("checkpoint", "rm", "1", "step-3"), "forgot 1 checkpoint")
	r.mustSwim("run", "1", "--plain")
	if got := runs(t, dir, 3); fmt.Sprint(got) != "[1 2 2]" {
		t.Errorf("after rm step-3: runs %v, want [1 2 2]", got)
	}

	// A new round: nothing kept.
	r.script(1, "the next migration", scriptBody(r))
	r.mustSwim("run", "1", "--plain")
	if got := runs(t, dir, 3); fmt.Sprint(got) != "[2 3 3]" {
		t.Errorf("a new round: runs %v, want [2 3 3]", got)
	}

	// A kept gate counts as passed: the round goes on.
	r.script(2, "gated", `stage change
gate --checkpoint "made the bucket" true
run "after" true`)
	r.mustSwim("run", "2", "--plain")
	r.mustSwim("run", "2", "--plain")
	contains(t, "kept gate", r.log(2), "KEPT  made the bucket", "PASS  after")
}

// scriptBody is lane 1's script from its first stage line to before
// summary, to rewrite it with r.script.
func scriptBody(r *repo) string {
	s := r.read("lane.1.sh")
	s = s[strings.Index(s, "stage change"):]
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "summary"))
}

// A checkpoint file can't make a step skip unseen: one forged to name a
// run where the step failed is kept, but swim verify says it's BROKEN.
func TestCheckpointForgedIsBroken(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "two steps", `stage change
run --checkpoint --key a "step a" true
run --checkpoint --key b "step b" bash -c 'exit 1'`)
	r.swim("run", "1", "--plain")
	f, err := checkpoint.Load(r.root, 1)
	if err != nil || len(f.Steps) != 1 {
		t.Fatalf("checkpoints after one pass, one failure: %+v %v", f, err)
	}
	// Claim b passed too, with the command hash b runs with.
	a := f.Steps["a"]
	r.script(1, "two steps", `stage change
run --checkpoint --key a "step a" true
run --checkpoint --key b "step b" true`)
	b := a
	b.Label = "step b"
	b.Cmd = checkpoint.CmdHash(t.TempDir(), []string{"true"})
	f.Steps["b"] = b
	data, _ := json.Marshal(f)
	os.WriteFile(checkpoint.Path(r.root, 1), data, 0o644)
	r.swim("run", "1", "--plain")
	contains(t, "log", r.log(1), "KEPT  step b")
	out, code := r.swim("verify", "--allow-unsigned")
	if code == 0 || !strings.Contains(out, `kept "b" from run `+a.Run+`, but that run's log has no pass of it with the same command`) {
		t.Fatalf("a forged checkpoint verified: exit %d\n%s", code, out)
	}
}

// lint: a checkpointed snapshot and two steps with one key are errors; one
// before stage change, and a label that expands without --key, warnings.
func TestCheckpointLint(t *testing.T) {
	r := newRepo(t, "")
	r.write("lane.1.sh", `#!/usr/bin/env bash
# swim: syntax 4
# Round: checkpoints
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 1
stage snapshot
snapshot --checkpoint "state" true
stage check
run --checkpoint "a check" true
stage change
run --checkpoint --key one "make it" true
run --checkpoint --key one "make it again" true
run --checkpoint "deploy $VERSION" true
summary
`, 0o755)
	out, _ := r.swim("lint", "1")
	contains(t, "lint", out, `snapshot "state" is marked --checkpoint`, `two checkpointed steps have the key "one"`,
		`run "a check" is a checkpoint before `+"`stage change`", "has a label that expands, and no --key")
}
