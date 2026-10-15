package version

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewestTag(t *testing.T) {
	out := "a1\trefs/tags/v0.20261014.1\n" +
		"b2\trefs/tags/v0.20261015.2\n" +
		"c3\trefs/tags/v0.20261015.10\n" + // index 10 beats 2: compared as numbers
		"d4\trefs/tags/v0.7.20261013\n" + // the old scheme isn't a release tag now
		"e5\trefs/tags/v1.0.0\n"
	if got := NewestTag(out); got != "v0.20261015.10" {
		t.Errorf("NewestTag = %q", got)
	}
	if got := NewestTag(""); got != "" {
		t.Errorf("NewestTag(empty) = %q", got)
	}
}

func TestCompareRelease(t *testing.T) {
	for _, c := range []struct {
		td   string
		ti   int
		want int
	}{
		{"20261015", 2, 0},
		{"20261015", 3, 1},
		{"20261016", 1, 1},
		{"20261015", 1, -1},
		{"20261014", 9, -1},
	} {
		if got := compareRelease(c.td, c.ti, "20261015", 2); got != c.want {
			t.Errorf("compareRelease(%s, %d) = %d, want %d", c.td, c.ti, got, c.want)
		}
	}
}

// LatestTag asks a remote with git: here a local repo with tags.
func TestLatestTagFromRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("commit", "-q", "--allow-empty", "-m", "one")
	git("tag", "v0.20261014.1")
	git("tag", "v0.20261015.2")
	git("tag", "not-a-release")
	t.Setenv("SWIM_RELEASES_URL", dir)
	tag, err := LatestTag(10 * time.Second)
	if err != nil || tag != "v0.20261015.2" {
		t.Errorf("LatestTag = %q, %v", tag, err)
	}

	t.Setenv("SWIM_RELEASES_URL", filepath.Join(dir, "missing"))
	if _, err := LatestTag(10 * time.Second); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("an unreachable remote: err = %v, want one naming it", err)
	}
}
