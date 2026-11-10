package e2e

import (
	"os/exec"
	"strings"
	"testing"
)

// Bare swim in a swim repo says what needs you, with the command for each:
// a failed round nobody has edited, a lane lint refuses, a batch no
// swimmer will take; what's ready to queue; and nothing else. Outside a
// swim repo it's the help.
func TestBareSwimSaysWhatNeedsYou(t *testing.T) {
	r := newRepo(t)
	if out := r.mustSwim(); !strings.Contains(out, "nothing needs you") {
		t.Errorf("an empty repo:\n%s", out)
	}

	r.shortLane(1, "# Round: fails", "stage check\nstep \"x\" false")
	r.shortLane(2, "# Round: lint refuses it", "stage check\nstep echo hi")
	r.shortLane(3, "# Round: ready", "stage check\nstep \"x\" true")
	r.shortLane(4, "# Round: queued", "stage check\nstep \"x\" true")
	r.swim("run", "1", "--plain")
	r.mustSwim("queue", "4")

	out := r.mustSwim()
	for _, want := range []string{
		"needs you",
		"lane 1   failed exit 1 · fails  → swim log 1 --failed",
		"lane 2   won't run: 1 lint error · lint refuses it  → swim lint 2",
		"lane 4: queued, no watcher up  → swim watch",
		"ready\n  lane 3  → swim queue 3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("bare swim lacks %q:\n%s", want, out)
		}
	}

	// Once its script is edited, the failed round is history: lane 1 is
	// ready again, not a failure that needs you.
	r.shortLane(1, "# Round: fixed", "stage check\nstep \"x\" true")
	if out := r.mustSwim(); strings.Contains(out, "lane 1   FAIL") || !strings.Contains(out, "lane 1,3  → swim queue 1 3") {
		t.Errorf("an edited lane still shows its failure:\n%s", out)
	}

	// Outside a swim repo, bare swim is the help.
	plain := t.TempDir()
	if err := exec.Command("git", "init", "-q", plain).Run(); err != nil {
		t.Fatal(err)
	}
	c := r.cmd(bin)
	c.Dir = plain
	help, _ := c.CombinedOutput()
	if !strings.Contains(string(help), "USAGE") || strings.Contains(string(help), "swim · ") {
		t.Errorf("bare swim outside a swim repo:\n%s", help)
	}
}
