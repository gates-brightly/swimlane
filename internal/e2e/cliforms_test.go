package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What people type first, read as what it means: a lane script's path, a
// comma list, several lanes to status, a duration to --since; and --json
// and --dry-run say what swim does instead.
func TestCLIFormsPeopleType(t *testing.T) {
	r := newRepo(t)
	for _, n := range []int{2, 3, 17} {
		r.shortLane(n, "# Round: lane form", "stage check\nrun \"x\" echo hi")
	}
	if out, code := r.swim("lint", "lane.17.sh"); code != 0 || !strings.Contains(out, "no problems found") {
		t.Errorf("lint lane.17.sh: exit %d\n%s", code, out)
	}
	// Another repo's lane, or any file elsewhere, is never this repo's lane
	// of the same number.
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, "lane.17.sh"), []byte("#!/usr/bin/env swim\n# swim: syntax 4\n# Round: someone else's\n"), 0o755)
	for _, args := range [][]string{
		{"lint", filepath.Join(other, "lane.17.sh")},
		{"status", "/etc/lane.17.sh"},
		{"run", filepath.Join(other, "lane.17.sh"), "--plain"},
		{"queue", filepath.Join(other, "lane.17.sh")},
		{"log", filepath.Join(other, "lane.17.sh")},
	} {
		if out, code := r.swim(args...); code == 0 || !strings.Contains(out, "isn't one of this repo's lanes") {
			t.Errorf("swim %v: exit %d\n%s", args, code, out)
		}
	}
	if strings.Contains(r.log(17), "  PASS  ") {
		t.Errorf("this repo's lane 17 ran for another repo's path:\n%s", r.log(17))
	}
	// A symlink to this repo's root is this repo.
	link := filepath.Join(t.TempDir(), "here")
	os.Symlink(r.root, link)
	if out, code := r.swim("lint", filepath.Join(link, "lane.17.sh")); code != 0 {
		t.Errorf("lint through a symlink to the repo: exit %d\n%s", code, out)
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
	if out, code := r.swim("watch", "--plain", "--once"); code != 0 {
		t.Errorf("watch --plain --once: exit %d\n%s", code, out)
	}
	if out, _ := r.swim("run", "2", "--dry-run"); !strings.Contains(out, "already a dry run") {
		t.Errorf("run --dry-run doesn't explain:\n%s", out)
	}
}
