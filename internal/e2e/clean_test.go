package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/chain"
)

// swim clean idles every finished lane: it archives its logs under one
// name and stubs its script, but keeps a script edited since its round
// ran; it removes waiting batches only when asked (no terminal: no, and
// --all: yes). verify still passes.
func TestClean(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "one", `run "a" true`)
	r.script(2, "two", `run "b" true`)
	r.script(3, "three", `run "c" true`)
	r.mustSwim("all")
	r.script(3, "three again", `run "c2" true`) // new work since its round ran
	r.mustSwim("queue", "add", "3")

	// A dry run changes nothing under .swim/.
	swimDir := filepath.Join(r.root, ".swim")
	before := treeFiles(swimDir)
	contains(t, "dry run", r.mustSwim("clean", "-n"), "idle     swim 1", "script kept: changed since its last round", "dry run: nothing changed")
	if changed := treeChanges(swimDir, before, treeFiles(swimDir)); len(changed) > 0 {
		t.Errorf("swim clean -n changed .swim/: %s", strings.Join(changed, ", "))
	}

	out := r.mustSwim("clean")
	contains(t, "clean", out, "archived 3 lanes as clean-", "stubbed 2 scripts")
	for _, n := range []int{1, 2, 3} {
		if l := r.status().Get(n); l.State != "idle" || !strings.HasPrefix(l.LastArchive, "agent") {
			t.Errorf("swim %d after clean: %s %q", n, l.State, l.LastArchive)
		}
	}
	for n, stub := range map[int]bool{1: true, 2: true, 3: false} {
		script := readFile(t, r, fmt.Sprintf("lane.%d.sh", n))
		if got := strings.Contains(script, "cleaned: archived as clean-"); got != stub {
			t.Errorf("swim %d stubbed: %v, want %v:\n%s", n, got, stub, script)
		}
	}
	if !strings.Contains(readFile(t, r, "lane.3.sh"), "three again") {
		t.Error("lane.3.sh, edited since its round, was replaced")
	}
	contains(t, "queue kept", r.mustSwim("queue"), "#1")
	contains(t, "verify", r.mustSwim("verify", "--levels", "--allow-unsigned"), "consistent  yes")

	// --all: the waiting batch too.
	r.mustSwim("clean", "--all")
	if out := r.mustSwim("queue"); strings.Contains(out, "#1") {
		t.Errorf("the waiting batch survived --all:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(r.root, ".swim", "cleaned")); err == nil {
		t.Error("swim clean moved files aside")
	}
	contains(t, "log", r.mustSwim("log"), "clean ", "queue-rm", "stub ")
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
