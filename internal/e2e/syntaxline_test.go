package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A lane script with no syntax line stops only its own lane, and lint, run
// and the queue say the same thing about it: add the line.
func TestMissingSyntaxLineStopsOnlyItsLane(t *testing.T) {
	r := newRepo(t)
	r.shortLane(1, "# Round: good", "stage check\nstep \"x\" echo lane-one")
	two := "#!/usr/bin/env swim\n# Round: no syntax line\n\nstage check\nstep \"x\" echo lane-two\n"
	if err := os.WriteFile(filepath.Join(r.root, "lane.2.sh"), []byte(two), 0o755); err != nil {
		t.Fatal(err)
	}
	const fix = "add `# swim: syntax 4` under"

	out, code := r.swim("lint")
	if code == 0 || !strings.Contains(out, "lane.2.sh:1  error") || !strings.Contains(out, fix) || strings.Contains(out, "migrates") {
		t.Fatalf("lint: exit %d\n%s", code, out)
	}
	if out, code := r.swim("status"); code != 0 || !strings.Contains(out, "won't run: 1 lint error (swim lint 2)") || !strings.Contains(out, "[syntax]") {
		t.Fatalf("status doesn't say lane 2 won't run: exit %d\n%s", code, out)
	}
	if out, code := r.swim("run", "1", "--plain"); code != 0 || !strings.Contains(r.log(1), "| lane-one") {
		t.Fatalf("run 1 refused over lane 2: exit %d\n%s", code, out)
	}
	if out, code := r.swim("run", "2", "--plain"); code == 0 || !strings.Contains(out, fix) {
		t.Fatalf("run 2: exit %d\n%s", code, out)
	}
	// It can't be queued either (the launcher would refuse it too).
	if out, code := r.swim("queue", "2"); code == 0 || !strings.Contains(out, fix) {
		t.Fatalf("queue 2: exit %d\n%s", code, out)
	}
}

// swim queue refuses what swim run would, at enqueue, while whoever
// queues it can fix it: nothing is queued.
func TestQueueRefusesWhatRunWould(t *testing.T) {
	r := newRepo(t)
	r.shortLane(1, "# Round: fine", "stage check\nstep \"x\" echo ok")
	r.shortLane(2, "# Round: label for a command", "stage check\nstep echo hi")
	nosyntax := "#!/usr/bin/env swim\n# Round: no syntax line\n\nstage check\nstep \"x\" echo hi\n"
	os.WriteFile(filepath.Join(r.root, "lane.3.sh"), []byte(nosyntax), 0o755)
	for _, n := range []string{"2", "3", "1,2"} {
		out, code := r.swim("queue", n)
		if code == 0 || !strings.Contains(out, "not queued: swim run would refuse it") {
			t.Errorf("queue %s: exit %d\n%s", n, code, out)
		}
	}
	if q := r.mustSwim("queue"); strings.Contains(q, "#1") {
		t.Errorf("something was queued:\n%s", q)
	}
	if out, code := r.swim("queue", "1"); code != 0 || !strings.Contains(out, "queued #1") {
		t.Errorf("queue 1, which lints clean: exit %d\n%s", code, out)
	}
}

// swim all skips a lane lint refuses and runs the rest, saying so and
// exiting non-zero; a lane waiting on the skipped one skips too. Named,
// the same lane refuses the run.
func TestRunAllSkipsALaneLintRefuses(t *testing.T) {
	r := newRepo(t)
	r.shortLane(1, "# Round: fine", "stage check\nstep \"x\" echo lane-one")
	nosyntax := "#!/usr/bin/env swim\n# Round: no syntax line\n\nstage check\nstep \"x\" echo lane-two\n"
	os.WriteFile(filepath.Join(r.root, "lane.2.sh"), []byte(nosyntax), 0o755)
	r.shortLane(3, "# Round: after two\n# After: 2", "stage check\nstep \"x\" echo lane-three")
	out, code := r.swim("all", "--plain")
	if code == 0 || !strings.Contains(out, "not running swim 2") {
		t.Fatalf("swim all: exit %d\n%s", code, out)
	}
	if !strings.Contains(r.log(1), "| lane-one") {
		t.Errorf("lane 1 didn't run:\n%s\n%s", out, r.log(1))
	}
	if strings.Contains(r.log(2), "lane-two") || strings.Contains(r.log(3), "| lane-three") {
		t.Errorf("a refused lane, or one waiting on it, ran:\n%s\n%s", r.log(2), r.log(3))
	}
	if out, code := r.swim("run", "1", "2", "--plain"); code == 0 || !strings.Contains(out, "refusing to start") {
		t.Errorf("run 1 2: exit %d\n%s", code, out)
	}
}

// Queueing a lane that waits on a lane lint refuses warns, naming that
// lane's lint error; when the batch skips, the hint says to fix that lane,
// not to run it (swim run would refuse it too).
func TestQueueWarnsOfARefusedDependency(t *testing.T) {
	r := newRepo(t)
	r.shortLane(1, "# Round: setup", "stage check\nstep echo hi")
	r.shortLane(2, "# Round: after setup\n# After: 1", "stage check\nstep \"x\" echo lane-two")
	// status says swim 1 won't run, and why, whatever the lint error.
	if st := must(r.swim("status", "1")); !strings.Contains(st, "won't run: 1 lint error (swim lint 1)") || !strings.Contains(st, "[step]") {
		t.Errorf("status 1:\n%s", st)
	}
	if y := must(r.swim("status", "1", "--yaml")); !strings.Contains(y, "lint_errors: 1") {
		t.Errorf("status 1 --yaml:\n%s", y)
	}
	if st := must(r.swim("status", "2")); strings.Contains(st, "won't run") {
		t.Errorf("status 2 says a clean lane won't run:\n%s", st)
	}
	out, code := r.swim("queue", "2")
	if code != 0 || !strings.Contains(out, "swim 2 waits on swim 1, which swim lint refuses") || !strings.Contains(out, "[step]") {
		t.Fatalf("queue 2: exit %d\n%s", code, out)
	}
	r.swim("watch", "--once")
	if strings.Contains(r.log(2), "| lane-two") {
		t.Fatalf("lane 2 ran past a lane that can't pass:\n%s", r.log(2))
	}
	if st := must(r.swim("status", "2")); !strings.Contains(st, "swim lint refuses it") || strings.Contains(st, "run it too") {
		t.Errorf("the skip hint should say to fix swim 1:\n%s", st)
	}
}
