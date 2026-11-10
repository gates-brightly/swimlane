package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// swim archive --passed tidies every lane whose round passed: its log is
// archived and its script removed (status keeps the note), but a script edited
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
	// Lanes 1 and 2 have no script now: empty. Lane 3 kept its new round: ready.
	for n, want := range map[int]string{1: "empty", 2: "empty", 3: "ready"} {
		if l := r.status().Get(n); l.State != want || !strings.HasPrefix(l.LastArchive, "lane") {
			t.Errorf("lane %d after archive --passed: %s %q", n, l.State, l.LastArchive)
		}
	}
	if l := r.status().Get(4); l.State != "failed" {
		t.Errorf("lane 4: %s", l.State)
	}
	for _, f := range []string{"lane.1.sh", "lane.2.sh"} {
		if _, err := os.Stat(filepath.Join(r.root, f)); !os.IsNotExist(err) {
			t.Errorf("archive left %s: %v", f, err)
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

// A committed lane (git tracks its script, as a repo's CI lanes) isn't
// archived: swim archive N refuses it, and --passed and the "passed, not
// archived" line leave it out. swim doctor --fix sweeps an untracked stub
// an older swim left, and names a tracked one for git rm.
func TestArchiveCommittedAndOldStubs(t *testing.T) {
	r := newRepo(t)
	r.script(1, "ci", `step "a" true`)
	r.commitLanes("ci lane")
	r.script(2, "two", `step "b" true`)
	r.swim("all", "--plain")
	contains(t, "passed, not archived", r.mustSwim(), "1 lane passed, not archived (lane 2)  → swim archive 2")

	if out, code := r.swim("archive", "1", "2", "done"); code == 0 || !strings.Contains(out, "lane 1 is committed") {
		t.Fatalf("archive a committed lane: %d %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(r.root, "lane.2.sh")); err != nil {
		t.Errorf("all or nothing: lane 2 was archived: %v", err)
	}
	out := r.mustSwim("archive", "--passed", "done")
	contains(t, "--passed", out, "lane2.prev-")
	if strings.Contains(out, "lane1.prev") {
		t.Errorf("--passed archived the committed lane:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(r.root, "lane.2.sh")); !os.IsNotExist(err) {
		t.Errorf("archive left lane.2.sh: %v", err)
	}
	if s := readFile(t, r, "lane.1.sh"); strings.Contains(s, "# swim:stub") {
		t.Errorf("the committed lane was stubbed:\n%s", s)
	}

	// Stubs an older swim left: one untracked, one committed.
	old := "#!/usr/bin/env bash\n# swim: syntax 4\n# swim:stub\n# old\necho nothing\n"
	for _, f := range []string{"lane.5.sh", "lane.6.sh"} {
		if err := os.WriteFile(filepath.Join(r.root, f), []byte(old), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r.git("add", "-f", "lane.6.sh")
	r.git("commit", "-q", "-m", "an old stub")
	out, _ = r.swim("doctor")
	contains(t, "doctor", out, "lane.5.sh", "swim doctor --fix (deletes it)", "git rm lane.6.sh (swim never writes to git)")
	r.swim("doctor", "--fix")
	if _, err := os.Stat(filepath.Join(r.root, "lane.5.sh")); !os.IsNotExist(err) {
		t.Errorf("doctor --fix left lane.5.sh: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.root, "lane.6.sh")); err != nil {
		t.Errorf("doctor --fix removed a tracked stub: %v", err)
	}
}

// swim new N above a free lane says which is free; the lowest, or a lane
// already in use, gets no note.
func TestNewNotesLowerFreeLane(t *testing.T) {
	r := newRepo(t)
	r.script(1, "one", `step "a" true`)
	contains(t, "new 7", r.mustSwim("new", "7", "seven"), "note: lane 2 is free")
	if out := r.mustSwim("new", "2", "two"); strings.Contains(out, "is free") {
		t.Errorf("the lowest free lane got a note:\n%s", out)
	}
	if out := r.mustSwim("new", "7", "seven again", "--force"); strings.Contains(out, "is free") {
		t.Errorf("rewriting a lane in use got a note:\n%s", out)
	}
}
