package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// The repo's name is origin's last path segment, else the toplevel's
// basename, else the directory's; the branch is "" when detached or outside
// git.
func TestRepoNameAndBranch(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "plain-dir")
	os.Mkdir(outside, 0o755)
	if got := RepoName(outside); got != "plain-dir" {
		t.Errorf("outside git: RepoName = %q", got)
	}
	if got := RepoBranch(outside); got != "" {
		t.Errorf("outside git: RepoBranch = %q", got)
	}

	repo := filepath.Join(base, "checkout")
	os.Mkdir(repo, 0o755)
	gitT(t, repo, "init", "-q", "-b", "fix-thing")
	sub := filepath.Join(repo, "sub")
	os.Mkdir(sub, 0o755)
	if got := RepoName(sub); got != "checkout" {
		t.Errorf("no origin: RepoName = %q, want the toplevel's basename", got)
	}
	if got := RepoBranch(sub); got != "fix-thing" {
		t.Errorf("RepoBranch = %q, want fix-thing", got)
	}
	for _, c := range []struct{ url, want string }{
		{"git@github.com:org/swimlane.git", "swimlane"},
		{"https://github.com/org/swimlane", "swimlane"},
		{"https://github.com/org/swimlane.git/", "swimlane"},
		{"ssh://git@host:22/org/infra.git", "infra"},
		{"/srv/git/ops.git", "ops"},
		{"git@host:ops", "ops"},
	} {
		gitT(t, repo, "config", "remote.origin.url", c.url)
		if got := RepoName(sub); got != c.want {
			t.Errorf("origin %s: RepoName = %q, want %q", c.url, got, c.want)
		}
	}

	gitT(t, repo, "commit", "-q", "--allow-empty", "-m", "x")
	wt := filepath.Join(base, "wt")
	gitT(t, repo, "worktree", "add", "-q", "-b", "other-fix", wt)
	if got := RepoBranch(wt); got != "other-fix" {
		t.Errorf("a worktree: RepoBranch = %q, want other-fix", got)
	}
	if got := RepoName(wt); got != "ops" {
		t.Errorf("a worktree: RepoName = %q, want origin's ops", got)
	}
	gitT(t, repo, "checkout", "-q", "--detach")
	if got := RepoBranch(repo); got != "" {
		t.Errorf("detached: RepoBranch = %q", got)
	}
}
