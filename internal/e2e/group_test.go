package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/status"
)

const ciGroup = `#!/usr/bin/env bash
# swim: syntax 2
# Round: CI
export GREETING=hello

# swim job 1.1 -----
# Round:  fmt
# Job:    job-fmt
# Always: yes
run "greet" bash -c 'echo "$GREETING from job $SWIM_CELL"'
gate "fmt" bash -c 'test ! -e fmtfail && touch fmt.done'

# swim job 1.2 -----
# Round:  lint
# Job:    job-lint
# Always: yes
gate "fmt first" test -e fmt.done
run "mark" touch lint.done

# swim job 1.3 -----
# Round:  e2e
# Job:    job-e2e
run "slow" bash -c 'sleep 1.5; touch e2e.done'
`

// A group file is one lane; its sub-jobs (1.1, 1.2, ...) run in file
// order, and the other lanes wait only for its Always ones.
func TestGroupLane(t *testing.T) {
	long(t)
	r := newRepo(t, "")
	if err := os.WriteFile(filepath.Join(r.root, "lanes.ci.sh"), []byte(ciGroup), 0o644); err != nil {
		t.Fatal(err)
	}
	// Lane 2 starts after setup (1.1, 1.2), alongside the slow 1.3.
	r.script(2, "a round", `gate "after setup, not after e2e" bash -c 'test -e lint.done && test ! -e e2e.done'`)
	jobLog := func(k string) string {
		data, _ := os.ReadFile(filepath.Join(r.root, ".swim", "logs", "agent1."+k+".log"))
		return string(data)
	}
	rounds := func(k string) int { return strings.Count(jobLog(k), "== ROUND ") }

	plan := r.mustSwim("plan")
	contains(t, "plan", plan, "[*] swim 1", "jobs, in order: 1.1 fmt [*] → 1.2 lint [*] → 1.3 e2e")

	out := r.mustSwim("all")
	for n := 1; n <= 2; n++ {
		if st := r.status().Get(n).State; st != status.Passed {
			t.Errorf("swim %d = %s\n%s", n, st, out)
		}
	}
	contains(t, "job 1.1 log", jobLog("1"), "hello from job 1")
	contains(t, "output", out, "[1.3]")

	// swim all again: the Always jobs run; 1.3 passed, so it doesn't.
	r.mustSwim("all")
	if rounds("1") != 2 || rounds("2") != 2 || rounds("3") != 1 {
		t.Errorf("second all: rounds 1.1=%d 1.2=%d 1.3=%d, want 2 2 1", rounds("1"), rounds("2"), rounds("3"))
	}
	// One job by name; then the whole lane.
	r.mustSwim("run", "1.3")
	if rounds("1") != 2 || rounds("3") != 2 {
		t.Errorf("swim run 1.3: rounds 1.1=%d 1.3=%d, want 2 2", rounds("1"), rounds("3"))
	}
	r.mustSwim("run", "1")
	if rounds("1") != 3 || rounds("2") != 3 || rounds("3") != 3 {
		t.Errorf("swim run 1: rounds %d %d %d, want 3 3 3", rounds("1"), rounds("2"), rounds("3"))
	}
	contains(t, "swim log 1.3", r.mustSwim("log", "1.3"), "slow")

	// The first failure skips the rest of the lane; lane 2 doesn't wait on it.
	os.WriteFile(filepath.Join(r.root, "fmtfail"), nil, 0o644)
	os.Remove(filepath.Join(r.root, "lint.done"))
	os.Remove(filepath.Join(r.root, "e2e.done"))
	if _, code := r.swim("all", "--rerun"); code != 1 {
		t.Fatalf("failing fmt: exit %d", code)
	}
	l := r.status().Get(1)
	if l.State != status.Failed || l.FindCell("2").State != status.Skipped || l.FindCell("3").State != status.Skipped {
		t.Errorf("after 1.1 failed: lane %s, 1.2 %s, 1.3 %s", l.State, l.FindCell("2").State, l.FindCell("3").State)
	}
	contains(t, "reason", l.FindCell("2").Reason, "an earlier job failed")
	os.Remove(filepath.Join(r.root, "fmtfail"))

	// Lint points at the group file's lines.
	src := strings.Replace(ciGroup, `gate "fmt first"`, "set -e\ngate \"fmt first\"", 1)
	os.WriteFile(filepath.Join(r.root, "lanes.ci.sh"), []byte(src), 0o644)
	lint, _ := r.swim("lint")
	contains(t, "lint", lint, "lanes.ci.sh:17", "swim 1.2: ")

	// The lane is its group file: swim new refuses it, and a lane.1.sh too
	// is refused before anything runs.
	if out, code := r.swim("new", "1", "other", "--force"); code == 0 || !strings.Contains(out, "lanes.ci.sh") {
		t.Errorf("new over a group lane: %d %s", code, out)
	}
	r.script(1, "also lane 1", `run "x" true`)
	if out, code := r.swim("all"); code == 0 || !strings.Contains(out, "lanes.ci.sh") {
		t.Errorf("a lane in a group file and a lane.1.sh: %d %s", code, out)
	}
}

// Any lane can be split into jobs, nested to any depth; a job id names it
// and every job under it, on the command line and in After:.
func TestNestedJobs(t *testing.T) {
	r := newRepo(t, "")
	r.write("lane.2.sh", `#!/usr/bin/env bash
# swim: syntax 2
# Round: build

# swim job 2.1
# Round: checks

# swim job 2.1.1
# Round: one
run "one" true

# swim job 2.1.2
# Round: two
run "two" bash -c 'test ! -e twofail'

# swim job 2.3
# Round: three
run "three" true
`, 0o755)
	r.script(3, "after 2.1", `run "x" true`)
	p := filepath.Join(r.root, "lane.3.sh")
	data, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(data), "\n", "\n# After: 2.1\n", 1)), 0o755)
	jobLog := func(k string) int {
		data, _ := os.ReadFile(filepath.Join(r.root, ".swim", "logs", "agent2."+k+".log"))
		return strings.Count(string(data), "== ROUND ")
	}

	contains(t, "plan", r.mustSwim("plan"), "jobs, in order: 2.1.1 one → 2.1.2 two → 2.3 three")
	r.mustSwim("run", "2.1")
	if jobLog("1.1") != 1 || jobLog("1.2") != 1 || jobLog("3") != 0 {
		t.Errorf("swim run 2.1: rounds 2.1.1=%d 2.1.2=%d 2.3=%d, want 1 1 0", jobLog("1.1"), jobLog("1.2"), jobLog("3"))
	}
	r.mustSwim("run", "2.1.2")
	if jobLog("1.1") != 1 || jobLog("1.2") != 2 {
		t.Errorf("swim run 2.1.2: rounds 2.1.1=%d 2.1.2=%d, want 1 2", jobLog("1.1"), jobLog("1.2"))
	}
	contains(t, "swim log 2.1", r.mustSwim("log", "2.1"), "2.1.1", "2.1.2", "passed")
	if out, code := r.swim("run", "2.4"); code == 0 || !strings.Contains(out, "no job 2.4") {
		t.Errorf("swim run 2.4: %d %s", code, out)
	}

	// After: 2.1 needs every job under 2.1 to have passed.
	r.mustSwim("run", "3")
	os.WriteFile(filepath.Join(r.root, "twofail"), nil, 0o644)
	r.swim("run", "2.1.2")
	out, _ := r.swim("run", "3")
	if l := r.status().Get(3); l.State != status.Skipped || !strings.Contains(l.Reason, "2.1.2 is failed") {
		t.Errorf("lane 3 after 2.1.2 failed: %s (%s)\n%s", l.State, l.Reason, out)
	}
}

// swim ci reports each job: its own section, annotations and JUnit suite.
func TestJobsInCI(t *testing.T) {
	r := newRepo(t, "")
	r.write("lane.1.sh", `#!/usr/bin/env bash
# swim: syntax 2
# Round: CI

# swim job 1.1
# Round: fmt
run "fmt ok" true

# swim job 1.2
# Round: lint
run "lint fails" false
`, 0o755)
	r.env = append(r.env, "GITHUB_ACTIONS=true")
	out, _ := r.swim("ci", "--junit", "swim.xml")
	contains(t, "ci output", out, "swim 1.1 PASS - fmt", "swim 1.2 FAIL - lint", "fmt ok", "::error title=swim 1.2%3A lint::")
	contains(t, "junit", string(mustRead(t, filepath.Join(r.root, "swim.xml"))), `name="swim 1.2: lint"`, `classname="swim.lane1.2.`)
}
