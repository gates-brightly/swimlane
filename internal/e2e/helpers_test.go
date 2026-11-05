package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// helpers/ops.sh, included and sourced by a lane: pr_fetch fetches a pull
// request's head and logs the commit, aws_gate stops a round on the wrong
// account or no session, and a helper called wrong stops it too.
func TestOpsHelpers(t *testing.T) {
	r := newRepo(t)
	_, file, _, _ := runtime.Caller(0)
	src, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "helpers", "ops.sh"))
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(r.root, "lib"), 0o755)
	os.WriteFile(filepath.Join(r.root, "lib", "swim-ops.sh"), src, 0o644)

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

	lane := func(body string) {
		os.WriteFile(filepath.Join(r.root, "lane.1.sh"), []byte("#!/usr/bin/env swim\n# swim: syntax 4\n# Round: review\n# Includes: lib/swim-ops.sh\n\nsource lib/swim-ops.sh\nstage check\n"+body+"\n"), 0o755)
	}

	lane(`aws_gate
aws_gate 111111111111
pr_fetch 7 upstream
step "the ref" git -C "$SWIM_ROOT" rev-parse --verify "$PR_REF"`)
	out := r.mustSwimEnv(env, "run", "1", "--plain")
	contains(t, "lint", must(r.swim("lint", "1")), "no problems found")
	contains(t, "helpers", out, "PASS  AWS session active", "arn:aws:iam::111111111111:user/op",
		"PASS  AWS session is account 111111111111", "PASS  fetch PR 7 from upstream", "the change under review", "PASS  the ref")
	if strings.Contains(out, "outside a step") {
		t.Errorf("a helper ran a command outside a step:\n%s", out)
	}

	lane(`aws_gate 222222222222
step "never" true`)
	out, code := r.swimEnv(env, "run", "1", "--plain")
	if code == 0 || !strings.Contains(out, "want account 222222222222") || strings.Contains(out, "PASS  never") {
		t.Errorf("wrong account: exit %d\n%s", code, out)
	}

	lane(`pr_fetch 99 upstream
step "never" true`)
	if out, code := r.swimEnv(env, "run", "1", "--plain"); code == 0 || strings.Contains(out, "PASS  never") {
		t.Errorf("a PR the remote doesn't have: exit %d\n%s", code, out)
	}

	lane(`pr_fetch main`)
	if out, code := r.swimEnv(env, "run", "1", "--plain"); code == 0 || !strings.Contains(out, "needs a pull request number") {
		t.Errorf("pr_fetch main: exit %d\n%s", code, out)
	}
}
