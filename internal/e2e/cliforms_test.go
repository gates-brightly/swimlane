package e2e

import (
	"strings"
	"testing"
)

// What people type first, read as what it means: a lane script's path, a
// comma list, several lanes to status, a duration to --since; and --json
// and --dry-run say what swim does instead.
func TestCLIFormsPeopleType(t *testing.T) {
	r := newRepo(t, "")
	for _, n := range []int{2, 3, 17} {
		r.shortLane(n, "# Round: lane form", "stage check\nrun \"x\" echo hi")
	}
	if out, code := r.swim("lint", "lane.17.sh"); code != 0 || !strings.Contains(out, "no problems found") {
		t.Errorf("lint lane.17.sh: exit %d\n%s", code, out)
	}
	out, code := r.swim("status", "2", "17")
	if code != 0 || !strings.Contains(out, "swim 2 ") || !strings.Contains(out, "swim 17") || strings.Contains(out, "swim 3 ") {
		t.Errorf("status 2 17: exit %d\n%s", code, out)
	}
	if out, code := r.swim("queue", "2,3"); code != 0 || !strings.Contains(out, "2 lanes") {
		t.Errorf("queue 2,3: exit %d\n%s", code, out)
	}
	if out, code := r.swim("report", "--since", "1h"); code != 0 || !strings.Contains(out, "(--since 1h)") {
		t.Errorf("report --since 1h: exit %d\n%s", code, out)
	}
	if out, _ := r.swim("status", "--json"); !strings.Contains(out, "--yaml") {
		t.Errorf("status --json doesn't point at --yaml:\n%s", out)
	}
	if out, _ := r.swim("run", "2", "--dry-run"); !strings.Contains(out, "already a dry run") {
		t.Errorf("run --dry-run doesn't explain:\n%s", out)
	}
}
