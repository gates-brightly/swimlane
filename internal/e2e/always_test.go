package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/status"
)

// Always lanes are setup work: they run every time, first, and every
// other lane in the run waits for them; Rerun-Children reruns what they
// feed.
func TestAlwaysLanes(t *testing.T) {
	r := newRepo(t, "")
	addHeader := func(n int, line string) {
		p := filepath.Join(r.root, fmt.Sprintf("lane.%d.sh", n))
		data, _ := os.ReadFile(p)
		os.WriteFile(p, []byte(strings.Replace(string(data), "\n", "\n"+line+"\n", 1)), 0o755)
	}
	rounds := func(n int) int { return strings.Count(r.log(n), "== ROUND ") }

	r.script(1, "lint", `gate "lint" bash -c 'sleep 0.5; test ! -e lintfail && touch lint.done'`)
	addHeader(1, "# Always: yes")
	addHeader(1, "# Rerun-Children: yes")
	r.script(2, "unit", `run "unit" true`)
	addHeader(2, "# After: 1")
	r.script(3, "e2e", `run "e2e" true`)
	addHeader(3, "# After: 2")
	r.script(4, "unrelated", `gate "setup finished first" test -e lint.done`)
	r.script(5, "init", `run "init" true`)
	addHeader(5, "# Always: yes")
	r.script(6, "needs init", `run "x" true`)
	addHeader(6, "# After: 5")

	// First run: everything passes, and lane 4 started after setup.
	out := r.mustSwim("all")
	for n := 1; n <= 6; n++ {
		if st := r.status().Get(n).State; st != status.Passed {
			t.Errorf("first all: swim %d = %s\n%s", n, st, out)
		}
	}

	// Second run: the Always lanes run again; lanes downstream of the
	// Rerun-Children lane rerun; the rest stay done.
	r.mustSwim("all")
	for n, want := range map[int]int{1: 2, 2: 2, 3: 2, 4: 1, 5: 2, 6: 1} {
		if got := rounds(n); got != want {
			t.Errorf("after second all: swim %d ran %d times, want %d", n, got, want)
		}
	}

	// The plan says so.
	plan := r.mustSwim("plan")
	contains(t, "plan", plan, "[*] swim 1", "[*] swim 5", "rerun (swim 1)")
	if strings.Contains(plan, "swim 4  unrelated") {
		t.Errorf("plan reruns a lane outside Rerun-Children:\n%s", plan)
	}
	if plan := r.mustSwim("plan", "--rerun"); !strings.Contains(plan, "then, once swim 1, 5 finish") {
		t.Errorf("plan --rerun has no setup phase line:\n%s", plan)
	}
	ydoc := r.mustSwim("plan", "--yaml")
	contains(t, "plan --yaml", ydoc, "action: always", "rerun_by: 1", "setup: [1, 5]")

	// Setup fails: lanes that name it are skipped; the others still run
	// once it has finished.
	os.WriteFile(filepath.Join(r.root, "lintfail"), nil, 0o644)
	if out, code := r.swim("all", "--rerun"); code != 1 {
		t.Fatalf("failing setup: exit %d\n%s", code, out)
	}
	f := r.status()
	for n, want := range map[int]string{1: status.Failed, 2: status.Skipped, 3: status.Skipped, 4: status.Passed, 5: status.Passed, 6: status.Passed} {
		if got := f.Get(n).State; got != want {
			t.Errorf("failing setup: swim %d = %s, want %s (%s)", n, got, want, f.Get(n).Reason)
		}
	}
	os.Remove(filepath.Join(r.root, "lintfail"))

	// A lane run by name pulls in the Always lanes it names, and no others.
	before1, before5 := rounds(1), rounds(5)
	r.mustSwim("run", "6")
	if rounds(5) != before5+1 || rounds(1) != before1 {
		t.Errorf("swim run 6: swim 5 ran %d times (want %d), swim 1 %d (want %d)", rounds(5), before5+1, rounds(1), before1)
	}
	before5 = rounds(5)
	r.mustSwim("run", "4")
	if rounds(5) != before5 {
		t.Errorf("swim run 4 (no After:) fired swim 5")
	}

	// Always lanes are standing setup: stub and archive need --force.
	if out, code := r.swim("stub", "1", "done"); code == 0 || !strings.Contains(out, "Always lane") {
		t.Errorf("stub of an Always lane: %d %s", code, out)
	}
	if out, code := r.swim("archive", "5"); code == 0 || !strings.Contains(out, "Always lane") {
		t.Errorf("archive of an Always lane: %d %s", code, out)
	}

	// An Always lane waits only on other Always lanes.
	addHeader(5, "# After: 4")
	if out, code := r.swim("all"); code == 0 || !strings.Contains(out, "waits only on other Always lanes") {
		t.Errorf("Always lane after a plain lane: %d %s", code, out)
	}
	if out, _ := r.swim("lint"); !strings.Contains(out, "waits only on other Always lanes") {
		t.Errorf("lint: %s", out)
	}
	r.mustSwim("stub", "5", "retired", "--force")
}
