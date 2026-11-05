package helpers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// The checks behind the modules' gates: a module's function is a line of
// bash that runs one under gate (swim _helper FUNC ARGS...), so the logic
// is Go, tested here, and the log shows the call. A check writes what it
// found to its output and returns nil (the gate passes), a failure (the
// gate fails, exit 1) or misuse (called wrong, exit 2).

// failure is a check that ran and found the wrong thing.
type failure struct{ msg string }

func (f failure) Error() string { return f.msg }

// misuse is a check called wrong: a missing or malformed argument.
type misuse struct{ msg string }

func (m misuse) Error() string { return m.msg }

func failf(format string, a ...any) error   { return failure{fmt.Sprintf(format, a...)} }
func misusef(format string, a ...any) error { return misuse{fmt.Sprintf(format, a...)} }

// env is what a check runs with: its output, and the repo it works in.
type env struct {
	out  io.Writer
	root string // $SWIM_ROOT, else "."
}

// path is p under the repo unless it's absolute.
func (e *env) path(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(e.root, p)
}

// show runs a command with its output in the check's.
func (e *env) show(name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stdout, c.Stderr = e.out, e.out
	return c.Run()
}

// value runs a command for its output, trimmed; on failure its output is
// shown, since it says why.
func (e *env) value(name string, args ...string) (string, error) {
	var b bytes.Buffer
	c := exec.Command(name, args...)
	c.Stdout, c.Stderr = &b, &b
	err := c.Run()
	if err != nil {
		e.out.Write(b.Bytes())
		if ee, ok := err.(*exec.Error); ok {
			return "", failf("%s: %v", name, ee.Err)
		}
		return "", failf("%s %s failed (%v)", name, strings.Join(args, " "), err)
	}
	return strings.TrimSpace(b.String()), nil
}

func (e *env) printf(format string, a ...any) { fmt.Fprintf(e.out, format+"\n", a...) }

// checks are the functions swim _helper runs, by the module function that
// runs each.
var checks = map[string]func(e *env, args []string) error{
	"aws_gate":         awsGate,
	"docker_gate":      dockerGate,
	"gh_actions_gate":  ghActionsGate,
	"gh_auth_gate":     ghAuthGate,
	"gh_pr_state_gate": ghPRStateGate,
	"gh_pr_head_gate":  ghPRHeadGate,
	"git_worktree":     gitWorktree,
	"git_pushed_gate":  gitPushedGate,
	"git_authors_gate": gitAuthorsGate,
	"os_mac_gate":      func(e *env, args []string) error { return osGate(e, args, "darwin", "macOS") },
	"os_linux_gate":    func(e *env, args []string) error { return osGate(e, args, "linux", "Linux") },
	"tool_gate":        toolGate,
}

// Checks are the functions swim _helper runs, sorted.
func Checks() []string {
	var out []string
	for k := range checks {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Run runs check fn with args, its output to out: exit 0 when it passes,
// 1 when it fails, 2 when it's called wrong (or there's no such check).
func Run(fn string, args []string, out io.Writer) int {
	check, ok := checks[fn]
	if !ok {
		fmt.Fprintf(out, "swim _helper: no check %q\n", fn)
		return 2
	}
	root := os.Getenv("SWIM_ROOT")
	if root == "" {
		root = "."
	}
	err := check(&env{out: out, root: root}, args)
	switch err := err.(type) {
	case nil:
		return 0
	case misuse:
		fmt.Fprintln(out, err.msg)
		return 2
	default:
		fmt.Fprintln(out, err.Error())
		return 1
	}
}

var digits = regexp.MustCompile(`^[0-9]+$`)

// aws_gate [ACCOUNT]
func awsGate(e *env, args []string) error {
	if len(args) > 1 {
		return misusef("aws_gate takes at most an account id, got %q", strings.Join(args, " "))
	}
	if len(args) == 1 && (len(args[0]) != 12 || !digits.MatchString(args[0])) {
		return misusef("aws_gate takes a 12-digit account id, got '%s'", args[0])
	}
	id, err := e.value("aws", "sts", "get-caller-identity", "--output", "json")
	if err != nil {
		return err
	}
	e.printf("%s", id)
	if len(args) == 0 {
		return nil
	}
	var who struct{ Account string }
	if err := json.Unmarshal([]byte(id), &who); err != nil {
		return failf("sts get-caller-identity didn't answer in JSON: %v", err)
	}
	profile := os.Getenv("AWS_PROFILE")
	if profile == "" {
		profile = "default"
	}
	e.printf("account %s (profile %s)", who.Account, profile)
	if who.Account != args[0] {
		return failf("want account %s", args[0])
	}
	return nil
}

// docker_gate
func dockerGate(e *env, args []string) error {
	if len(args) > 0 {
		return misusef("docker_gate takes no arguments")
	}
	if err := e.show("docker", "version"); err != nil {
		return failf("docker version failed (%v)", err)
	}
	return nil
}

// gh_actions_gate
func ghActionsGate(e *env, args []string) error {
	if len(args) > 0 {
		return misusef("gh_actions_gate takes no arguments")
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		return failf("not a GitHub Actions runner (GITHUB_ACTIONS is not true)")
	}
	or := func(k string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return "?"
	}
	e.printf("runner %s (%s): %s run %s at %s", or("RUNNER_NAME"), or("RUNNER_OS"), or("GITHUB_REPOSITORY"), or("GITHUB_RUN_ID"), or("GITHUB_SHA"))
	return nil
}

// gh_auth_gate [LOGIN]
func ghAuthGate(e *env, args []string) error {
	switch len(args) {
	case 0:
		if err := e.show("gh", "auth", "status"); err != nil {
			return failf("gh auth status failed (%v)", err)
		}
		return nil
	case 1:
	default:
		return misusef("gh_auth_gate takes at most a login, got %q", strings.Join(args, " "))
	}
	got, err := e.value("gh", "api", "user", "--jq", ".login")
	if err != nil {
		return err
	}
	e.printf("signed in as %s", got)
	if got != args[0] {
		return failf("want %s", args[0])
	}
	return nil
}

// repoFlag takes --repo OWNER/NAME off the front of args (the module
// passes $REPO, which gh_repo sets).
func repoFlag(fn string, args []string) (string, []string, error) {
	if len(args) < 2 || args[0] != "--repo" || args[1] == "" {
		return "", nil, misusef("%s: no repo (gh_repo sets REPO)", fn)
	}
	return args[1], args[2:], nil
}

// prNumber checks a pull request number.
func prNumber(fn, n string) error {
	if !digits.MatchString(n) {
		return misusef("%s needs a pull request number, got '%s'", fn, n)
	}
	return nil
}

// gh_pr_state_gate --repo R N STATE...
func ghPRStateGate(e *env, args []string) error {
	repo, args, err := repoFlag("gh_pr_state_gate", args)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return misusef("gh_pr_state_gate needs a pull request number, got ''")
	}
	if err := prNumber("gh_pr_state_gate", args[0]); err != nil {
		return err
	}
	if len(args) == 1 {
		return misusef("gh_pr_state_gate %s needs the states it may be in (OPEN, MERGED, CLOSED, DRAFT)", args[0])
	}
	jq := `[(if .isDraft then "DRAFT" else .state end), .headRefOid, (.reviewDecision // "")] | join(" ")`
	got, err := e.value("gh", "pr", "view", args[0], "--repo", repo, "--json", "state,isDraft,headRefOid,reviewDecision", "--jq", jq)
	if err != nil {
		return err
	}
	e.printf("PR %s in %s: %s", args[0], repo, got)
	state, _, _ := strings.Cut(got, " ")
	for _, want := range args[1:] {
		if state == want {
			return nil
		}
	}
	return failf("want %s", strings.Join(args[1:], " "))
}

// gh_pr_head_gate --repo R N SHA
func ghPRHeadGate(e *env, args []string) error {
	repo, args, err := repoFlag("gh_pr_head_gate", args)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return misusef("gh_pr_head_gate needs a pull request number, got ''")
	}
	if err := prNumber("gh_pr_head_gate", args[0]); err != nil {
		return err
	}
	if len(args) != 2 || args[1] == "" {
		return misusef("gh_pr_head_gate %s needs the commit it should be at", args[0])
	}
	got, err := e.value("gh", "pr", "view", args[0], "--repo", repo, "--json", "headRefOid", "--jq", ".headRefOid")
	if err != nil {
		return err
	}
	e.printf("PR %s head: %s", args[0], got)
	if !strings.HasPrefix(got, args[1]) {
		return failf("want %s", args[1])
	}
	return nil
}

// git_worktree PATH BRANCH [BASE]
func gitWorktree(e *env, args []string) error {
	if len(args) < 2 || len(args) > 3 || args[0] == "" || args[1] == "" {
		return misusef("git_worktree needs a path and a branch: git_worktree .worktrees/bdk-1328 bdk-1328")
	}
	wt, br, base := e.path(args[0]), args[1], "origin/main"
	if len(args) == 3 {
		base = args[2]
	}
	if _, err := os.Stat(filepath.Join(wt, ".git")); err == nil {
		cur, _ := e.value("git", "-C", wt, "branch", "--show-current")
		if cur == br {
			e.printf("%s is on %s already", wt, br)
			return nil
		}
		if cur == "" {
			cur = "a detached HEAD"
		}
		return failf("%s is a worktree on %s, not %s", wt, cur, br)
	}
	if exec.Command("git", "-C", e.root, "show-ref", "--verify", "--quiet", "refs/heads/"+br).Run() == nil {
		return failf("branch %s exists already (not at %s): pick another branch or path", br, wt)
	}
	if err := e.show("git", "-C", e.root, "worktree", "add", "--no-track", "-b", br, wt, base); err != nil {
		return failf("git worktree add failed (%v)", err)
	}
	return nil
}

// git_pushed_gate BRANCH [REMOTE]
func gitPushedGate(e *env, args []string) error {
	if len(args) < 1 || len(args) > 2 || args[0] == "" {
		return misusef("git_pushed_gate needs a branch")
	}
	remote := "origin"
	if len(args) == 2 {
		remote = args[1]
	}
	here, err := e.value("git", "-C", e.root, "rev-parse", "--verify", "refs/heads/"+args[0])
	if err != nil {
		return err
	}
	ls, err := e.value("git", "-C", e.root, "ls-remote", remote, "refs/heads/"+args[0])
	if err != nil {
		return err
	}
	there, _, _ := strings.Cut(ls, "\t")
	shown := there
	if shown == "" {
		shown = "(no such branch)"
	}
	e.printf("local %s, %s %s", here, remote, shown)
	if here != there {
		return failf("%s on %s isn't at the local head", args[0], remote)
	}
	return nil
}

// git_authors_gate "NAME <EMAIL>" RANGE [PATH]
func gitAuthorsGate(e *env, args []string) error {
	if len(args) < 2 || len(args) > 3 || args[0] == "" || args[1] == "" {
		return misusef(`git_authors_gate needs an identity and a range: git_authors_gate "Name <email>" origin/main..HEAD`)
	}
	dir := e.root
	if len(args) == 3 {
		dir = e.path(args[2])
	}
	out, err := e.value("git", "-C", dir, "log", "--format=%h %an <%ae> | %cn <%ce>", args[1])
	if err != nil {
		return err
	}
	if out == "" {
		e.printf("no commits in %s", args[1])
		return nil
	}
	lines := strings.Split(out, "\n")
	var bad []string
	for _, l := range lines {
		h, rest, _ := strings.Cut(l, " ")
		if rest != args[0]+" | "+args[0] {
			bad = append(bad, h+" "+rest)
		}
	}
	if len(bad) == 0 {
		e.printf("%d commits, all by %s", len(lines), args[0])
		return nil
	}
	e.printf("not by %s:", args[0])
	for _, b := range bad {
		e.printf("%s", b)
	}
	return failf("%d of %d commits in %s not by %s", len(bad), len(lines), args[1], args[0])
}

// os_mac_gate / os_linux_gate
func osGate(e *env, args []string, goos, name string) error {
	if len(args) > 0 {
		return misusef("os_%s_gate takes no arguments", map[string]string{"darwin": "mac", "linux": "linux"}[goos])
	}
	if u, err := e.value("uname", "-srm"); err == nil {
		e.printf("%s", u)
	}
	if runtime.GOOS != goos {
		return failf("not %s", name)
	}
	return nil
}

var versionNumber = regexp.MustCompile(`[0-9]+(\.[0-9]+)*`)

// tool_gate CMD [VERSION]
func toolGate(e *env, args []string) error {
	if len(args) < 1 || len(args) > 2 || args[0] == "" {
		return misusef("tool_gate needs a command name")
	}
	name := args[0]
	p, err := exec.LookPath(name)
	if err != nil {
		return failf("%s: not on PATH", name)
	}
	// go has no --version (it exits 2): ask `CMD version` when it fails.
	v, err := exec.Command(name, "--version").CombinedOutput()
	if err != nil {
		v, _ = exec.Command(name, "version").CombinedOutput()
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(v)), "\n")
	e.printf("%s: %s", p, first)
	if len(args) == 1 {
		return nil
	}
	want := args[1]
	n := versionNumber.FindString(first)
	if n == want || strings.HasPrefix(n, want+".") {
		return nil
	}
	if n == "" {
		n = "no version"
	}
	return failf("want %s %s, got %s", name, want, n)
}
