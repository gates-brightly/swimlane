package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// quickstartBlocks are the ```sh blocks between the README's
// <!-- quickstart --> and <!-- /quickstart --> markers.
func quickstartBlocks(t *testing.T) []string { return markedBlocks(t, "README.md") }

// markedBlocks are the ```sh blocks between a page's <!-- quickstart -->
// and <!-- /quickstart --> markers (page is repo-relative).
func markedBlocks(t *testing.T, page string) []string {
	t.Helper()
	return namedBlocks(t, page, "quickstart")
}

// namedBlocks are the ```sh blocks between a page's <!-- name --> and
// <!-- /name --> markers.
func namedBlocks(t *testing.T, page, name string) []string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", page))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(data)
	start := strings.Index(readme, "<!-- "+name+" -->")
	end := strings.Index(readme, "<!-- /"+name+" -->")
	if start < 0 || end < start {
		t.Fatalf("%s has no <!-- %s --> ... <!-- /%s --> block", page, name, name)
	}
	var blocks []string
	for _, m := range regexp.MustCompile("(?s)```sh\n(.*?)```").FindAllStringSubmatch(readme[start:end], -1) {
		blocks = append(blocks, m[1])
	}
	if len(blocks) == 0 {
		t.Fatalf("%s's %s block has no ```sh blocks", page, name)
	}
	return blocks
}

// The README's first five minutes run as written, in order, with no
// credentials, with the lanes the AI would write: if a command, flag or
// output it relies on changes, this fails.
func TestQuickstartRuns(t *testing.T) {
	r := newRepo(t) // its config (signing: off) applies to the quickstart's own repo too
	dir := t.TempDir()
	script := "set -e\n" + strings.Join(quickstartBlocks(t), "\n")
	c := r.cmd("bash", "-c", script)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("quickstart failed: %v\n%s", err, out)
	}
	qs := filepath.Join(dir, "swim-try")
	contains(t, "quickstart output", string(out),
		"PASS  how big it is", // swim log 1
		"SKIP  delete the .tmp files (dry run: set TRY_ALLOW_DELETE=1 to approve)", // the page quotes this line
		"exit 3: nothing deleted",
		"the .tmp files are gone",
	)
	for _, lane := range []string{"1", "2"} {
		lint := r.cmd(bin, "lint", lane)
		lint.Dir = qs
		if out, err := lint.CombinedOutput(); err != nil || !strings.Contains(string(out), "no problems found") {
			t.Errorf("the README's lane %s doesn't lint clean (the AI is told to lint it): %v\n%s", lane, err, out)
		}
	}
	log2, _ := os.ReadFile(filepath.Join(qs, ".swim", "logs", "lane2.log"))
	contains(t, "lane 2's log", string(log2), "SKIP", "APPROVED")
}

// docs/guide.md, the tour, runs as written, in order, with no
// credentials: each step does what the page says it does.
func TestTourRuns(t *testing.T) {
	r := newRepo(t)
	dir := t.TempDir()
	c := r.cmd("bash", "-c", "set -e\n"+strings.Join(markedBlocks(t, "docs/guide.md"), "\n"))
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("docs/guide.md failed: %v\n%s", err, out)
	}
	contains(t, "the tour", string(out),
		"PASS  count them",                                                            // 2
		"STOP  gate failed: there is a config file", "the round failed, as it should", // 3
		"SKIP  remove scratch.txt from the repo (dry run", "scratch.txt is gone", // 4
		"approved in queue q-", "removed, approved in the queue", // 5
		"changed since it was queued", "skipped, not run", // 5: the edit voids it
		"skipped (lane 3 failed)",                                  // 6
		"VERIFIED",                                                 // 8
		"was changed after the round ended", "the edit was caught", // 8: the record catches an edit
	)
	if strings.Contains(string(out), "undeclared files") {
		t.Errorf("a step left an undeclared output:\n%s", out)
	}
}
