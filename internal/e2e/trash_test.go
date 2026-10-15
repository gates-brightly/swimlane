package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// swim trash moves one lane's files aside with their hashes, archives its
// log and drops it from swim status; swim trash --restore and swim clean
// --restore put it back byte for byte, and swim prune --stamp deletes it.
func TestTrash(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "one", `run "a" true`)
	r.script(2, "two", `run "b" true`)
	r.mustSwim("run", "1")
	before := readFile(t, r, "lane.1.sh")

	contains(t, "dry run", r.mustSwim("trash", "1", "-n"), "would trash swim 1: move lane.1.sh", "dry run: nothing changed")
	if _, err := os.Stat(filepath.Join(r.root, "lane.1.sh")); err != nil {
		t.Fatal("-n moved the script")
	}
	out := r.mustSwim("trash", "1")
	contains(t, "trash", out, "trashed swim 1: lane.1.sh", "log archived as agent1.prev-trash-", "swim trash --restore ")
	dirs, _ := os.ReadDir(filepath.Join(r.root, ".swim", "cleaned"))
	if len(dirs) != 1 || !strings.HasSuffix(dirs[0].Name(), "-lane1") {
		t.Fatalf("cleaned: %v", dirs)
	}
	stamp := dirs[0].Name()
	if _, err := os.Stat(filepath.Join(r.root, "lane.1.sh")); err == nil {
		t.Error("lane.1.sh still in place")
	}
	contains(t, "manifest", readFile(t, r, ".swim/cleaned/"+stamp+"/cleaned.yml"), "lane: 1", "from: cli", "path: lane.1.sh", "sha256: ")
	if st := r.mustSwim("status"); strings.Contains(st, "swim 1 ") {
		t.Errorf("swim status still lists swim 1:\n%s", st)
	}
	contains(t, "list", r.mustSwim("trash"), stamp, "swim 1", "cli")
	contains(t, "verify", r.mustSwim("verify"), "consistent  yes")
	contains(t, "log", r.mustSwim("log"), "trash ", stamp)
	// Queueing or running a trashed lane says where it went, not that a
	// Round: line is missing.
	for _, args := range [][]string{{"queue", "add", "1"}, {"run", "1", "--plain"}} {
		out, code := r.swim(args...)
		if code == 0 {
			t.Errorf("swim %v on a trashed lane exited 0:\n%s", args, out)
		}
		contains(t, strings.Join(args, " "), out, "swim 1 was trashed ("+stamp, "swim trash --restore "+stamp)
	}

	contains(t, "restore", r.mustSwim("trash", "--restore", stamp), "restored swim 1", "pending again")
	if got := readFile(t, r, "lane.1.sh"); got != before {
		t.Error("the restored script differs")
	}
	contains(t, "untrash", r.mustSwim("log"), "untrash ")

	// swim clean --restore takes a trash stamp too; swim prune deletes one.
	r.mustSwim("trash", "2")
	dirs, _ = os.ReadDir(filepath.Join(r.root, ".swim", "cleaned"))
	contains(t, "clean --restore", r.mustSwim("clean", "--restore", dirs[0].Name()), "restored swim 2")
	r.mustSwim("trash", "2")
	dirs, _ = os.ReadDir(filepath.Join(r.root, ".swim", "cleaned"))
	contains(t, "prune", r.mustSwim("prune", "--stamp", dirs[0].Name(), "--confirm"), "pruned 1 items")
	contains(t, "verify after", r.mustSwim("verify"), "consistent  yes")
}

// What swim trash refuses, and why: a lane in a queued batch, an Always
// lane (unless --force), a group file's lane, a lane with no script.
func TestTrashRefuses(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "queued", `run "a" true`)
	r.script(2, "always", `run "b" true`)
	withHeader(t, r, 2, "# Always: yes")
	r.mustSwim("queue", "add", "1")
	if out, code := r.swim("trash", "1"); code == 0 || !strings.Contains(out, "in a queued batch, #1") || !strings.Contains(out, "swim queue rm 1 first") {
		t.Errorf("queued lane: exit %d\n%s", code, out)
	}
	if out, code := r.swim("trash", "2"); code == 0 || !strings.Contains(out, "an Always lane") || !strings.Contains(out, "--force") {
		t.Errorf("Always lane: exit %d\n%s", code, out)
	}
	contains(t, "--force", r.mustSwim("trash", "2", "--force"), "trashed swim 2")
	if out, code := r.swim("trash", "9"); code == 0 || !strings.Contains(out, "no lane script") {
		t.Errorf("no script: exit %d\n%s", code, out)
	}
	r.write("lanes.stack.sh", `#!/usr/bin/env bash
# swim: syntax 2
# Round: a stack

# swim job 5.1 -----
# Round: provision
run "provision" true
`, 0o644)
	if out, code := r.swim("trash", "5"); code == 0 || !strings.Contains(out, "group file") {
		t.Errorf("group file lane: exit %d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(r.root, "lanes.stack.sh")); err != nil {
		t.Error("the group file moved")
	}
}
