package e2e

import (
	"os/exec"
	"strings"
	"testing"
)

// swim changelog prints the changelog built into the binary, and needs no repo.
func TestChangelog(t *testing.T) {
	dir := t.TempDir() // not a git repo
	run := func(args ...string) (string, int) {
		t.Helper()
		c := exec.Command(bin, append([]string{"changelog"}, args...)...)
		c.Dir = dir
		c.Env = []string{"NO_COLOR=1", "PATH=/usr/bin:/bin", "HOME=" + dir}
		out, err := c.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return string(out), code
	}
	headings := func(out string) int { return strings.Count("\n"+out, "\n## ") }

	out, code := run()
	if code != 0 || headings(out) != 1 {
		t.Fatalf("swim changelog: exit %d, %d revisions:\n%s", code, headings(out), out)
	}
	if !strings.Contains(out, "older revision(s): swim changelog -n") {
		t.Errorf("default output should point at older revisions:\n%s", out)
	}

	for _, args := range [][]string{{"-n", "2"}, {"-n2"}, {"-n=2"}, {"--n", "2"}} {
		if out, code := run(args...); code != 0 || headings(out) != 2 {
			t.Errorf("swim changelog %v: exit %d, %d revisions", args, code, headings(out))
		}
	}

	all, code := run("--all")
	if code != 0 || !strings.Contains(all, "## 2.20261009 (v0.2.20261009") || !strings.Contains(all, "## 64a0e07 (pre-release") {
		t.Fatalf("swim changelog --all: exit %d\n%s", code, all)
	}
	if strings.Contains(all, "older revision(s)") {
		t.Error("--all shouldn't point at older revisions")
	}

	since, code := run("--since", "v0.2.20261009")
	if code != 0 || strings.Contains(since, "## 2.20261009") || headings(since) != headings(all)-4 {
		t.Errorf("--since v0.2.20261009 should show only revisions after it (exit %d):\n%s", code, since)
	}

	if out, code := run("-n", "0"); code != 2 || !strings.Contains(out, "positive number") {
		t.Errorf("-n 0: exit %d\n%s", code, out)
	}
	if out, code := run("--since", "nope"); code != 1 || !strings.Contains(out, "known: ") {
		t.Errorf("--since nope: exit %d\n%s", code, out)
	}
	if out, code := run("-n", "1", "--all"); code != 2 {
		t.Errorf("-n with --all: exit %d\n%s", code, out)
	}
}
