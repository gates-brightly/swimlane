package e2e

import (
	"strings"
	"testing"
)

// swim archive --done tidies every lane whose round passed: its log is
// archived and its script stubbed with the note, but a script edited
// since its round ran is kept, and a failed lane is left alone. verify
// still passes.
func TestArchiveDone(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "one", `run "a" true`)
	r.script(2, "two", `run "b" true`)
	r.script(3, "three", `run "c" true`)
	r.script(4, "four", `run "d" false`)
	r.swim("all", "--plain")
	r.script(3, "three again", `run "c2" true`) // new work since its round ran

	out := r.mustSwim("archive", "--done", "shipped")
	contains(t, "archive --done", out, "agent1.prev-", "agent2.prev-", "agent3.prev-", "kept lane.3.sh: changed since its round ran")
	if strings.Contains(out, "agent4") {
		t.Errorf("the failed lane was archived:\n%s", out)
	}
	for _, n := range []int{1, 2, 3} {
		if l := r.status().Get(n); l.State != "idle" || !strings.HasPrefix(l.LastArchive, "agent") {
			t.Errorf("swim %d after archive --done: %s %q", n, l.State, l.LastArchive)
		}
	}
	if l := r.status().Get(4); l.State != "failed" {
		t.Errorf("swim 4: %s", l.State)
	}
	for _, f := range []string{"lane.1.sh", "lane.2.sh"} {
		if s := readFile(t, r, f); !strings.Contains(s, "# swim:stub") || !strings.Contains(s, "shipped") {
			t.Errorf("%s isn't a stub with the note:\n%s", f, s)
		}
	}
	if !strings.Contains(readFile(t, r, "lane.3.sh"), "three again") {
		t.Error("lane.3.sh, edited since its round, was replaced")
	}
	contains(t, "status", r.mustSwim("status", "1"), "stub: shipped")
	log := r.mustSwim("log")
	contains(t, "log", log, "archive", "shipped")
	if strings.Contains(log, "  stub  ") {
		t.Errorf("a stub event:\n%s", log)
	}
	contains(t, "verify", r.mustSwim("verify", "--allow-unsigned"), "VERIFIED")
	contains(t, "again", r.mustSwim("archive", "--done"), "nothing to archive")
}
