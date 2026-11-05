package e2e

import (
	"strings"
	"testing"
)

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
