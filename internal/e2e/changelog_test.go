package e2e

import (
	"encoding/base64"
	"os/exec"
	"path/filepath"
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

// A swim the justfile stamped with its last commits (version.Recent) shows
// them by default: each one's subject, what it changed and what its message
// says; -n still shows the release notes.
func TestChangelogCommits(t *testing.T) {
	long(t)
	recent := base64.StdEncoding.EncodeToString([]byte(
		"\x1eb111496\x1f2026-10-30\x1fswim watch: ask for guards\x1fThe screen asks.\n\nCo-Authored-By: x <y@z>\n\x1f\n\ninternal/cli/watchask.go\ndocs/usage.md\n" +
			"\x1ea22d658\x1f2026-10-30\x1ffeat: update git controls\x1f\x1f\n\ninternal/policy/policy.go\n"))
	path := filepath.Join(t.TempDir(), "swim")
	const v = "github.com/gates-brightly/swimlane/internal/version"
	build := exec.Command("go", "build", "-buildvcs=false", "-o", path, "-ldflags", "-X "+v+".Recent="+recent, "./cmd/swim")
	build.Dir = repoRoot()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	run := func(args ...string) string {
		t.Helper()
		c := exec.Command(path, append([]string{"changelog"}, args...)...)
		c.Dir = t.TempDir()
		c.Env = []string{"NO_COLOR=1", "PATH=/usr/bin:/bin", "HOME=" + c.Dir}
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("swim changelog %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	out := run()
	contains(t, "swim changelog", out, "the last 2 commits",
		"b111496  2026-10-30  swim watch: ask for guards", "changed: docs (1), internal/cli (1)", "    The screen asks.",
		"a22d658  2026-10-30  feat: update git controls", "changed: internal/policy (1)",
		"swim changelog -n 1 for the release notes")
	if strings.Contains(out, "Co-Authored-By") || strings.Contains(out, "## ") {
		t.Errorf("trailers or release notes in the commits view:\n%s", out)
	}
	if out := run("-n", "1"); strings.Count("\n"+out, "\n## ") != 1 {
		t.Errorf("-n 1 should show one revision:\n%s", out)
	}
}
