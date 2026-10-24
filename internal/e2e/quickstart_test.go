package e2e

import (
	"os"
	"os/exec"
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
	_, file, _, _ := runtime.Caller(0)
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", page))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(data)
	start := strings.Index(readme, "<!-- quickstart -->")
	end := strings.Index(readme, "<!-- /quickstart -->")
	if start < 0 || end < start {
		t.Fatalf("%s has no <!-- quickstart --> ... <!-- /quickstart --> block", page)
	}
	var blocks []string
	for _, m := range regexp.MustCompile("(?s)```sh\n(.*?)```").FindAllStringSubmatch(readme[start:end], -1) {
		blocks = append(blocks, m[1])
	}
	if len(blocks) == 0 {
		t.Fatal("the quickstart has no ```sh blocks")
	}
	return blocks
}

// The README's quickstart runs as written, in order, with no credentials:
// if a command, flag or output it relies on changes, this fails.
func TestQuickstartRuns(t *testing.T) {
	r := newRepo(t, "") // its config (signing: off) applies to the quickstart's own repo too
	dir := t.TempDir()
	script := "set -e\n" + strings.Join(quickstartBlocks(t), "\n")
	c := r.cmd("bash", "-c", script)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("quickstart failed: %v\n%s", err, out)
	}
	qs := filepath.Join(dir, "swim-quickstart")
	contains(t, "quickstart output", string(out),
		"hello from swim",     // swim log 1
		"VERIFIED",            // swim verify
		"dry run",             // the guarded step, unapproved
		"HELLO_ALLOW_WRITE=1", // the approval, as recorded
		"via flag",            // swim history
	)
	if got, _ := os.ReadFile(filepath.Join(qs, "hello.txt")); string(got) != "hello\n" {
		t.Errorf("hello.txt = %q, want the approved step's output", got)
	}
	log2, _ := os.ReadFile(filepath.Join(qs, ".swim", "logs", "agent2.log"))
	contains(t, "lane 2's log", string(log2), "SKIP", "APPROVED")
}

// docs/quickstart.md, the hour-long guide, runs as written, in order, with
// no credentials: each step does what the page says it does.
func TestDocsQuickstartRuns(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not installed")
	}
	r := newRepo(t, "")
	dir := t.TempDir()
	c := r.cmd("bash", "-c", "set -e\n"+strings.Join(markedBlocks(t, "docs/quickstart.md"), "\n"))
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("docs/quickstart.md failed: %v\n%s", err, out)
	}
	contains(t, "docs quickstart", string(out),
		"PASS  count them",                                                            // 2
		"STOP  gate failed: there is a config file", "the round failed, as it should", // 3
		"SKIP  remove scratch.txt (quickstart) (dry run", "scratch.txt is gone", // 4
		"approved by me (signed)", "removed, approved by a signature", // 5
		"approves another version of lane.2.sh",          // 5: the edit voids it
		"SKIP (swim 3 failed)",                           // 6
		"PASS  report report.md", "The service came up.", // 8
		"VERIFIED", // 9
	)
	if strings.Contains(string(out), "undeclared files") {
		t.Errorf("a step left an undeclared output:\n%s", out)
	}
}
