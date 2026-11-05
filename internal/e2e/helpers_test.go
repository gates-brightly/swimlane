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
	_, file, _, _ := runtime.Caller(0)
	os.MkdirAll(filepath.Join(r.root, "lib", "swim"), 0o755)
	for _, m := range []string{"aws", "gh", "os"} {
		src, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "helpers", m+".sh"))
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(r.root, "lib", "swim", m+".sh"), src, 0o644)
	}

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
os_`+here+`_gate
if os_`+here+` && ! os_`+other+` && ! gh_actions; then step "branched" true; fi
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
