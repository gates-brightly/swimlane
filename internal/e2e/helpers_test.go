package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The helper modules (helpers/*.sh), included and sourced by a lane:
// gh_pr_fetch fetches a pull request's head and logs the commit, aws_gate
// stops a round on the wrong account or no session, the gh_actions and os
// checks tell machines apart, and a helper called wrong stops the round.
func TestOpsHelpers(t *testing.T) {
	r := newRepo(t)
	installHelpers(t, r, "aws", "gh", "os")

	// A remote holding pull request 7's head, as GitHub keeps it.
	git := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	work, remote := t.TempDir(), t.TempDir()
	git(remote, "init", "-q", "--bare")
	git(work, "init", "-q")
	git(work, "commit", "-q", "--allow-empty", "-m", "the change under review")
	git(work, "push", "-q", remote, "HEAD:refs/pull/7/head")
	git(r.root, "remote", "add", "upstream", remote)

	// aws, answering as account 111111111111.
	fake := t.TempDir()
	os.WriteFile(filepath.Join(fake, "aws"), []byte(`#!/bin/sh
case "$*" in
  *"--query Account"*) echo 111111111111 ;;
  *) echo '{"Account": "111111111111", "Arn": "arn:aws:iam::111111111111:user/op"}' ;;
esac
`), 0o755)
	env := []string{"PATH=" + fake + string(os.PathListSeparator) + os.Getenv("PATH")}

	// This machine's OS and the other one, as the os module names them.
	here, other, hereName, otherWord := "linux", "mac", "Linux", "not macOS"
	if runtime.GOOS == "darwin" {
		here, other, hereName, otherWord = "mac", "linux", "macOS", "not Linux"
	}

	lane := func(body string) {
		os.WriteFile(filepath.Join(r.root, "lane.1.sh"), []byte("#!/usr/bin/env swim\n# swim: syntax 4\n# Round: review\n# Includes: lib/swim/aws.sh lib/swim/gh.sh lib/swim/os.sh\n\nsource lib/swim/aws.sh\nsource lib/swim/gh.sh\nsource lib/swim/os.sh\nstage check\n"+body+"\n"), 0o755)
	}

	lane(`aws_gate
aws_gate 111111111111
gh_pr_fetch 7 upstream
os_` + here + `_gate
if os_` + here + ` && ! os_` + other + ` && ! gh_actions; then step "branched" true; fi
step "the ref" git -C "$SWIM_ROOT" rev-parse --verify "$PR_REF"`)
	out := r.mustSwimEnv(env, "run", "1", "--plain")
	contains(t, "lint", must(r.swim("lint", "1")), "no problems found")
	contains(t, "helpers", out, "PASS  AWS session active", "arn:aws:iam::111111111111:user/op",
		"PASS  AWS session is account 111111111111", "PASS  fetch PR 7 from upstream", "the change under review", "PASS  running on "+hereName, "PASS  branched", "PASS  the ref")
	if strings.Contains(out, "outside a step") {
		t.Errorf("a helper ran a command outside a step:\n%s", out)
	}

	lane(`aws_gate 222222222222
step "never" true`)
	out, code := r.swimEnv(env, "run", "1", "--plain")
	if code == 0 || !strings.Contains(out, "want account 222222222222") || strings.Contains(out, "PASS  never") {
		t.Errorf("wrong account: exit %d\n%s", code, out)
	}

	lane(`gh_pr_fetch 99 upstream
step "never" true`)
	if out, code := r.swimEnv(env, "run", "1", "--plain"); code == 0 || strings.Contains(out, "PASS  never") {
		t.Errorf("a PR the remote doesn't have: exit %d\n%s", code, out)
	}

	lane(`gh_pr_fetch main`)
	if out, code := r.swimEnv(env, "run", "1", "--plain"); code == 0 || !strings.Contains(out, "needs a pull request number") {
		t.Errorf("gh_pr_fetch main: exit %d\n%s", code, out)
	}

	lane("os_" + other + "_gate\nstep \"never\" true")
	if out, code := r.swimEnv(env, "run", "1", "--plain"); code == 0 || !strings.Contains(out, otherWord) || strings.Contains(out, "PASS  never") {
		t.Errorf("os_%s_gate on %s: exit %d\n%s", other, hereName, code, out)
	}

	lane(`gh_actions_gate
step "never" true`)
	if out, code := r.swimEnv(append(env, "GITHUB_ACTIONS="), "run", "1", "--plain"); code == 0 || !strings.Contains(out, "not a GitHub Actions runner") || strings.Contains(out, "PASS  never") {
		t.Errorf("gh_actions_gate off a runner: exit %d\n%s", code, out)
	}
	actions := append(env, "GITHUB_ACTIONS=true", "RUNNER_NAME=gh-1", "RUNNER_OS=Linux", "GITHUB_REPOSITORY=o/r", "GITHUB_RUN_ID=42", "GITHUB_SHA=abc")
	contains(t, "on a runner", r.mustSwimEnv(actions, "run", "1", "--plain"), "runner gh-1 (Linux): o/r run 42 at abc", "PASS  never")
}

// installHelpers copies helper modules into r as lib/swim/<module>.sh.
func installHelpers(t *testing.T, r *repo, modules ...string) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	os.MkdirAll(filepath.Join(r.root, "lib", "swim"), 0o755)
	for _, m := range modules {
		src, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "helpers", m+".sh"))
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(r.root, "lib", "swim", m+".sh"), src, 0o644)
	}
}

// gitAs runs git in dir as a fixed identity (the container's may be empty).
func gitAs(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// The git, docker and tool modules, and gh's auth, repo and PR gates,
// against a local remote and fake gh, docker and node.
func TestHelperModules(t *testing.T) {
	r := newRepo(t)
	installHelpers(t, r, "gh", "git", "docker", "tool")

	remote := t.TempDir()
	gitAs(t, remote, "init", "-q", "--bare")
	gitAs(t, r.root, "commit", "-q", "--allow-empty", "-m", "base")
	gitAs(t, r.root, "remote", "add", "upstream", remote)
	gitAs(t, r.root, "push", "-q", "upstream", "HEAD:refs/heads/main")
	gitAs(t, r.root, "branch", "pushed")
	gitAs(t, r.root, "push", "-q", "upstream", "pushed")
	gitAs(t, r.root, "branch", "unpushed")
	gitAs(t, r.root, "remote", "add", "origin", "git@github.com:gates-brightly/swimlane.git")

	fake := t.TempDir()
	write := func(name, body string) { os.WriteFile(filepath.Join(fake, name), []byte("#!/bin/sh\n"+body), 0o755) }
	write("gh", `case "$*" in
  "auth status") echo "Logged in to github.com account gates-brightly" ;;
  "api user --jq .login") echo gates-brightly ;;
  *"pr view 7 --repo gates-brightly/swimlane --json state,"*) echo "OPEN abc1234def APPROVED" ;;
  *"pr view 7 --repo gates-brightly/swimlane --json headRefOid"*) echo abc1234def ;;
  *) echo "fake gh: $*" >&2; exit 1 ;;
esac
`)
	write("docker", `case "$1" in
  version) echo "Server: Docker Engine 27.0" ;;
  ps) echo "NAMES STATUS IMAGE"; echo "lith-api Up 2h nxb/api" ;;
esac
`)
	write("node", "echo v20.11.1\n")
	env := []string{"PATH=" + fake + string(os.PathListSeparator) + os.Getenv("PATH")}

	lane := func(body string) {
		os.WriteFile(filepath.Join(r.root, "lane.1.sh"), []byte("#!/usr/bin/env swim\n# swim: syntax 4\n# Round: modules\n# Includes: lib/swim/gh.sh lib/swim/git.sh lib/swim/docker.sh lib/swim/tool.sh\n\nsource lib/swim/gh.sh\nsource lib/swim/git.sh\nsource lib/swim/docker.sh\nsource lib/swim/tool.sh\nstage check\n"+body+"\n"), 0o755)
	}
	fails := func(what, body, want string) {
		t.Helper()
		lane(body + "\nstep \"never\" true")
		if out, code := r.swimEnv(env, "run", "1", "--plain"); code == 0 || !strings.Contains(out, want) || strings.Contains(out, "PASS  never") {
			t.Errorf("%s: exit %d, want %q\n%s", what, code, want, out)
		}
	}

	lane(`gh_auth_gate
gh_auth_gate gates-brightly
gh_repo
step "repo" echo "repo=$REPO"
gh_pr_state_gate 7 OPEN DRAFT
gh_pr_head_gate 7 abc1234
git_fetch_gate upstream main
git_worktree .worktrees/bdk-1 bdk-1 upstream/main
step "in the worktree" git -C "$WT" branch --show-current
git_worktree .worktrees/bdk-1 bdk-1 upstream/main
git_pushed_gate pushed upstream
git_authors_gate "t <t@t>" upstream/main..HEAD
git_authors_gate "t <t@t>" HEAD~0..HEAD
docker_gate
docker_compose_snapshot nxb-lith before
tool_gate node 20
tool_gate git`)
	out := r.mustSwimEnv(env, "run", "1", "--plain")
	contains(t, "modules", out,
		"PASS  gh is signed in ", "signed in as gates-brightly", "repo=gates-brightly/swimlane",
		"PR 7 in gates-brightly/swimlane: OPEN abc1234def APPROVED", "PASS  PR 7 is at abc1234",
		"PASS  fetch main from upstream", "PASS  worktree .worktrees/bdk-1 on bdk-1", "is on bdk-1 already",
		"PASS  pushed is on upstream at its local head", "no commits in upstream/main..HEAD",
		"PASS  docker is running", "lith-api", "PASS  node 20", "v20.11.1", "PASS  git")
	if strings.Contains(out, "outside a step") {
		t.Errorf("a helper ran a command outside a step:\n%s", out)
	}
	contains(t, "lint", must(r.swim("lint", "1")), "no problems found")

	gitAs(t, r.root, "commit", "-q", "--allow-empty", "-m", "mine")
	lane(`git_authors_gate "t <t@t>" upstream/main..HEAD`)
	contains(t, "authors", r.mustSwimEnv(env, "run", "1", "--plain"), "1 commits, all by t <t@t>")
	fails("authors", `git_authors_gate "Zach Gates <zach@brightly.com>" upstream/main..HEAD`, "not by Zach Gates <zach@brightly.com>")
	fails("unpushed", `git_pushed_gate unpushed upstream`, "(no such branch)")
	fails("taken branch", `git_worktree .worktrees/other pushed upstream/main`, "branch pushed exists already")
	fails("wrong login", `gh_auth_gate someone-else`, "want someone-else")
	fails("wrong state", `gh_pr_state_gate 7 MERGED`, "want MERGED")
	fails("moved head", `gh_pr_head_gate 7 fff0000`, "want fff0000")
	fails("old node", `tool_gate node 18`, "want node 18, got 20.11.1")
	fails("no tool", `tool_gate no-such-tool`, "no-such-tool: not on PATH")
	fails("not GitHub", `gh_repo upstream`, "isn't a GitHub repo")
}
