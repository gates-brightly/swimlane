package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/launcher"
)

// read is a repo file's contents ("" if missing).
func (r *repo) read(name string) string {
	data, _ := os.ReadFile(filepath.Join(r.root, name))
	return string(data)
}

// decisionLane is lane n with one decision of three choices and a step
// after it; r.script's lint --fix declares it (# Decisions:).
func decisionLane(r *repo, n int) {
	r.script(n, "roll out", `stage change
decide ROLLOUT "How should it roll out?" "canary" "all at once" "don't deploy"
case $DECISION in
  1) run "canary" true ;;
  2) run "all" true ;;
  3) stop "not deploying" ;;
esac
run "after" true`)
}

// NAME=N where swim run starts answers a decision: that branch runs, the
// log, status, history and chain say so, and verify accepts it.
func TestDecisionByFlag(t *testing.T) {
	r := newRepo(t, "")
	decisionLane(r, 1)
	if !strings.Contains(r.read("lane.1.sh"), "# Decisions: ROLLOUT  How should it roll out?") {
		t.Fatalf("lint --fix didn't declare the decision:\n%s", r.read("lane.1.sh"))
	}
	contains(t, "status, pending", r.mustSwim("status", "1"), "decision ROLLOUT  How should it roll out?", "swim run will ask")
	out, code := r.swimEnv([]string{"ROLLOUT=2"}, "run", "1", "--plain")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	log := r.log(1)
	contains(t, "log", log, "DECIDED  ROLLOUT: 2. all at once (ROLLOUT=2)", "PASS  all", "PASS  after")
	if strings.Contains(log, "PASS  canary") {
		t.Errorf("another branch ran:\n%s", log)
	}
	contains(t, "status", r.mustSwim("status", "1"), "decided ROLLOUT: 2. all at once (ROLLOUT=2)")
	contains(t, "history", r.mustSwim("history"), "decide ROLLOUT=2 for swim 1 by", "via flag", "Chosen:", "all at once", "1 decision")
	contains(t, "verify", r.mustSwim("verify", "--allow-unsigned"), "VERIFIED")
	contains(t, "project log", r.read(".swim.log"), "decide", "(ROLLOUT=2, by ")
}

// With nobody to ask, a decision is unanswered: the round ends there,
// SKIP (no branch, nothing after it), swim run exits 3, and a lane that
// waits on it doesn't start.
func TestDecisionUnanswered(t *testing.T) {
	r := newRepo(t, "")
	decisionLane(r, 1)
	r.script(2, "after the rollout", `run "smoke" true`)
	withHeader(t, r, 2, "# After: 1")
	out, code := r.swim("all", "--plain", "--no-prompt")
	if code != launcher.ExitDryRun {
		t.Fatalf("exit %d, want %d\n%s", code, launcher.ExitDryRun, out)
	}
	log := r.log(1)
	contains(t, "log", log, "SKIP  How should it roll out? (dry run: decision needed: ROLLOUT; nobody here to ask (set ROLLOUT=1..3))", "== END SKIP")
	for _, ran := range []string{"PASS  canary", "PASS  all", "STOP  not deploying", "PASS  after"} {
		if strings.Contains(log, ran) {
			t.Errorf("%q ran with the decision unanswered:\n%s", ran, log)
		}
	}
	contains(t, "summary", out, "swim 1 skipped")
	if strings.Contains(r.log(2), "PASS  smoke") {
		t.Error("a lane after the unanswered one ran")
	}
}

// A value that isn't one of the choices, or one the lane set itself, is
// refused: unanswered, not clamped or guessed.
func TestDecisionRefusals(t *testing.T) {
	r := newRepo(t, "")
	decisionLane(r, 1)
	out, code := r.swimEnv([]string{"ROLLOUT=4"}, "run", "1", "--plain")
	if code != launcher.ExitDryRun || !strings.Contains(out, "ROLLOUT=4 isn't one of the choices (1-3)") {
		t.Fatalf("ROLLOUT=4: exit %d\n%s", code, out)
	}
	r.script(1, "roll out", `stage change
export ROLLOUT=1
decide ROLLOUT "How should it roll out?" "canary" "all at once"
case $DECISION in 1) run "canary" true ;; esac`)
	out, code = r.swim("run", "1", "--plain", "--no-prompt")
	if code != launcher.ExitDryRun || !strings.Contains(out, "ROLLOUT=1 was set inside the lane") {
		t.Fatalf("a lane's own ROLLOUT=1: exit %d\n%s", code, out)
	}
	if strings.Contains(r.log(1), "PASS  canary") {
		t.Error("the lane decided for itself")
	}
	// Nor can it write the answer into its log.
	if out, code := r.swim("_mark", "1", "DECIDED", "ROLLOUT: 1. canary"); code == 0 || !strings.Contains(out, "a lane can't decide for itself") {
		t.Errorf("_mark DECIDED: %d %s", code, out)
	}
}

// lint: an undeclared decision is an error (--fix declares it), and so is
// decide in a subshell or with fewer than two choices.
func TestDecisionLint(t *testing.T) {
	r := newRepo(t, "")
	r.write("lane.1.sh", `#!/usr/bin/env bash
# swim: syntax 4
# Round: roll out
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 1
stage change
decide ROLLOUT "How?" "canary" "all at once"
x=$(decide OTHER "Which?" "a" "b")
decide ONE "Only one?" "a"
summary
`, 0o755)
	out, code := r.swim("lint", "1")
	if code == 0 {
		t.Fatalf("lint passed:\n%s", out)
	}
	contains(t, "lint", out, "[decision-unlisted]", "decision ROLLOUT is asked but not declared", "[decision-subshell]", "[decision-choices]", "decide ONE offers 1 choices")
	if out, code := r.swim("run", "1", "--plain", "--no-prompt"); code == 0 {
		t.Errorf("swim run ran a lane with lint errors:\n%s", out)
	}
	fixed, _ := r.swim("lint", "--fix", "1") // the subshell and the one choice are still errors
	contains(t, "lint --fix", fixed, "# Decisions: ROLLOUT  How?")
	contains(t, "declared", r.read("lane.1.sh"), "# Decisions: ROLLOUT  How?")
}

// A queued batch answers with --decide; one that doesn't leaves the
// decision unanswered (nobody watches a queue), whatever the worker's
// environment says.
func TestDecisionQueued(t *testing.T) {
	r := newRepo(t, "")
	decisionLane(r, 1)
	if out, code := r.swim("queue", "add", "1", "--decide", "NOPE=1"); code == 0 || !strings.Contains(out, "no lane in this batch declares a decision NOPE") {
		t.Errorf("--decide NOPE=1: %d %s", code, out)
	}
	if out, code := r.swim("queue", "add", "1", "--decide", "ROLLOUT=0"); code == 0 || !strings.Contains(out, "N is the number of a choice") {
		t.Errorf("--decide ROLLOUT=0: %d %s", code, out)
	}
	contains(t, "queued", r.mustSwim("queue", "add", "1", "--decide", "ROLLOUT=1"), "decisions: ROLLOUT=1")
	r.swim("watch", "--once", "--no-tui")
	contains(t, "log", r.log(1), "DECIDED  ROLLOUT: 1. canary (ROLLOUT=1, decided in queue ", "PASS  canary")
	contains(t, "history", r.mustSwim("history", "--oneline"), "decide ROLLOUT=1 for swim 1 by", "via queue")

	r.mustSwim("queue", "add", "1")
	out, _ := r.swimEnv([]string{"ROLLOUT=2"}, "watch", "--once", "--no-tui")
	if strings.Contains(r.log(1), "PASS  all") {
		t.Errorf("the worker's own ROLLOUT=2 answered a batch that doesn't:\n%s", out)
	}
	contains(t, "unanswered in the queue", r.log(1), "decision needed: ROLLOUT")
}

// Run directly with bash, the operator's own NAME=N answers, and a
// decision the header doesn't declare is refused.
func TestDecisionDirect(t *testing.T) {
	r := newRepo(t, "")
	decisionLane(r, 1)
	c := r.cmd("bash", "lane.1.sh")
	c.Env = append(c.Env, "ROLLOUT=3")
	c.CombinedOutput()
	contains(t, "direct", r.log(1), "DECIDED  ROLLOUT: 3. don't deploy (ROLLOUT=3)", "STOP  not deploying")
	data := strings.Replace(r.read("lane.1.sh"), "# Decisions: ROLLOUT  How should it roll out?\n", "", 1)
	os.WriteFile(filepath.Join(r.root, "lane.1.sh"), []byte(data), 0o755)
	c = r.cmd("bash", "lane.1.sh")
	c.Env = append(c.Env, "ROLLOUT=1")
	c.CombinedOutput()
	contains(t, "undeclared", r.log(1), "ROLLOUT isn't declared in lane.1.sh (list it in # Decisions:)")
}
