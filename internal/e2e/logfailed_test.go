package e2e

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// swim log N --failed shows only what a fix needs: one step of five fails,
// and its output is there in full (repeated lines unfolded), with its
// stage and the step before it; no PASS line, the passing steps counted.
// --yaml keeps only the failed results. A passing round has no failed
// steps, and says so with exit 0.
func TestLogFailed(t *testing.T) {
	r := newRepo(t)
	r.shortLane(1, "# Round: five steps", `stage check
step "one" echo one-out
step "two" echo two-out
stage verify
step "three" bash -c 'echo broke-here; for i in 1 2 3 4 5 6; do echo same-line; done; exit 3'
step "four" echo four-out
step "five" true`)
	r.swim("run", "1", "--plain")

	out := r.mustSwim("log", "1", "--failed")
	contains(t, "--failed", out, "== ROUND", "five steps", "-- stage verify", "   before it: two (PASS)",
		"  FAIL  three (exit 3)", "        | broke-here", "   (4 passed steps not shown)", "== END FAIL")
	if n := strings.Count(out, "        | same-line"); n != 6 {
		t.Errorf("the failed step's output isn't in full (%d of 6 repeated lines):\n%s", n, out)
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "  PASS  ") || strings.Contains(l, "one-out") || strings.Contains(l, "four-out") {
			t.Errorf("--failed shows a passing step: %q\n%s", l, out)
		}
	}
	if strings.Contains(out, "-- stage check") {
		t.Errorf("--failed shows a stage nothing failed in:\n%s", out)
	}

	var d struct {
		Counts  struct{ Pass int }
		Results []struct{ Result, Label, Before string }
	}
	if err := yaml.Unmarshal([]byte(r.mustSwim("log", "1", "--failed", "--yaml")), &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Results) != 1 || d.Results[0].Label != "three" || d.Results[0].Before != "two" || d.Counts.Pass != 4 {
		t.Errorf("--failed --yaml: %+v", d)
	}

	r.shortLane(2, "# Round: all pass", `stage check
step "fine" true`)
	r.swim("run", "2", "--plain")
	out, code := r.swim("log", "2", "--failed")
	if code != 0 || !strings.Contains(out, "no failed steps (lane 2: all pass)") {
		t.Errorf("a passing round: exit %d\n%s", code, out)
	}
}
