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

// tamperSetup is what the README's tamper story starts from and doesn't
// show: a repo with a signing key, which swim init lists in lookup.yml,
// and the AI's lane.1.sh, whose one guarded step runs install.sh.
const tamperSetup = `mkdir tamper && cd tamper && git init -q
mkdir -p "$HOME/.ssh" && ssh-keygen -q -t ed25519 -N '' -f "$HOME/.ssh/id_ed25519"
swim config signing auto
swim init >/dev/null
echo 'echo installing the new cert' > install.sh
cat > lane.1.sh <<'LANE'
#!/usr/bin/env bash
# swim: syntax 4
# Round:    Rotate the cert
# Guards:   CERT_ALLOW_ROTATE  replace the live cert
# Includes: install.sh
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 1

stage snapshot
snapshot "the cert now" echo "cert 2025"

stage change
if guard CERT_ALLOW_ROTATE "replace the live cert"; then
  run "install the new cert" bash install.sh
fi
summary
LANE
git add -A && git commit -qm "the AI's round"
`

// The README's tamper story runs as written, from tamperSetup, and prints
// what its comments say it does.
func TestTamperStoryRuns(t *testing.T) {
	for _, tool := range []string{"ssh-keygen", "perl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not installed")
		}
	}
	r := newRepo(t, "")
	dir := t.TempDir()
	c := r.cmd("bash", "-c", "set -e\n"+tamperSetup+"set +e\n"+strings.Join(namedBlocks(t, "README.md", "tamper"), "\n"))
	c.Dir = dir
	c.Env = append(c.Env, "HOME="+t.TempDir(), "USER=me",
		"GIT_AUTHOR_NAME=me", "GIT_AUTHOR_EMAIL=me@example.com", "GIT_COMMITTER_NAME=me", "GIT_COMMITTER_EMAIL=me@example.com")
	out, _ := c.CombinedOutput()
	contains(t, "tamper story", string(out),
		"SKIP  replace the live cert (dry run:", "approves another version of lane.1.sh", "review the change",
		"swim approve 1 --guards CERT_ALLOW_ROTATE)",
		"APPROVED  replace the live cert (approved by me (signed))",
		"PASS  install the new cert",
		"log for run r-", "was changed after the round ended",
		"NOT VERIFIED: consistent: 1 problem (BROKEN, above)")
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
		"approves another version of lane.2.sh", // 5: the edit voids it
		"SKIP (swim 3 failed)",                  // 6
		"VERIFIED",                              // 8
	)
	if strings.Contains(string(out), "undeclared files") {
		t.Errorf("a step left an undeclared output:\n%s", out)
	}
}
