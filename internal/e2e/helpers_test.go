package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
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

// installHelpers installs helper modules in r with swim helpers add, as
// lib/swim/<module>.sh.
func installHelpers(t *testing.T, r *repo, modules ...string) {
	t.Helper()
	r.mustSwim(append([]string{"helpers", "add"}, modules...)...)
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
	// go has no --version: it fails and says so, and go version answers.
	write("go", `case "$1" in
  version) echo "go version go1.27.1 darwin/arm64" ;;
  *) echo "flag provided but not defined: -version" >&2; exit 2 ;;
esac
`)
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
tool_gate go 1.27
tool_gate git`)
	out := r.mustSwimEnv(env, "run", "1", "--plain")
	contains(t, "modules", out,
		"PASS  gh is signed in ", "signed in as gates-brightly", "repo=gates-brightly/swimlane",
		"PR 7 in gates-brightly/swimlane: OPEN abc1234def APPROVED", "PASS  PR 7 is at abc1234",
		"PASS  fetch main from upstream", "PASS  worktree .worktrees/bdk-1 on bdk-1", "is on bdk-1 already",
		"PASS  pushed is on upstream at its local head", "no commits in upstream/main..HEAD",
		"PASS  docker is running", "lith-api", "PASS  node 20", "v20.11.1", "PASS  go 1.27", "go version go1.27.1", "PASS  git")
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
	fails("old go", `tool_gate go 1.26`, "want go 1.26, got 1.27.1")
	fails("no tool", `tool_gate no-such-tool`, "no-such-tool: not on PATH")
	fails("not GitHub", `gh_repo upstream`, "isn't a GitHub repo")
}

// swim helpers add writes stamped copies; swim helpers and swim lint say
// how each stands: current, edited since, or from another swim.
func TestHelpersCommand(t *testing.T) {
	r := newRepo(t)
	out := r.mustSwim("helpers")
	contains(t, "list", out, "aws ", "docker ", "gh ", "git ", "os ", "tool ", "in this repo: none")
	contains(t, "show", r.mustSwim("helpers", "gh"), "gh: GitHub", "not in this repo yet: swim helpers add gh",
		"# Includes: lib/swim/gh.sh", "\ngh_pr_fetch N [REMOTE]\n    A gate that fetches pull request N's head", "\ngh_auth_gate [LOGIN]\n")
	contains(t, "show paired", r.mustSwim("helpers", "os"), "\nos_mac / os_linux\n    True on macOS / Linux")
	if out, code := r.swim("helpers", "nope"); code == 0 || !strings.Contains(out, "no helper module") {
		t.Errorf("unknown module: exit %d\n%s", code, out)
	}

	out = r.mustSwim("helpers", "add", "gh", "tool")
	contains(t, "add", out, "wrote lib/swim/gh.sh (gh, from swim ", "# Includes: lib/swim/gh.sh lib/swim/tool.sh", "source lib/swim/tool.sh")
	gh := filepath.Join(r.root, "lib", "swim", "gh.sh")
	data, _ := os.ReadFile(gh)
	if !regexp.MustCompile(`^# swim helpers: gh, from swim \S+, sha256 [0-9a-f]{64}\n# swim helpers, module gh:`).Match(data) {
		t.Fatalf("stamp:\n%.200s", data)
	}
	contains(t, "again", r.mustSwim("helpers", "add", "gh"), "lib/swim/gh.sh: gh is current")
	contains(t, "list copies", r.mustSwim("helpers"), "lib/swim/gh.sh  gh, current", "lib/swim/tool.sh  tool, current")
	contains(t, "show installed", r.mustSwim("helpers", "gh"), "in this repo: lib/swim/gh.sh, gh, current")

	os.WriteFile(filepath.Join(r.root, "lane.1.sh"), []byte("#!/usr/bin/env swim\n# swim: syntax 4\n# Round: r\n# Includes: lib/swim/gh.sh\n\nsource lib/swim/gh.sh\nstage check\nstep \"x\" true\n"), 0o755)
	contains(t, "lint current", must(r.swim("lint", "1")), "no problems found")

	// An edit: lint warns on the helper's file, and add won't overwrite it.
	os.WriteFile(gh, append(data, []byte("# a local tweak\n")...), 0o644)
	contains(t, "lint edited", must(r.swim("lint", "1")), "lib/swim/gh.sh:1", "edited since swim helpers add wrote it", "[helpers]")
	if out, code := r.swim("helpers", "add", "gh"); code == 0 || !strings.Contains(out, "--force replaces the edits") {
		t.Errorf("add over an edit: exit %d\n%s", code, out)
	}
	contains(t, "force", r.mustSwim("helpers", "add", "gh", "--force"), "wrote lib/swim/gh.sh")
	contains(t, "lint restored", must(r.swim("lint", "1")), "no problems found")

	// Another swim's gh, as it wrote it: lint says this swim ships another.
	src := strings.SplitN(string(data), "\n", 2)[1]
	old := strings.Replace(src, "gh_pr_fetch()", "gh_pr_fetch_v0()", 1)
	sum := sha256.Sum256([]byte(old))
	os.WriteFile(gh, []byte("# swim helpers: gh, from swim 0.20260101.1, sha256 "+hex.EncodeToString(sum[:])+"\n"+old), 0o644)
	contains(t, "lint older", must(r.swim("lint", "1")), "gh from swim 0.20260101.1; this swim", "swim helpers add gh updates it")
	contains(t, "update", r.mustSwim("helpers", "add", "gh"), "wrote lib/swim/gh.sh")

	// A file swim didn't write is never overwritten without --force.
	os.WriteFile(filepath.Join(r.root, "lib", "swim", "os.sh"), []byte("echo mine\n"), 0o644)
	if out, code := r.swim("helpers", "add", "os"); code == 0 || !strings.Contains(out, "swim didn't write it") {
		t.Errorf("add over a file of yours: exit %d\n%s", code, out)
	}
	if out, code := r.swim("helpers", "add", "nope"); code == 0 || !strings.Contains(out, "no helper module") {
		t.Errorf("unknown module: exit %d\n%s", code, out)
	}
}

// A step a helper ran shows the call, not the helper's command, and its
// helper: line names the function, the copy and the swim it came from: in
// the log and its YAML. A copy edited since swim helpers add wrote it shows
// the command in full, as does a function the lane defines itself.
func TestHelperProvenance(t *testing.T) {
	r := newRepo(t)
	installHelpers(t, r, "os")
	here := "linux"
	if runtime.GOOS == "darwin" {
		here = "mac"
	}
	lane := func(body string) {
		os.WriteFile(filepath.Join(r.root, "lane.1.sh"), []byte("#!/usr/bin/env swim\n# swim: syntax 4\n# Round: provenance\n# Includes: lib/swim/os.sh\n\nsource lib/swim/os.sh\nstage check\n"+body+"\n"), 0o755)
	}
	lane("os_" + here + "_gate")
	out := r.mustSwim("run", "1", "--plain")
	contains(t, "live", out, "$ os_"+here+"_gate\n")
	log := r.mustSwim("log", "1")
	contains(t, "log", log, "$ os_"+here+"_gate\n", "helper: os_"+here+"_gate, lib/swim/os.sh, os from swim ")
	for name, s := range map[string]string{"live": out, "log": log} {
		if strings.Contains(s, "bash -c") {
			t.Errorf("%s shows the helper's command:\n%s", name, s)
		}
	}
	contains(t, "yaml", r.mustSwim("log", "1", "--yaml"), "helper: os_"+here+"_gate, lib/swim/os.sh, os from swim ")

	f := filepath.Join(r.root, "lib/swim/os.sh")
	data, _ := os.ReadFile(f)
	os.WriteFile(f, append(data, "# edited\n"...), 0o644)
	r.mustSwim("run", "1", "--plain")
	contains(t, "edited", r.mustSwim("log", "1"), "swim _helper os_"+here+"_gate\n", "os edited since swim helpers add wrote it: shown in full")

	// The repo's own module (no stamp), with a function of the same name.
	os.WriteFile(filepath.Join(r.root, "lib/mine.sh"), []byte("os_"+here+"_gate() { gate --helper \"os_"+here+"_gate\" \"mine\" true; }\n"), 0o644)
	os.WriteFile(filepath.Join(r.root, "lane.1.sh"), []byte("#!/usr/bin/env swim\n# swim: syntax 4\n# Round: provenance\n# Includes: lib/mine.sh\n\nsource lib/mine.sh\nstage check\nos_"+here+"_gate\n"), 0o755)
	r.mustSwim("run", "1", "--plain")
	contains(t, "the repo's own", r.mustSwim("log", "1"), "$ true", "lib/mine.sh, not a swim helpers copy: shown in full")
}

// swim helpers stats: each helper's calls and failures across rounds,
// and the checks lanes keep writing by hand (numbers aside) in different
// work: the candidates for a helper. A rerun of one lane isn't a pattern.
func TestHelperStats(t *testing.T) {
	r := newRepo(t)
	contains(t, "empty", r.mustSwim("helpers", "stats"), "no rounds in this repo's lane logs yet")
	installHelpers(t, r, "tool")
	for n, ver := range map[int]string{1: "", 2: " 999"} {
		os.WriteFile(filepath.Join(r.root, "lane."+strconv.Itoa(n)+".sh"), []byte("#!/usr/bin/env swim\n# swim: syntax 4\n# Round: goal "+strconv.Itoa(n)+"\n# Includes: lib/swim/tool.sh\n\nsource lib/swim/tool.sh\nstage check\ngate \"AWS session is sbx"+strconv.Itoa(n)+"\" true\ntool_gate git"+ver+"\n"), 0o755)
	}
	r.swim("run", "1", "2", "--plain")
	r.swim("run", "2", "--plain")
	out := r.mustSwim("helpers", "stats")
	contains(t, "stats", out, "3 rounds in this repo's lane logs", "      3       2  tool_gate", "want git 999, got",
		"candidates for a helper", "       3      2       0  AWS session is sbx2")

	// One lane's reruns: the same goal every time, so not a candidate.
	r2 := newRepo(t)
	os.WriteFile(filepath.Join(r2.root, "lane.1.sh"), []byte("#!/usr/bin/env swim\n# swim: syntax 4\n# Round: one goal\n\nstage check\ngate \"AWS session is sbx1\" true\n"), 0o755)
	for i := 0; i < 3; i++ {
		r2.mustSwim("run", "1", "--plain")
	}
	contains(t, "reruns", r2.mustSwim("helpers", "stats"), "helpers: none ran", "checks written by hand: none repeated")
}
