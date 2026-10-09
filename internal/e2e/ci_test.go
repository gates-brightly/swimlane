package e2e

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/status"
)

// ciEnv runs swim with extra environment.
func (r *repo) swimEnv(env []string, args ...string) (string, int) {
	c := r.cmd(bin, args...)
	c.Env = append(append([]string(nil), r.env...), env...)
	out, err := c.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		r.t.Fatal(err)
	}
	return string(out), code
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	c := exec.Command("git", append([]string{"-C", r.root}, args...)...)
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := c.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func ciRepo(t *testing.T) *repo {
	r := newRepo(t, "")
	r.git("commit", "-q", "--allow-empty", "-m", "base")
	r.script(1, "root", `run "hello" echo hi`)
	r.script(2, "fails", `run "convert" false
guard ALLOW_X "drop x - 2026-10-09"
drift "queue policy differs"`)
	r.script(3, "after fails", `run "never" true`)
	r.script(4, "after root", `run "child" true`)
	withHeader(t, r, 2, "# After: 1")
	withHeader(t, r, 3, "# After: 2")
	withHeader(t, r, 4, "# After: 1")
	return r
}

func TestCIGitHub(t *testing.T) {
	r := ciRepo(t)
	summary := filepath.Join(t.TempDir(), "summary.md")
	junit := filepath.Join(r.root, "swim.xml")
	out, code := r.swimEnv([]string{"GITHUB_ACTIONS=true", "CI=true", "GITHUB_STEP_SUMMARY=" + summary, "ALLOW_Y=1"}, "ci", "--junit", junit)
	if code != 1 {
		t.Fatalf("a failed lane exits 1, got %d:\n%s", code, out)
	}
	contains(t, "ci output", out,
		"swim ci · swim ", "provider github", "selected: every pending round that hasn't passed",
		"guard flags set: none",
		"[2] ==> convert", "[2] FAIL  convert (exit 1)", // step headers and results stream live
		"::group::swim 1 PASS - root (job ", "::endgroup::",
		"==== swim 2 FAIL - fails (job ", // a failed lane stays open
		"::error title=swim 2%3A fails::FAIL  convert (exit 1)",
		"::warning title=swim 2%3A fails::DRIFT  queue policy differs",
		"::notice title=swim 2%3A fails::drop x - 2026-10-09: dry run: set ALLOW_X=1 to approve",
		"::notice title=swim%3A 1 lane skipped::swim 3 (swim 2 failed)",
		"artifacts to upload: .swim/logs/ .swim/snapshots/ .swim.log "+junit)
	md, _ := os.ReadFile(summary)
	contains(t, "job summary", string(md), "## swim ci: failed", "| swim 2 |", "FAIL  convert (exit 1)", "| swim 3 |", "swim 2 failed")
	data, _ := os.ReadFile(junit)
	var j struct {
		Suites []struct {
			Name string `xml:"name,attr"`
		} `xml:"testsuite"`
		Failures int `xml:"failures,attr"`
	}
	if err := xml.Unmarshal(data, &j); err != nil || len(j.Suites) != 4 || j.Failures != 1 {
		t.Errorf("junit: %v %+v\n%s", err, j, data)
	}
	// Commit-tagged results.
	commit := r.git("rev-parse", "HEAD")
	if l := r.status().Get(1); l.Commit != commit {
		t.Errorf("status commit %q, want %q", l.Commit, commit)
	}
	contains(t, "agent1.log", r.log(1), "| commit: "+commit)
	hist, _ := os.ReadFile(filepath.Join(r.root, ".swim.log"))
	contains(t, ".swim.log", string(hist), "commit="+commit)

	// Lanes never get stdin; nothing left to do is a notice (exit 0), or 1
	// with --require-work.
	r.script(2, "fixed", `run "convert" true`)
	r.script(3, "after fixed", `run "x" true`)
	r.mustSwim("all")
	out, code = r.swimEnv([]string{"GITHUB_ACTIONS=true"}, "ci")
	if code != 0 || !strings.Contains(out, "::notice title=swim ci%3A nothing to run::") {
		t.Errorf("nothing to run: %d\n%s", code, out)
	}
	if _, code := r.swimEnv([]string{"GITHUB_ACTIONS=true"}, "ci", "--require-work"); code != 1 {
		t.Errorf("--require-work with nothing to run: exit %d", code)
	}
}

func TestCIGitLabAndGeneric(t *testing.T) {
	r := ciRepo(t)
	out, _ := r.swimEnv([]string{"GITLAB_CI=true", "CI=true"}, "ci", "1")
	contains(t, "gitlab", out, "provider gitlab", "\x1b[0Ksection_start:", "[collapsed=true]\r\x1b[0Kswim 1 PASS - root", "\x1b[0Ksection_end:")
	out, _ = r.swimEnv([]string{"CI=true"}, "ci", "2")
	contains(t, "generic", out, "provider generic", "swim: error: swim 2: fails: FAIL  convert (exit 1)")
}

func TestCIChanged(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "one", `run "a" true`)
	r.script(2, "two", `run "b" true`)
	r.script(3, "three", `run "c" true`)
	withHeader(t, r, 3, "# After: 1")
	r.git("add", "-f", "lane.1.sh", "lane.2.sh", "lane.3.sh")
	r.git("commit", "-q", "-m", "lanes")
	r.mustSwim("all") // everything passed
	base := r.git("rev-parse", "HEAD")

	// Commit 2 touches lane 2 only.
	r.script(2, "two v2", `run "b2" true`)
	r.git("add", "-f", "lane.2.sh")
	r.git("commit", "-q", "-m", "lane 2")
	out, code := r.swimEnv([]string{"CI=true"}, "ci", "--changed="+base)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, "changed", out, "selected: --changed: since "+base+"; changed: lane.2.sh", "lanes 2\n")
	if strings.Contains(out, "[1] started") || strings.Contains(out, "[3] started") {
		t.Errorf("unchanged lanes ran:\n%s", out)
	}

	// Commit 3 rewrites lane 3, whose parent lane 1 is also rewritten but
	// not yet run: the changed lane brings its unpassed parent along.
	r.script(1, "one v2", `run "a2" true`)
	r.mustSwim("status")
	head2 := r.git("rev-parse", "HEAD")
	r.script(3, "three v2", `run "c2" true`)
	withHeader(t, r, 3, "# After: 1")
	r.git("add", "-f", "lane.3.sh")
	r.git("commit", "-q", "-m", "lane 3")
	out, _ = r.swimEnv([]string{"CI=true"}, "ci", "--changed="+head2)
	contains(t, "changed with parent", out, "changed: lane.3.sh", "lanes 1, 3\n", "[1] started: one v2", "[3] started: three v2")

	// GitHub push event: base from the payload; a zero before SHA runs
	// every pending round.
	ev := filepath.Join(t.TempDir(), "event.json")
	os.WriteFile(ev, []byte(`{"before":"0000000000000000000000000000000000000000"}`), 0o644)
	out, _ = r.swimEnv([]string{"GITHUB_ACTIONS=true", "GITHUB_EVENT_NAME=push", "GITHUB_EVENT_PATH=" + ev}, "ci", "--changed")
	contains(t, "new branch", out, "a new branch (no before SHA): every pending round")
}

func TestCIPinsAndLanes(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "one", `run "a" true`)
	// A script beyond the configured lanes with no lanes setting anywhere
	// is counted in on a runner...
	r.script(6, "six", `run "six" true`)
	cfgDir := filepath.Join(t.TempDir(), "empty")
	out, code := r.swimEnv([]string{"CI=true", "XDG_CONFIG_HOME=" + cfgDir}, "ci")
	if code != 0 || !strings.Contains(out, "lanes: 6 from the highest lane script") || !strings.Contains(out, "[6] started: six") {
		t.Errorf("fresh runner: %d\n%s", code, out)
	}
	// ...but with lanes set (swim.yml), it fails with the fix.
	os.WriteFile(filepath.Join(r.root, "swim.yml"), []byte("lanes: 4\n"), 0o644)
	r.script(6, "six again", `run "six" true`)
	out, code = r.swimEnv([]string{"CI=true", "XDG_CONFIG_HOME=" + cfgDir}, "ci")
	if code != 2 || !strings.Contains(out, "fix: set `lanes: 6` in swim.yml") {
		t.Errorf("lanes set: %d\n%s", code, out)
	}
	os.Remove(filepath.Join(r.root, "swim.yml"))

	// Pinned: a lane rewritten after selection (between the job id it was
	// named by and its start) is skipped. Simulate with swim run JOB.
	r.script(2, "two", `run "slow" sleep 0.5`)
	r.script(3, "three", `run "c" true`)
	withHeader(t, r, 3, "# After: 2")
	withHeader(t, r, 3, "# Job: 3f2a9c1e-aaaa-4bbb-8ccc-000000000003")
	job3 := jobOf(t, r, 3)
	c := r.cmd(bin, "run", "2", job3, "--plain")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return r.status().Get(2).CurrentStep == "slow" })
	r.script(3, "three rewritten", `run "c" true`)
	c.Wait()
	l := r.status().Get(3)
	if l.State != status.Skipped || !strings.Contains(l.Reason, "was rewritten") {
		t.Errorf("rewritten pinned lane: %+v", l)
	}
}

func jobOf(t *testing.T, r *repo, n int) string {
	t.Helper()
	data, _ := os.ReadFile(filepath.Join(r.root, fmt.Sprintf("lane.%d.sh", n)))
	for _, line := range strings.Split(string(data), "\n") {
		if f := strings.Fields(line); len(f) == 3 && f[0] == "#" && f[1] == "Job:" {
			return f[2]
		}
	}
	t.Fatalf("no job line in lane.%d.sh", n)
	return ""
}

func TestCIHeartbeat(t *testing.T) {
	r := newRepo(t, "")
	r.git("commit", "-q", "--allow-empty", "-m", "base")
	r.script(1, "quiet", `run "wait" sleep 1.2`)
	r.script(2, "chatty", `run "talk" bash -c 'for i in 1 2 3 4 5 6; do echo tick; sleep 0.2; done'`)
	out, code := r.swimEnv([]string{"CI=true"}, "ci", "--heartbeat", "400ms")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	contains(t, "heartbeat", out, "[1] still running: wait (0:0")
	if strings.Contains(out, "[2] still running") {
		t.Errorf("a lane writing output needs no heartbeat:\n%s", out)
	}
}
