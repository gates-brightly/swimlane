package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/chain"
)

// stdout is swim's standard output alone (mustSwim mixes in stderr), for
// output compared byte for byte.
func (r *repo) stdout(args ...string) string {
	r.t.Helper()
	out, err := r.cmd(bin, args...).Output()
	if err != nil {
		r.t.Fatalf("swim %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// The store keeps what a round ran and wrote outside the repo: after swim
// stub replaces the script and the repo's logs and chain are gone, swim
// store still shows the exact script and log the chain entry hashed, and
// swim store verify catches a kept object that was changed.
func TestStoreKeepsWhatRanAfterTheRepoLosesIt(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "store test", `run "say hello" -- echo hello-from-the-store`)
	original, err := os.ReadFile(filepath.Join(r.root, "lane.1.sh"))
	if err != nil {
		t.Fatal(err)
	}
	r.mustSwim("run", "1")
	run := r.status().Get(1).Run
	links, err := chain.Read(r.root)
	if err != nil {
		t.Fatal(err)
	}
	var entry chain.Link
	for _, l := range links {
		if l.Type == "round" && l.Run == run {
			entry = l
		}
	}
	if entry.Log == "" {
		t.Fatalf("no round entry for run %s in the chain", run)
	}
	if out := r.mustSwim("store", "verify"); !strings.Contains(out, "VERIFIED: 1 round kept") {
		t.Errorf("store verify after one round:\n%s", out)
	}

	// The repo loses it all: the script is stubbed, .swim is removed.
	r.mustSwim("archive", "1")
	r.mustSwim("stub", "1", "done")
	if err := os.RemoveAll(filepath.Join(r.root, ".swim")); err != nil {
		// Staged copies are read-only.
		filepath.Walk(filepath.Join(r.root, ".swim"), func(p string, _ os.FileInfo, _ error) error { return os.Chmod(p, 0o700) })
		os.RemoveAll(filepath.Join(r.root, ".swim"))
	}

	if out := r.mustSwim("store", "ls"); !strings.Contains(out, run) || !strings.Contains(out, "PASSED") {
		t.Errorf("store ls doesn't list the passed round %s:\n%s", run, out)
	}
	if got := r.stdout("store", "show", run[:len(run)-2], "--file", "lane.1.sh"); got != string(original) {
		t.Errorf("store show --file lane.1.sh isn't the script that ran:\n%s", got)
	}
	script := r.stdout("store", "show", run, "1", "--script")
	if chain.Hash([]byte(script)) != entry.Script {
		t.Errorf("the kept script hashes to %s; its chain entry says %s", chain.Hash([]byte(script))[:12], entry.Script[:12])
	}
	log := r.stdout("store", "show", run, "--log")
	if chain.Hash([]byte(log)) != entry.Log || !strings.Contains(log, "hello-from-the-store") {
		t.Errorf("the kept log isn't the section the chain hashed (%s, want %s):\n%s", chain.Hash([]byte(log))[:12], entry.Log[:12], log)
	}

	// Someone changes a kept object: show and verify both say so.
	dir := strings.TrimSpace(r.stdout("store", "path"))
	obj := filepath.Join(dir, "objects", entry.Log[:2], entry.Log)
	os.Chmod(obj, 0o600)
	if err := os.WriteFile(obj, []byte("nothing happened\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	if out, code := r.swim("store", "verify"); code != 1 || !strings.Contains(out, "BROKEN") || !strings.Contains(out, "log ("+entry.Log[:12]+"): object") {
		t.Errorf("store verify after a kept log was changed: exit %d\n%s", code, out)
	}
	if out, code := r.swim("store", "show", run, "--log"); code == 0 || !strings.Contains(out, "was changed") {
		t.Errorf("store show --log printed a changed object: exit %d\n%s", code, out)
	}
}

// A lane run directly with bash is kept too, from the working tree as it
// ends; and store verify reports a round the chain recorded that the
// store didn't keep.
func TestStoreKeepsDirectRunsAndMissesShow(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "direct", `run "direct" -- echo direct-run`)
	if out, err := r.cmd("bash", "lane.1.sh").CombinedOutput(); err != nil {
		t.Fatalf("bash lane.1.sh: %v\n%s", err, out)
	}
	run := r.status().Get(1).Run
	out := r.mustSwim("store", "show", run)
	if !strings.Contains(out, "run directly with bash") || !strings.Contains(out, "lane.1.sh") {
		t.Errorf("store show of a direct run:\n%s", out)
	}
	if log := r.stdout("store", "show", run, "--log"); !strings.Contains(log, "direct-run") {
		t.Errorf("store show --log of a direct run:\n%s", log)
	}

	// A later round's record goes; the chain still has the round.
	r.script(2, "launched", `run "launched" -- true`)
	r.mustSwim("run", "2")
	run2 := r.status().Get(2).Run
	dir := strings.TrimSpace(r.stdout("store", "path"))
	recs, _ := filepath.Glob(filepath.Join(dir, "repos", "*", run2, "2.yml"))
	if len(recs) != 1 {
		t.Fatalf("records for %s lane 2: %v", run2, recs)
	}
	os.Remove(recs[0])
	if out, code := r.swim("store", "verify"); code != 1 || !strings.Contains(out, run2+" lane 2: the chain recorded the round") {
		t.Errorf("store verify with a round's record removed: exit %d\n%s", code, out)
	}
}

// store_dir: off keeps nothing, and swim store says so.
func TestStoreOff(t *testing.T) {
	r := newRepo(t, "")
	r.mustSwim("config", "store_dir", "off")
	r.script(1, "off", `run "off" -- true`)
	r.mustSwim("run", "1")
	if out, code := r.swim("store", "ls"); code == 0 || !strings.Contains(out, "the store is off") {
		t.Errorf("store ls with store_dir off: exit %d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(envOf(r, "XDG_DATA_HOME"), "swim", "store")); err == nil {
		t.Error("store_dir: off, but the store directory was made")
	}
}
