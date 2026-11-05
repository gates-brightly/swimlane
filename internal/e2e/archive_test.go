package e2e

import (
	"strings"
	"testing"
)

// swim archive --passed tidies every lane whose round passed: its log is
// archived and its script stubbed with the note, but a script edited
// since its round ran is kept, and a failed lane is left alone. verify
// still passes.
func TestArchiveDone(t *testing.T) {
	r := newRepo(t)
	r.script(1, "one", `step "a" true`)
	r.script(2, "two", `step "b" true`)
	r.script(3, "three", `step "c" true`)
	r.script(4, "four", `step "d" false`)
	r.swim("all", "--plain")
	r.script(3, "three again", `step "c2" true`) // new work since its round ran

	out := r.mustSwim("archive", "--passed", "shipped")
	contains(t, "archive --passed", out, "lane1.prev-", "lane2.prev-", "lane3.prev-", "kept lane.3.sh: changed since its round ran")
	if strings.Contains(out, "lane4.prev") {
		t.Errorf("the failed lane was archived:\n%s", out)
	}
	// Lanes 1 and 2 hold a stub now: empty. Lane 3 kept its new round: ready.
	for n, want := range map[int]string{1: "empty", 2: "empty", 3: "ready"} {
		if l := r.status().Get(n); l.State != want || !strings.HasPrefix(l.LastArchive, "lane") {
			t.Errorf("lane %d after archive --passed: %s %q", n, l.State, l.LastArchive)
		}
	}
	if l := r.status().Get(4); l.State != "failed" {
		t.Errorf("lane 4: %s", l.State)
	}
	for _, f := range []string{"lane.1.sh", "lane.2.sh"} {
		if s := readFile(t, r, f); !strings.Contains(s, "# swim:stub") || !strings.Contains(s, "shipped") {
			t.Errorf("%s isn't a stub with the note:\n%s", f, s)
		}
	}
	if !strings.Contains(readFile(t, r, "lane.3.sh"), "three again") {
		t.Error("lane.3.sh, edited since its round, was replaced")
	}
	contains(t, "status", r.mustSwim("status", "1"), "note: shipped")
	log := r.mustSwim("log")
	contains(t, "log", log, "archive", "shipped")
	if strings.Contains(log, "  stub  ") {
		t.Errorf("a stub event:\n%s", log)
	}
	contains(t, "verify", r.mustSwim("verify", "--allow-unsigned"), "VERIFIED")
	contains(t, "again", r.mustSwim("archive", "--passed"), "nothing to archive")
}
