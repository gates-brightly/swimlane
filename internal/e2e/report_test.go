package e2e

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// treeHash hashes every file under dir (paths and contents).
func treeHash(t *testing.T, dir string) string {
	t.Helper()
	var paths []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			paths = append(paths, p)
		}
		return nil
	})
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		data, _ := os.ReadFile(p)
		fmt.Fprintf(h, "%s\n%x\n", p, sha256.Sum256(data))
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// swim report shows what's new since the reader's earmark: the first
// report is the work in progress; after a lane runs again, only its new
// round, its .swim.log lines and its status; a peek changes nothing, and
// after --earmark there's nothing new.
func TestReportLoop(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "one", `run "first" echo one-output-a`)
	r.script(2, "two", `run "second" echo two-output`)
	r.mustSwim("all")

	out := r.mustSwim("report", "--earmark")
	contains(t, "first report", out, "the work in progress", "==> swim 1", "==> swim 2", "| one-output-a", "| two-output",
		".swim.log", "earmark default moved to")

	r.script(1, "one again", `run "first" echo one-output-b`)
	r.mustSwim("run", "1")
	before := treeHash(t, filepath.Join(r.root, ".swim"))
	out = r.mustSwim("report")
	contains(t, "second report", out, "since ", "(earmark default)", "==> swim 1", "| one-output-b", "unchanged: swim 2 PASS",
		"(new round)", "earmark default not moved (peek")
	for _, old := range []string{"one-output-a", "two-output", "==> swim 2"} {
		if strings.Contains(out, old) {
			t.Errorf("second report repeats %q:\n%s", old, out)
		}
	}
	if after := treeHash(t, filepath.Join(r.root, ".swim")); after != before {
		t.Error("swim report without --earmark changed .swim/")
	}
	if again := r.mustSwim("report"); !strings.Contains(again, "one-output-b") {
		t.Errorf("a peek moved the earmark:\n%s", again)
	}
	r.mustSwim("report", "--earmark")
	contains(t, "caught up", r.mustSwim("report"), "nothing new")

	// --results drops step output, saying where it is.
	r.mustSwim("run", "2")
	res := r.mustSwim("report", "--results")
	contains(t, "results", res, "PASS  second", "lines of command and output: swim log 2")
	if strings.Contains(res, "| two-output") {
		t.Errorf("--results kept step output:\n%s", res)
	}

	// --yaml: swim.report/v1, the round a swim.log/v1 document.
	var doc struct {
		Schema string `yaml:"schema"`
		Lanes  []struct {
			Lane   int `yaml:"lane"`
			Rounds []struct {
				Round struct {
					Schema string `yaml:"schema"`
				} `yaml:"round"`
			} `yaml:"rounds"`
		} `yaml:"lanes"`
	}
	if err := yaml.Unmarshal([]byte(r.mustSwim("report", "--yaml")), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Schema != "swim.report/v1" || len(doc.Lanes) != 1 || doc.Lanes[0].Lane != 2 || len(doc.Lanes[0].Rounds) != 1 || doc.Lanes[0].Rounds[0].Round.Schema != "swim.log/v1" {
		t.Errorf("yaml: %+v", doc)
	}
}

// Readers keep their own earmarks; narrowing never makes the next full
// report miss a line; an archived round keeps its place.
func TestReportReadersAndArchive(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "one", `run "first" echo one-a`)
	r.script(2, "two", `run "second" echo two-a`)
	r.mustSwim("all")
	r.mustSwim("report", "--earmark", "--name", "a")
	r.mustSwim("report", "--earmark", "--name", "b")

	r.script(1, "one again", `run "first" echo one-b`)
	r.script(2, "two again", `run "second" echo two-b`)
	r.mustSwim("all")
	// a reads only swim 1, then everything: swim 2's lines are still new.
	contains(t, "a narrowed", r.mustSwim("report", "1", "--earmark", "--name", "a"), "one-b")
	full := r.mustSwim("report", "--earmark", "--name", "a")
	contains(t, "a full", full, "two-b")
	if strings.Contains(full, "one-b") {
		t.Errorf("swim 1's round shown twice to reader a:\n%s", full)
	}
	// b hasn't read anything since.
	contains(t, "b", r.mustSwim("report", "--name", "b"), "one-b", "two-b")
	contains(t, "earmarks", r.mustSwim("report", "--earmarks"), "a ", "b ")

	// Archived, then run again: b gets the new round once, and nothing lost.
	r.mustSwim("archive", "1", "--as", "older")
	r.script(1, "one third", `run "first" echo one-c`)
	r.mustSwim("run", "1")
	out := r.mustSwim("report", "--name", "b")
	contains(t, "b after archive", out, "one-b", "one-c")
	if strings.Contains(out, "WARN") {
		t.Errorf("an archive lost the earmark's place:\n%s", out)
	}
	contains(t, "reset", r.mustSwim("report", "--earmark", "--reset", "--name", "b"), "earmark b removed")
}

// --wait returns as soon as there's something new (the run's first
// .swim.log line is); with nothing new it times out as swim wait does.
func TestReportWait(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "one", `run "first" echo waited-for`)
	r.mustSwim("run", "1")
	r.mustSwim("report", "--earmark")
	if out, code := r.swim("report", "--wait", "--timeout", "300ms", "--interval", "100ms"); code == 0 || !strings.Contains(out, "nothing new") {
		t.Errorf("--wait with nothing new: exit %d\n%s", code, out)
	}
	done := make(chan string, 1)
	go func() {
		out, _ := r.cmd(bin, "report", "--earmark", "--wait", "--interval", "100ms", "--timeout", "20s").CombinedOutput()
		done <- string(out)
	}()
	time.Sleep(300 * time.Millisecond)
	r.mustSwim("run", "1")
	select {
	case out := <-done:
		contains(t, "--wait", out, ".swim.log", "  run  ", "earmark default moved to")
	case <-time.After(25 * time.Second):
		t.Fatal("--wait didn't return")
	}
}

// A running step shows from its spool; the earmark stays before it, so
// the next report shows the step finished, not skipped as seen.
func TestReportInFlight(t *testing.T) {
	r := newRepo(t, "")
	release := filepath.Join(r.root, "release")
	r.script(1, "slow", `run "slow step" bash -c 'echo started-slow; while [ ! -f `+release+` ]; do sleep 0.05; done; echo finished-slow'`)
	c := r.cmd(bin, "run", "1", "--plain")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer c.Process.Kill()
	waitFor(t, func() bool { return r.status().Get(1).CurrentStep == "slow step" })
	time.Sleep(300 * time.Millisecond)
	contains(t, "in flight", r.mustSwim("report", "--earmark"), "RUNNING", "slow step", "in progress", "started-slow")
	os.WriteFile(release, nil, 0o644)
	c.Wait()
	out := r.mustSwim("report", "--earmark")
	contains(t, "after", out, "PASS  slow step", "finished-slow", "== END PASS")
	if strings.Contains(out, "RUNNING") {
		t.Errorf("the finished step still shows as running:\n%s", out)
	}
}
