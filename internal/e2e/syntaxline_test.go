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
	// Queued, the swimmer refuses it too: it never runs.
	r.mustSwim("queue", "2")
	r.swim("watch", "--once")
	if strings.Contains(r.log(2), "| lane-two") {
		t.Fatalf("a queued lane with no syntax line ran:\n%s", r.log(2))
	}
	if !strings.Contains(r.log(2), "no `# swim: syntax` line") {
		t.Errorf("the queued lane's log doesn't say why it was refused:\n%s", r.log(2))
	}
}
