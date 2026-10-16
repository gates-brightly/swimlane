package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/chain"
)

// swim clean archives every finished lane under one name, leaves Always
// lanes, removes waiting batches and moves scripts aside only when asked
// (no terminal: archive, and no to both), and --restore puts the scripts
// back; verify still passes.
func TestClean(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "one", `run "a" true`)
	r.script(2, "two", `run "b" true`)
	r.script(3, "setup", `run "c" true`)
	withHeader(t, r, 3, "# Always: yes")
	r.mustSwim("all")
	r.mustSwim("queue", "add", "1")

	// A dry run changes nothing under .swim/.
	swimDir := filepath.Join(r.root, ".swim")
	before := treeFiles(swimDir)
	contains(t, "dry run", r.mustSwim("clean", "-n"), "nothing is deleted", "archive  swim 1", "skip     swim 3  Always lane", "dry run: nothing changed")
	if changed := treeChanges(swimDir, before, treeFiles(swimDir)); len(changed) > 0 {
		t.Errorf("swim clean -n changed .swim/: %s", strings.Join(changed, ", "))
	}

	out := r.mustSwim("clean")
	contains(t, "clean", out, "archived 2 lanes as clean-")
	for _, n := range []int{1, 2} {
		if l := r.status().Get(n); l.State != "idle" || !strings.HasPrefix(l.LastArchive, "agent") {
			t.Errorf("swim %d after clean: %s %q", n, l.State, l.LastArchive)
		}
	}
	if l := r.status().Get(3); l.State != "passed" {
		t.Errorf("the Always lane was cleaned: %s", l.State)
	}
	contains(t, "queue kept", r.mustSwim("queue"), "#1")
	if _, err := os.Stat(filepath.Join(r.root, "lane.1.sh")); err != nil {
		t.Error("scripts moved without --scripts")
	}
	contains(t, "verify", r.mustSwim("verify"), "consistent  yes")

	// --all: the queue and the scripts too; --restore brings them back.
	r.mustSwim("run", "1")
	out = r.mustSwim("clean", "--all")
	contains(t, "all", out, "moved 2 scripts to .swim/cleaned/", "swim clean --restore")
	if _, err := os.Stat(filepath.Join(r.root, "lane.1.sh")); err == nil {
		t.Error("lane.1.sh still in place")
	}
	if _, err := os.Stat(filepath.Join(r.root, "lane.3.sh")); err != nil {
		t.Error("the Always lane's script was moved")
	}
	if out := r.mustSwim("queue"); strings.Contains(out, "#1") {
		t.Errorf("the waiting batch survived --all:\n%s", out)
	}
	dirs, _ := os.ReadDir(filepath.Join(r.root, ".swim", "cleaned"))
	if len(dirs) != 1 {
		t.Fatalf("cleaned: %v", dirs)
	}
	contains(t, "restore", r.mustSwim("clean", "--restore", dirs[0].Name()), "restored 2 scripts")
	if _, err := os.Stat(filepath.Join(r.root, "lane.1.sh")); err != nil {
		t.Error("lane.1.sh not restored")
	}
	contains(t, "log", r.mustSwim("log"), "clean ", "queue-rm")
	links, _ := chain.Read(r.root)
	n := 0
	for _, l := range links {
		if l.Type == "clean" {
			n++
		}
	}
	if n < 2 {
		t.Errorf("clean chain entries: %d", n)
	}
}

// swim prune deletes only past its cutoff, asks for --confirm without a
// terminal, and with --logs records each pruned round so verify reports
// it pruned, not missing.
func TestPrune(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "one", `run "a" true`)
	r.mustSwim("run", "1")
	r.mustSwim("archive", "1", "--as", "first")
	r.mustSwim("run", "1")
	r.mustSwim("clean", "--scripts", "--no-queue")

	contains(t, "bare", r.mustSwim("prune"), "deletes nothing without a cutoff", "moved-aside scripts", "archived logs")
	contains(t, "nothing old", r.mustSwim("prune", "--older", "30d", "--logs"), "nothing to prune past the cutoff")
	time.Sleep(1100 * time.Millisecond)
	if out, code := r.swim("prune", "--older", "1s", "--logs"); code == 0 || !strings.Contains(out, "--confirm") {
		t.Errorf("prune without a terminal or --confirm: exit %d\n%s", code, out)
	}
	out := r.mustSwim("prune", "--older", "1s", "--logs", "--confirm")
	contains(t, "prune", out, "pruned", "recorded in the chain as prune-")
	if files, _ := filepath.Glob(filepath.Join(r.root, ".swim", "logs", "agent1.prev-*.log")); len(files) != 0 {
		t.Errorf("archived logs left: %v", files)
	}
	if _, err := os.Stat(filepath.Join(r.root, ".swim", "cleaned")); err == nil {
		if dirs, _ := os.ReadDir(filepath.Join(r.root, ".swim", "cleaned")); len(dirs) != 0 {
			t.Errorf("moved-aside scripts left: %v", dirs)
		}
	}
	if _, err := os.Stat(filepath.Join(r.root, ".swim", "scripts")); err != nil {
		t.Error("kept script copies were deleted")
	}
	v := r.mustSwim("verify")
	contains(t, "verify", v, "consistent  yes", "was pruned on", "swim prune")
	if strings.Contains(v, "deleted, not archived") {
		t.Errorf("a pruned log reported missing:\n%s", v)
	}
}
