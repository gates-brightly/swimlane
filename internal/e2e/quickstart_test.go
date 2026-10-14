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
func quickstartBlocks(t *testing.T) []string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(data)
	start := strings.Index(readme, "<!-- quickstart -->")
	end := strings.Index(readme, "<!-- /quickstart -->")
	if start < 0 || end < start {
		t.Fatal("README.md has no <!-- quickstart --> ... <!-- /quickstart --> block")
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
		"consistent  yes",     // swim verify
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
