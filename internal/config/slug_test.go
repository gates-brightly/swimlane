package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoSlug(t *testing.T) {
	remotes := map[string]string{}
	old := remoteFn
	remoteFn = func(root string) string { return remotes[root] }
	t.Cleanup(func() { remoteFn = old })

	remotes["/a/api"] = "git@github.com:org/api.git"
	remotes["/b/api-wt"] = "git@github.com:org/api" // a worktree: same remote, .git or not
	remotes["/c/api"] = "git@github.com:other/api.git"
	a, _ := repoSlug("/a/api")
	wt, _ := repoSlug("/b/api-wt")
	c, _ := repoSlug("/c/api")
	if !strings.HasPrefix(a, "api-") || len(a) != len("api-")+6 {
		t.Errorf("slug %q isn't name-<6 hex>", a)
	}
	if a[len(a)-6:] != wt[len(wt)-6:] {
		t.Errorf("worktree of the same remote: %q vs %q", a, wt)
	}
	if a == c {
		t.Errorf("two repos called api share %q", a)
	}
	// No remote: the root path, so a moved checkout changes it.
	x, _ := repoSlug("/d/tool")
	y, _ := repoSlug("/e/tool")
	if x == y {
		t.Errorf("no remote: two paths share %q", x)
	}
}

func TestOriginURL(t *testing.T) {
	cfg := "[core]\n\tbare = false\n[remote \"upstream\"]\n\turl = git@github.com:up/api.git\n[remote \"origin\"]\n\turl = git@github.com:org/api.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n"
	if got := originURL(cfg); got != "git@github.com:org/api.git" {
		t.Errorf("originURL = %q", got)
	}
	if got := originURL("[core]\n"); got != "" {
		t.Errorf("no origin: %q", got)
	}
	// A real repo, and a worktree of it, share the remote.
	root := t.TempDir()
	run := func(dir string, args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run(root, "init", "-q")
	run(root, "remote", "add", "origin", "git@github.com:org/api.git")
	run(root, "commit", "-q", "--allow-empty", "-m", "x")
	wt := filepath.Join(t.TempDir(), "wt")
	run(root, "worktree", "add", "-q", wt)
	a, _ := repoSlug(root)
	b, _ := repoSlug(wt)
	if remoteFn(wt) != "git@github.com:org/api.git" || a[len(a)-6:] != b[len(b)-6:] {
		t.Errorf("worktree: remote %q, slugs %q %q", remoteFn(wt), a, b)
	}
}
