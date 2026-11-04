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
	r := newRepo(t, "")
	r.shortLane(1, "# Round: good", "stage check\nrun \"x\" echo lane-one")
	two := "#!/usr/bin/env swim\n# Round: no syntax line\n\nstage check\nrun \"x\" echo lane-two\n"
	if err := os.WriteFile(filepath.Join(r.root, "lane.2.sh"), []byte(two), 0o755); err != nil {
		t.Fatal(err)
	}
	const fix = "add `# swim: syntax 4` under"

	out, code := r.swim("lint")
	if code == 0 || !strings.Contains(out, "lane.2.sh:1  error") || !strings.Contains(out, fix) || strings.Contains(out, "migrates") {
		t.Fatalf("lint: exit %d\n%s", code, out)
	}
	if out, code := r.swim("status"); code != 0 {
		t.Fatalf("status refused over lane 2: %s", out)
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
	r := newRepo(t, "")
	r.shortLane(1, "# Round: fine", "stage check\nrun \"x\" echo ok")
	r.shortLane(2, "# Round: label for a command", "stage check\nrun echo hi")
	nosyntax := "#!/usr/bin/env swim\n# Round: no syntax line\n\nstage check\nrun \"x\" echo hi\n"
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
