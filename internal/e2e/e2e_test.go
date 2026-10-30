// Package e2e drives the built swim binary against a scratch repo.
package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	// The tests run a built binary; importing its code makes `go test`
	// rerun them (instead of reusing a cached pass) when that code changes.
	_ "github.com/gates-brightly/swimlane/internal/cli"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/launcher"
	"github.com/gates-brightly/swimlane/internal/lint"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/testwait"
	"github.com/gates-brightly/swimlane/internal/version"
	"gopkg.in/yaml.v3"
)

var bin string

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "stamp" && os.Getenv(ledgerEnv) != "" {
		stampMain(os.Args[2:]) // a lane's timestamp (fault_test.go); exits
	}
	hermetic()
	dir, err := os.MkdirTemp("", "swim-e2e-bin")
	if err != nil {
		panic(err)
	}
	bin = filepath.Join(dir, "swim")
	_, file, _, _ := runtime.Caller(0)
	build := exec.Command("go", "build", "-buildvcs=false", "-o", bin, "./cmd/swim")
	build.Dir = filepath.Join(filepath.Dir(file), "..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build failed: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// hermetic strips what the suite may inherit when it runs inside a swim
// lane (this repo's own CI runs it through swim ci), a CI runner or a
// developer's shell: the lane's SWIM_* variables and STEP_LOG, the git shim
// older swims put first on a lane's PATH (it refused git commit and stopped
// the outer lane), the CI provider's variables (tests set the ones they need;
// inherited, they would change provider detection, turn the TUI off and
// write to the runner's real job summary), and testEnvKeys.
func hermetic() {
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case strings.HasPrefix(k, "SWIM_"), strings.HasPrefix(k, "GITHUB_"), strings.HasPrefix(k, "GITLAB_"),
			strings.HasPrefix(k, "CI_"), k == "CI", k == "STEP_LOG", k == "XDG_CONFIG_HOME", testEnvKeys[k]:
			os.Unsetenv(k)
		}
	}
	var path []string
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if !strings.HasSuffix(filepath.ToSlash(p), "/.swim/bin") {
			path = append(path, p)
		}
	}
	os.Setenv("PATH", strings.Join(path, string(os.PathListSeparator)))
}

// testEnvKeys are names the tests give values of their own (env files, a
// lane's shell, guard flags). A developer's shell may export them (STAGE,
// OWNER, ...), and swim lets the shell win, so they're cleared first.
var testEnvKeys = map[string]bool{"STAGE": true, "REGION": true, "PROFILE": true, "TABLE": true, "OWNER": true,
	"GENERAL_ONLY": true, "SHELL_ONLY": true, "ALLOW_X": true, "AWS_PROFILE": true, "AWS_REGION": true}

// killLanes kills every lane still recorded in root's .swim/lane*.pid,
// with its process group (the lane's steps).
func killLanes(root string) {
	files, _ := filepath.Glob(filepath.Join(root, ".swim", "lane*.pid"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var pid int
		if _, err := fmt.Sscan(string(data), &pid); err != nil || pid <= 1 {
			continue
		}
		syscall.Kill(-pid, syscall.SIGKILL)
		syscall.Kill(pid, syscall.SIGKILL)
	}
}

// parallel runs a top-level test alongside the others: each test drives
// swim in its own scratch repo and config, so most of the suite's time is
// processes waiting, which overlaps well. newRepo calls it, so a test opts
// in by making a repo; subtests, tests in serialTests, tests that use
// newSerialRepo and runs with SWIM_E2E_SERIAL=1 stay serial. Safe to call
// more than once per test.
func parallel(t *testing.T) {
	if strings.Contains(t.Name(), "/") || serialTests[t.Name()] || os.Getenv("SWIM_E2E_SERIAL") != "" {
		return
	}
	if _, done := parallelTests.LoadOrStore(t, true); done {
		return
	}
	t.Parallel()
}

var parallelTests sync.Map

// newSerialRepo is newRepo for a test that runs alone: the PTY tests, which
// wait for a terminal screen to redraw and miss it on a loaded machine.
func newSerialRepo(t *testing.T, deps string) *repo {
	t.Helper()
	parallelTests.LoadOrStore(t, true) // parallel() then leaves it serial
	return newRepo(t, deps)
}

// serialTests run alone: their checks time things tightly enough that a
// loaded machine fails them.
var serialTests = map[string]bool{}

type repo struct {
	t    *testing.T
	root string
	env  []string
}

func newRepo(t *testing.T, deps string) *repo {
	t.Helper()
	parallel(t)
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	xdg := t.TempDir()
	r := &repo{t: t, root: root, env: append(os.Environ(),
		"XDG_CONFIG_HOME="+xdg,
		"PATH="+filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"),
		"NO_COLOR=1", "SWIM_ROOT=", "SWIM_LANE=", "STEP_LOG=", "SWIM_BIN=",
		// Every status.yml write is checked against the lane state
		// machine: an illegal move panics (status.Transition).
		"SWIM_CHECK_STATES=1",
	)}
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	// A test that fails mid-run, or crashes swim on purpose, must not leave
	// lanes behind: they run in their own process groups and would outlive
	// it (looping on files whose directory is about to go). Registered after
	// t.TempDir, so it runs before the directory is removed.
	t.Cleanup(func() { killLanes(root) })
	r.watch(root)
	// Never sign with the developer's own ~/.ssh key in tests (nor let swim
	// init list it in lookup.yml); the signing tests set their own keys.
	r.mustSwim("config", "signing", "off")
	r.mustSwim("init")
	if deps != "" {
		cfg := filepath.Join(xdg, "swim", "config.yml")
		data, _ := os.ReadFile(cfg)
		data = bytes.Replace(data, []byte("deps: {}"), []byte("deps: "+deps), 1)
		os.WriteFile(cfg, data, 0o644)
	}
	return r
}

func (r *repo) cmd(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.Dir = r.root
	c.Env = r.env
	return c
}

func (r *repo) swim(args ...string) (string, int) {
	out, err := r.cmd(bin, args...).CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		r.t.Fatalf("swim %v: %v", args, err)
	}
	return string(out), code
}

func (r *repo) mustSwim(args ...string) string {
	r.t.Helper()
	out, code := r.swim(args...)
	if code != 0 {
		r.t.Fatalf("swim %v exited %d:\n%s", args, code, out)
	}
	return out
}

// mustDryRun runs swim and requires exit ExitDryRun: nothing failed, but a
// guarded step dry-ran.
func (r *repo) mustDryRun(args ...string) string {
	r.t.Helper()
	out, code := r.swim(args...)
	if code != launcher.ExitDryRun {
		r.t.Fatalf("swim %v exited %d, want %d (a dry run):\n%s", args, code, launcher.ExitDryRun, out)
	}
	return out
}

// newLane is swim new N ..., with the template's placeholder snapshot
// filled in, as an author would before running it.
func (r *repo) newLane(n string, args ...string) string {
	r.t.Helper()
	out := r.mustSwim(append([]string{"new", n}, args...)...)
	p := filepath.Join(r.root, "lane."+n+".sh")
	data, err := os.ReadFile(p)
	if err != nil {
		r.t.Fatal(err)
	}
	os.WriteFile(p, []byte(strings.Replace(string(data), `echo "replace with a read-only describe/list command"`, `echo "nothing to describe"`, 1)), 0o755)
	return out
}

// lane script writes lane.N.sh with the given round and body, with the
// header lines its body needs (# Guards:, # Includes:) added as swim lint
// --fix adds them: a lane as an author would leave it. A test of what
// happens without them writes the file itself.
func (r *repo) script(n int, round, body string) {
	r.t.Helper()
	s := fmt.Sprintf("#!/usr/bin/env bash\n# swim: syntax 2\n# Round: %s\n_swim_lib=$(\"${SWIM_BIN:-swim}\" lib) || exit 1; eval \"$_swim_lib\"\nlane_init %d\n%s\nsummary\n", round, n, body)
	if err := os.WriteFile(filepath.Join(r.root, fmt.Sprintf("lane.%d.sh", n)), []byte(s), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if _, err := lint.Fix(r.root, &config.Config{Root: r.root, Settings: config.Settings{Lanes: n}}, []int{n}); err != nil {
		r.t.Fatal(err)
	}
}

func (r *repo) log(n int) string {
	data, _ := os.ReadFile(filepath.Join(r.root, ".swim", "logs", fmt.Sprintf("agent%d.log", n)))
	return string(data)
}

func (r *repo) status() *status.File {
	r.t.Helper()
	f, err := status.Load(r.root)
	if err != nil {
		r.t.Fatal(err)
	}
	return f
}

// must drops swim's exit code, for checks on its output alone.
func must(out string, _ int) string { return out }

func contains(t *testing.T, what, s string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(s, w) {
			t.Errorf("%s missing %q:\n%s", what, w, s)
		}
	}
}

func TestInitIsIdempotent(t *testing.T) {
	r := newRepo(t, "")
	out := r.mustSwim("init")
	contains(t, "second init", out, "already configured", "already has the swim block")
	gi, _ := os.ReadFile(filepath.Join(r.root, ".gitignore"))
	if strings.Count(string(gi), "# >>> swim >>>") != 1 {
		t.Fatalf(".gitignore:\n%s", gi)
	}
	// No lanes yet: status shows none.
	if out := r.mustSwim("status"); strings.Contains(out, "swim 1") {
		t.Fatalf("status of a repo with no lanes:\n%s", out)
	}
}

func TestFullRunWithDependencies(t *testing.T) {
	r := newRepo(t, "{2: [1], 4: [2]}")
	r.script(1, "Lane one", `snapshot "state" echo state-v1
run "slow" `+hold("go"))
	r.script(2, "Lane two", `run "probe" echo probing
if guard TEST_ALLOW_DELETE "delete thing (2026-10-09: test)"; then run "delete" echo deleting; fi
drift "policy differs"`)
	r.script(3, "Lane three", `run "fails" bash -c 'echo nope >&2; exit 4'
run "keeps going" true
gate "stops" false
run "never" true`)
	r.script(4, "Lane four", `run "after" echo after`)

	c := r.cmd(bin, "run")
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}

	// Mid-run: lane 1 is running a step, lanes 2 and 4 wait on their deps.
	var mid *status.File
	waitFor(t, func() bool {
		f, err := status.Load(r.root)
		if err == nil && len(f.Lanes) == 4 && f.Get(1).CurrentStep == "slow" && f.Get(2).State == status.Waiting {
			mid = f
		}
		return mid != nil
	})
	r.release("go")
	if mid.Get(1).State != status.Running || fmt.Sprint(mid.Get(2).WaitingOn) != "[1]" || fmt.Sprint(mid.Get(4).WaitingOn) != "[2]" {
		t.Errorf("mid-run status: 1=%+v 2=%+v 4=%+v", mid.Get(1), mid.Get(2), mid.Get(4))
	}

	err := c.Wait()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
		t.Fatalf("launcher exit: %v\n%s", err, out.String())
	}
	s := out.String()
	contains(t, "launcher output", s,
		"[2] waiting on swim 1", "[4] waiting on swim 2",
		"[2] SKIP  delete thing (2026-10-09: test) (dry run: set TEST_ALLOW_DELETE=1 to approve)",
		"to approve: TEST_ALLOW_DELETE=1 swim run 2",
		"swim summary", "FAIL  fails (exit 4)", "STOP  gate failed: stops")
	if strings.Contains(s, "never") {
		t.Errorf("step after a failed gate ran:\n%s", s)
	}

	f := r.status()
	// Lane 2 dry-ran its guarded step: skipped, not passed, so lane 4 after
	// it never started.
	want := map[int]string{1: status.Passed, 2: status.Skipped, 3: status.Failed, 4: status.Skipped}
	for n, st := range want {
		if got := f.Get(n).State; got != st {
			t.Errorf("swim %d state = %s, want %s", n, got, st)
		}
	}
	if l := f.Get(2); l.Skip != 1 || l.Drift != 1 || l.Pass != 1 {
		t.Errorf("swim 2 counts: %+v", l)
	}
	if l := f.Get(3); *l.ExitCode != 1 || len(l.FailedSteps) != 3 {
		t.Errorf("swim 3: %+v", l)
	}

	log := r.log(1)
	contains(t, "agent1.log", log, "# swim lane log | syntax 2 | swim 1", "== ROUND ", "  Lane one\n", "  PASS  snapshot: state", "        saved: .swim/snapshots/lane1-", "  PASS  slow", "== END PASS", "exit=0")
	snaps, _ := filepath.Glob(filepath.Join(r.root, ".swim", "snapshots", "lane1-*-state.txt"))
	if len(snaps) != 1 {
		t.Fatalf("snapshots: %v", snaps)
	}
	if data, _ := os.ReadFile(snaps[0]); string(data) != "state-v1\n" {
		t.Errorf("snapshot content %q", data)
	}

	// status --yaml is the file's content with a schema tag, plus each
	// pending lane's header and dependencies. It refreshes the file first
	// (a new updated_at), so the file is read after it.
	got := r.mustSwim("status", "--yaml")
	contains(t, "status --yaml", got, "pending_header:", "deps:")
	got = withoutKeys(got, "pending_header", "deps")
	raw, _ := os.ReadFile(status.Path(r.root))
	var doc, file map[string]any // status.yml is JSON, which reads as YAML
	yaml.Unmarshal([]byte(got), &doc)
	yaml.Unmarshal(raw, &file)
	if doc["schema"] != "swim.status/v1" || !reflect.DeepEqual(withoutKey(doc, "schema"), file) {
		t.Errorf("status --yaml should be the file plus its schema:\n%s\n---\n%s", got, raw)
	}
	contains(t, "status", r.mustSwim("status"), "swim 1", "PASS", "swim 3", "FAIL exit 1", "STOP  gate failed: stops")
}

func TestSkipPropagatesDownChain(t *testing.T) {
	r := newRepo(t, "{2: [1], 4: [2]}")
	r.script(1, "fails", `run "x" false`)
	r.script(2, "two", `run "y" true`)
	r.script(4, "four", `run "z" true`)
	out, code := r.swim("run")
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	contains(t, "output", out, "[2] SKIP (swim 1 failed)", "[4] SKIP (swim 2 skipped)")
	f := r.status()
	if f.Get(2).State != status.Skipped || f.Get(4).State != status.Skipped || f.Get(4).Reason != "swim 2 skipped" {
		t.Fatalf("status: 2=%+v 4=%+v", f.Get(2), f.Get(4))
	}
	// A skipped lane records the round it skipped, so its next run is a retry, not a new job.
	if f.Get(4).Round != "four" || f.Get(4).FinishedAt == nil {
		t.Errorf("skipped lane should record its round: %+v", f.Get(4))
	}
	if r.log(2) != "" {
		t.Error("skipped lane wrote a log")
	}
}

// A round that only dry-ran its guarded change didn't do its work: it's
// skipped, not passed, and a lane after it doesn't start.
func TestDryRunIsSkippedNotPassed(t *testing.T) {
	r := newRepo(t, "{2: [1]}")
	r.script(1, "change", `stage change
if guard TEST_ALLOW_X "do x (2026-10-27: test)"; then run "do x" echo did-x; fi`)
	r.script(2, "after", `run "assumes x" true`)
	out, code := r.swim("run", "--no-prompt")
	if code != launcher.ExitDryRun {
		t.Fatalf("exit %d\n%s", code, out)
	}
	contains(t, "output", out, "dry run: do x", "[2] SKIP (swim 1 skipped)")
	contains(t, "agent1.log", r.log(1), "== END SKIP  pass=0 fail=0 skip=1")
	f := r.status()
	if l := f.Get(1); l.State != status.Skipped || l.Reason != "dry run: do x" {
		t.Fatalf("status 1: %+v", l)
	}
	if r.log(2) != "" {
		t.Error("lane after a dry run started")
	}
	// Approved, the round passes and the lane after it runs.
	c := r.cmd(bin, "run")
	c.Env = append(c.Env, "TEST_ALLOW_X=1")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("approved run: %v\n%s", err, out)
	}
	if f := r.status(); f.Get(1).State != status.Passed || f.Get(2).State != status.Passed {
		t.Fatalf("after approval: 1=%+v 2=%+v", f.Get(1), f.Get(2))
	}
}

func TestRunSelectedLanesOnly(t *testing.T) {
	r := newRepo(t, "{2: [1]}")
	for n := 1; n <= 3; n++ {
		r.script(n, fmt.Sprintf("lane %d", n), `run "ok" true`)
	}
	// `swim 3 2` is shorthand for `swim run 3 2`. Lane 2's dependency (1) is
	// not in the run and holds a round that hasn't passed: lane 2 is skipped.
	out, code := r.swim("3", "2")
	if code != 1 || r.log(1) != "" || r.log(2) != "" || r.log(3) == "" {
		t.Fatalf("wrong lanes ran (exit %d):\n%s", code, out)
	}
	contains(t, "output", out, "[2] SKIP (swim 1's pending round has not passed; run it too: swim run 1 ...)")

	// Once lane 1's round has passed, lane 2 may run on its own.
	r.mustSwim("1")
	out = r.mustSwim("2")
	if strings.Contains(out, "waiting on") {
		t.Errorf("lane waited on a dependency outside the run:\n%s", out)
	}
	// A stubbed dependency never blocks.
	r.mustSwim("stub", "1", "done")
	r.mustSwim("run", "2")

	if out, code := r.swim("run", "100"); code == 0 || !strings.Contains(out, "lanes are numbered 1..99") {
		t.Errorf("run 9: %d %s", code, out)
	}
}

func TestAfterHeaderDiamond(t *testing.T) {
	r := newRepo(t, "") // no config deps: the scripts declare them
	r.script(1, "root", `run "write" bash -c '`+untilReleased("root-go")+`; echo root > "$SWIM_ROOT/out1"'`)
	r.script(2, "child a", `gate "root output" test -s "$SWIM_ROOT/out1"`)
	r.script(3, "child b", `gate "root output" test -s "$SWIM_ROOT/out1"`)
	r.script(4, "join", `gate "children" true`)
	addHeader := func(n int, line string) {
		p := filepath.Join(r.root, fmt.Sprintf("lane.%d.sh", n))
		data, _ := os.ReadFile(p)
		os.WriteFile(p, []byte(strings.Replace(string(data), "\n", "\n"+line+"\n", 1)), 0o755)
	}
	addHeader(2, "# After: 1")
	addHeader(3, "# After: swim 1")
	addHeader(4, "# After: 2, 3")

	all := start(t, r.cmd(bin, "all"), nil) // every pending lane, like `swim run`
	// The root holds until its dependents have said what they wait on.
	all.saw(t, "[2] waiting on swim 1")
	all.saw(t, "[4] waiting on swim 2, 3")
	r.release("root-go")
	if code := all.wait(t); code != 0 {
		t.Fatalf("swim all: exit %d\n%s", code, all.out.String())
	}
	out := all.out.String()
	if out, code := r.swim("all", "2"); code == 0 || !strings.Contains(out, "all takes no lanes") {
		t.Errorf("all with a lane: %d %s", code, out)
	}
	for n := 1; n <= 4; n++ {
		if st := r.status().Get(n).State; st != status.Passed {
			t.Errorf("swim %d = %s\n%s", n, st, out)
		}
	}

	// A cycle is refused before anything runs.
	addHeader(1, "# After: 4")
	if out, code := r.swim("all", "--rerun"); code == 0 || !strings.Contains(out, "dependency cycle") || r.status().Get(1).State != status.Passed {
		t.Errorf("cycle: %d %s", code, out)
	}
}

func TestAfterPinnedToJob(t *testing.T) {
	r := newRepo(t, "")
	r.newLane("1", "root", "--job", "root-job-0001")
	r.script(2, "child", `run "ok" true`)
	p := filepath.Join(r.root, "lane.2.sh")
	data, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(data), "\n", "\n# After: root-jo\n", 1)), 0o755)

	// A prefix shorter than 8 characters doesn't resolve.
	if out, code := r.swim("run", "2"); code == 0 || !strings.Contains(out, "no lane holds or last ran job root-jo") {
		t.Fatalf("short ref: %d %s", code, out)
	}
	os.WriteFile(p, []byte(strings.Replace(string(data), "\n", "\n# After: root-job-0001\n", 1)), 0o755)
	// The pinned job hasn't run: lane 2 alone is skipped.
	if out, code := r.swim("run", "2"); code != 1 || !strings.Contains(out, "swim 1 (job root-job) has not passed") {
		t.Fatalf("unrun pinned dep: %d %s", code, out)
	}
	r.mustSwim("run", "1")
	r.mustSwim("run", "2")
	// After lane 1 is rewritten, the pin still refers to the job that ran
	// (from status), which passed.
	r.newLane("1", "next root", "--force")
	r.mustSwim("run", "2")
}

func TestGuardApprovedAndConfirmFailsClosed(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "guarded", `if guard TEST_ALLOW_X "do x (2026-10-09: test)"; then run "do x" echo did-x; fi
confirm "really?"
run "after confirm" true`)
	c := r.cmd(bin, "run", "1")
	c.Env = append(c.Env, "TEST_ALLOW_X=1")
	c.Stdin = strings.NewReader("") // operator gives no answer
	out, err := c.CombinedOutput()
	if err == nil {
		t.Fatalf("expected failure, got:\n%s", out)
	}
	log := r.log(1)
	contains(t, "agent1.log", log, "APPROVED  do x (2026-10-09: test) (TEST_ALLOW_X=1)", "PASS  do x", "STOP  not confirmed: really?")
	if strings.Contains(log, "after confirm") {
		t.Error("round continued after an unconfirmed prompt")
	}
}

func TestArchiveStubNewAndRunningRefusals(t *testing.T) {
	long(t)
	r := newRepo(t, "")
	r.newLane("1", "first round")
	if out, code := r.swim("new", "1", "second"); code == 0 || !strings.Contains(out, "pending round") {
		t.Fatalf("new over pending: %d %s", code, out)
	}
	if f := r.status(); f.Get(1).Pending != "first round" {
		t.Errorf("pending = %q", f.Get(1).Pending)
	}
	r.mustSwim("run", "1")
	r.mustSwim("archive", "1", "first round")
	if _, err := os.Stat(filepath.Join(r.root, ".swim", "logs", "agent1.prev-first-round.log")); err != nil {
		t.Fatal(err)
	}
	r.mustSwim("stub", "1", "all done")
	if l := r.status().Get(1); l.State != status.Idle || l.Pending != "" || l.LastArchive != "agent1.prev-first-round.log" {
		t.Errorf("after archive+stub: %+v", l)
	}
	if out, code := r.swim("run", "1"); code == 0 || !strings.Contains(out, "no pending round") {
		t.Errorf("run stub: %d %s", code, out)
	}
	if out := r.mustSwim("run"); !strings.Contains(out, "nothing pending") {
		t.Errorf("run with only stubs: %s", out)
	}

	// While a lane runs, nothing may touch its files.
	r.script(2, "long", `run "sleep" `+hold("go"))
	bg := r.cmd("bash", "lane.2.sh")
	if err := bg.Start(); err != nil {
		t.Fatal(err)
	}
	defer bg.Wait()
	defer r.release("go")
	waitFor(t, func() bool { l := r.status().Get(2); return l != nil && l.State == status.Running })
	os.WriteFile(filepath.Join(r.root, "agent2.log.keep"), nil, 0o644)
	for _, args := range [][]string{{"archive", "2", "x"}, {"stub", "2", "x"}, {"new", "2", "x", "--force"}, {"run", "2"}} {
		if out, code := r.swim(args...); code == 0 || !strings.Contains(out, "running") {
			t.Errorf("swim %v on running lane: %d %s", args, code, out)
		}
	}
	if out, err := r.cmd("bash", "lane.2.sh").CombinedOutput(); err == nil || !strings.Contains(string(out), "already running") {
		t.Errorf("second copy of a running lane script: %v %s", err, out)
	}
}

func TestInterruptRecordsAndStopsRound(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "interrupt me", `run "long" `+blocked("long")+`
run "never" true`)
	c := r.cmd("bash", "lane.1.sh")
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	r.waitStep(t, 1, "long")
	// What a terminal does on Ctrl-C: SIGINT to the whole foreground group.
	syscall.Kill(-c.Process.Pid, syscall.SIGINT)
	err := c.Wait()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 130 {
		t.Fatalf("lane script exit: %v", err)
	}
	log := r.log(1)
	contains(t, "agent1.log", log, "  FAIL  long (exit 130, interrupted)", "== END INTERRUPTED", "exit=130")
	if strings.Contains(log, "never") {
		t.Error("round continued after Ctrl-C")
	}
	l := r.status().Get(1)
	if l.State != status.Interrupted || *l.ExitCode != 130 {
		t.Errorf("status: %+v", l)
	}
	if _, err := os.Stat(filepath.Join(r.root, ".swim", "lane1.pid")); err == nil {
		t.Error("pidfile left behind")
	}
}

func TestStepStandalone(t *testing.T) {
	r := newRepo(t, "")
	c := r.cmd(bin, "step", "--new", "--label", "hello", "--", "bash", "-c", "echo hi; exit 7")
	c.Env = append(c.Env, "STEP_LOG=custom.log")
	out, err := c.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 7 {
		t.Fatalf("exit: %v\n%s", err, out)
	}
	data, _ := os.ReadFile(filepath.Join(r.root, "custom.log"))
	contains(t, "custom.log", string(data), "        $ bash -c 'echo hi; exit 7'", "        | hi\n", "  FAIL  hello (exit 7)")
}

func TestStaleRunningShown(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "killed", `run "long" `+blocked("long")+``)
	c := r.cmd("bash", "lane.1.sh")
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return laneOf(r, 1).CurrentStep == "long" })
	syscall.Kill(-c.Process.Pid, syscall.SIGKILL) // terminal closed: no traps run
	c.Wait()
	contains(t, "status", r.mustSwim("status"), "running? (process gone: interrupted)", "step: long")
	// A dead pidfile doesn't block a rerun.
	r.script(1, "rerun", `run "quick" true`)
	r.mustSwim("run", "1")
	r.mustSwim("status", "--rebuild")
	if l := r.status().Get(1); l.State != status.Passed || l.Round != "rerun" {
		t.Errorf("after rebuild: %+v", l)
	}
}

func TestHelp(t *testing.T) {
	r := newRepo(t, "")
	// Piped or not, --help is the short overview; --help --full the guide.
	short := r.mustSwim("--help")
	contains(t, "--help", short, "CORE", "swim guide")
	if strings.Contains(short, "PLANNER WORKFLOW") || strings.Count(short, "\n") >= 80 {
		t.Errorf("piped --help printed the guide:\n%s", short)
	}
	contains(t, "--help --full", r.mustSwim("--help", "--full"), "PLANNER WORKFLOW", "WORKER RULES", "SAFETY RULES", "READING RESULTS", "COMMAND DETAIL")
	contains(t, "guide", r.mustSwim("guide"), "PLANNER WORKFLOW", "COMMAND DETAIL")
	contains(t, "run --help", r.mustSwim("run", "--help"), "waiting on swim N")
	contains(t, "help status", r.mustSwim("help", "status"), "--rebuild")
	if out, code := r.swim("bogus"); code != 2 || !strings.Contains(out, "unknown command") {
		t.Errorf("bogus: %d %s", code, out)
	}
}

// Timing. A test waits for an event (a status state, a log line, a file),
// never for a fixed time, and waitFor's deadline is only how long before a
// hung swim fails the test: it costs nothing while things work, so it's
// generous. A step the test must see running is hold(name), not a sleep
// the test has to beat. Where a test times something on purpose (a round's
// Timeout, a step limit, an upper bound on how long a stop takes), the
// margin is scaled(d): in CI the suite runs beside the fault, security and
// fuzz lanes on a 3-4 CPU runner, where an idle machine's margins fail.
// E2E_SLOW sets the factor (default 1; this repo's CI lanes set 3); scale
// a margin, never hand-tune one test's constant.
var slowFactor = func() time.Duration {
	if n, err := strconv.Atoi(os.Getenv("E2E_SLOW")); err == nil && n > 1 {
		return time.Duration(n)
	}
	return 1
}()

// scaled is d times E2E_SLOW.
func scaled(d time.Duration) time.Duration { return d * slowFactor }

// dur is scaled(d) as a swim duration ("3s", "1.5s"), for a header or flag
// and the log line that echoes it.
func dur(d time.Duration) string { return scaled(d).String() }

// waitDeadline bounds every wait for an event.
var waitDeadline = testwait.Deadline

// watched are each test's sources of change (its repos' watchers), for
// waitFor.
var watched sync.Map // *testing.T -> testwait.Any

// watch adds dir (a repo, or a directory a test's lanes write to) to what
// r's test waits on: waitFor checks its condition again whenever a file
// under it changes. Git's own files and the kept stages aren't watched.
func (r *repo) watch(dir string) {
	r.t.Helper()
	w, err := testwait.Watch(dir, func(d string) bool {
		rel, _ := filepath.Rel(dir, d)
		return rel == ".git" || rel == filepath.Join(".swim", "runs") || rel == filepath.Join(".swim", "scripts")
	})
	if err != nil {
		r.t.Fatalf("watch %s: %v", dir, err)
	}
	r.t.Cleanup(w.Close)
	prev, _ := watched.Load(r.t)
	src, _ := prev.(testwait.Any)
	watched.Store(r.t, append(src, w))
	r.t.Cleanup(func() { watched.Delete(r.t) })
}

// waitFor waits until cond holds, checking it again whenever a file in the
// test's repos changes (never on a timer); a hung swim fails after
// waitDeadline.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	src, ok := watched.Load(t)
	if !ok {
		t.Fatal("waitFor: this test has no repo to watch (newRepo), so nothing would wake the wait")
	}
	testwait.Until(t, "a condition", append(src.(testwait.Any), &output), cond)
}

// waitIn is waitFor for a directory outside the test's repos (a temp
// directory, an agent's socket directory): cond is checked again whenever
// a file under dir changes. false if it didn't hold by waitDeadline.
func waitIn(dir string, cond func() bool) bool {
	w, err := testwait.Watch(dir, nil)
	if err != nil {
		return cond()
	}
	defer w.Close()
	return testwait.UntilOK(w, cond, waitDeadline)
}

// output changes whenever a command the tests started prints (a
// syncBuffer), or an in-process stand-in (a test broker) records
// something: for waits on what isn't a file.
var output testwait.Signal

// hold is a step command that runs until r.release(name): it blocks
// reading a FIFO, $SWIM_ROOT/name, that release serves.
func hold(name string) string {
	return `bash -c 'f="$SWIM_ROOT/` + name + `"; [ -p "$f" ] || mkfifo "$f" 2>/dev/null; cat "$f" >/dev/null'`
}

// release ends hold(name)'s steps, now and later: it serves a line to
// every reader of the FIFO until the test ends.
func (r *repo) release(name string) {
	r.t.Helper()
	p := filepath.Join(r.root, name)
	if err := syscall.Mkfifo(p, 0o644); err != nil && !os.IsExist(err) {
		r.t.Fatal(err)
	}
	if _, loaded := released.LoadOrStore(p, true); loaded {
		return
	}
	stop := make(chan struct{})
	served := make(chan struct{})
	go func() {
		defer close(served)
		for {
			f, err := os.OpenFile(p, os.O_WRONLY, 0) // blocks until a hold opens it
			select {
			case <-stop:
				if err == nil {
					f.Close()
				}
				return
			default:
			}
			if err != nil {
				return
			}
			f.Write([]byte("go\n"))
			f.Close()
		}
	}()
	r.t.Cleanup(func() {
		close(stop)
		// Unblock the server's open, if it's waiting for a reader.
		if f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
			<-served
			f.Close()
		} else {
			<-served
		}
		released.Delete(p)
	})
}

// released are the FIFOs r.release serves.
var released sync.Map

// blocked is a step command that runs until it's killed, named by marker
// so noStray can find it (pgrep -f blocked-MARKER). Its trap makes a
// signal end it even when it lands while mkfifo runs: without one, bash
// carries on when a child it waited for exits normally despite the
// signal, and cat would then block for good.
func blocked(marker string) string {
	return `bash -c 'trap "exit 130" INT TERM; ` + fifoCat(marker) + `'`
}

// fifoCat is a bash fragment that runs until it's killed: cat blocks
// opening a FIFO nobody writes, $SWIM_ROOT/.blocked-MARKER. Unquoted, so it
// fits inside a double-quoted trap too (test repos' paths have no spaces).
func fifoCat(marker string) string {
	f := "$SWIM_ROOT/.blocked-" + marker
	return "mkfifo " + f + " 2>/dev/null; cat " + f
}

// startRun runs swim run ARGS in the background and returns a wait func
// for its output and exit code.
func startRun(t *testing.T, r *repo, args ...string) func() (string, int) {
	t.Helper()
	c := r.cmd(bin, args...)
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	code := 0
	go func() {
		if err := c.Wait(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			}
		}
		close(done)
	}()
	t.Cleanup(func() { c.Process.Kill() })
	return func() (string, int) {
		select {
		case <-done:
		case <-time.After(waitDeadline):
			t.Fatalf("swim %v didn't end:\n%s", args, out.String())
		}
		return out.String(), code
	}
}

// laneOf is lane n's status entry, empty before it has one.
func laneOf(r *repo, n int) status.Lane {
	if f, err := status.Load(r.root); err == nil && f != nil {
		if l := f.Get(n); l != nil {
			return *l
		}
	}
	return status.Lane{}
}

var jobLineRE = regexp.MustCompile(`(?m)^job: (\S+)$`)

func TestJobIDsAndPinning(t *testing.T) {
	r := newRepo(t, "")
	out := r.newLane("1", "pinned round")
	m := jobLineRE.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("new printed no job id:\n%s", out)
	}
	job := m[1]
	script, _ := os.ReadFile(filepath.Join(r.root, "lane.1.sh"))
	contains(t, "lane.1.sh", string(script), "# swim: syntax 4", "# Job:     "+job)
	if l := r.status().Get(1); l.PendingJob != job {
		t.Errorf("pending_job = %q", l.PendingJob)
	}

	// Steps see the job id, so they can tag resources as "ours".
	body := strings.Replace(string(script), `run "verify" true`, `run "tag" bash -c 'echo "tag=$SWIM_JOB"'`, 1)
	os.WriteFile(filepath.Join(r.root, "lane.1.sh"), []byte(body), 0o755)

	// Pin by an 8-character prefix.
	out = r.mustSwim("run", job[:8])
	contains(t, "pinned run", out, "tag="+job, job[:8])
	log := r.log(1)
	contains(t, "agent1.log", log, "  job="+job+"  pinned round", "| tag="+job)
	if l := r.status().Get(1); l.Job != job || l.State != status.Passed {
		t.Errorf("status after run: %+v", l)
	}
	contains(t, "status by job", r.mustSwim("status", job), "swim 1", "job: "+job)

	// Rewriting the lane replaces the job; the old pin must refuse to run.
	r.newLane("1", "different round", "--force", "--job", "orders-cutover-2")
	if out, code := r.swim("run", job); code == 0 || !strings.Contains(out, "no lane holds job") || !strings.Contains(out, "orders-cutover-2") {
		t.Errorf("stale pin: %d %s", code, out)
	}
	// Custom ids must be unique across lanes.
	if out, code := r.swim("new", "2", "dup", "--job", "orders-cutover-2"); code == 0 || !strings.Contains(out, "already in lane.1.sh") {
		t.Errorf("duplicate job id: %d %s", code, out)
	}
	if out, code := r.swim("new", "2", "bad", "--job", "12345678"); code == 0 || !strings.Contains(out, "not all digits") {
		t.Errorf("numeric job id: %d %s", code, out)
	}

	// Archive by the job that last ran, defaulting the name to the job id.
	out = r.mustSwim("archive", job[:8])
	if _, err := os.Stat(filepath.Join(r.root, ".swim", "logs", "agent1.prev-"+job+".log")); err != nil {
		t.Fatalf("archive by job: %v\n%s", err, out)
	}
}

func TestScriptWithoutJobLineGetsOne(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "no job line", `run "x" true`)
	r.mustSwim("run", "1")
	l := r.status().Get(1)
	if len(l.Job) != 36 || !strings.Contains(r.log(1), "job="+l.Job) {
		t.Fatalf("generated job: %+v\n%s", l, r.log(1))
	}
}

func TestProjectLog(t *testing.T) {
	r := newRepo(t, "{2: [1]}")
	r.newLane("1", "first goal", "--job", "job-one-0001")
	body, _ := os.ReadFile(filepath.Join(r.root, "lane.1.sh"))
	os.WriteFile(filepath.Join(r.root, "lane.1.sh"), []byte(strings.Replace(string(body), `gate "target is ours (tagged)" true`, `run "precheck" false`, 1)), 0o755)
	r.newLane("2", "second goal", "--job", "job-two-0002")
	r.swim("run")
	r.script(3, "direct run", `run "ok" true`)
	if out, err := r.cmd("bash", "lane.3.sh").CombinedOutput(); err != nil {
		t.Fatalf("direct run: %v %s", err, out)
	}
	r.mustSwim("archive", "job-one-0001")
	r.mustSwim("stub", "1", "precheck needs a fix")
	r.mustSwim("note", "--lane", "2", "rerun after lane 1 is fixed")

	data, err := os.ReadFile(filepath.Join(r.root, ".swim.log"))
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(l, "#") {
			continue
		}
		var f []string
		for _, w := range strings.Fields(l) {
			if !strings.HasPrefix(w, "run=r-") { // run ids vary; checked in TestRunID
				f = append(f, w)
			}
		}
		events = append(events, strings.Join(f[2:], " "))
	}
	want := []string{
		"init status",
		"new swim 1 job=job-one-0001 first goal",
		"new swim 2 job=job-two-0002 second goal",
		"lock created .swim.lock: syntax " + version.Current.String(),
		"run swim 1,2",
		"start swim 1 job=job-one-0001 first goal",
		"fail swim 1 job=job-one-0001 pass=",
		"skip swim 2 job=job-two-0002 swim 1 failed second goal",
		"run-done swim 1,2 passed=0 failed=1 skipped=1 interrupted=0",
		"start swim 3 job=",
		"pass swim 3 job=",
		"archive swim 1 job=job-one-0001 agent1.prev-job-one-0001.log",
		"stub swim 1 job=job-one-0001 precheck needs a fix",
		"note swim 2 job=job-two-0002 rerun after lane 1 is fixed",
	}
	if len(events) != len(want) {
		t.Fatalf("got %d events, want %d:\n%s", len(events), len(want), data)
	}
	for i, w := range want {
		if !strings.HasPrefix(events[i], w) {
			t.Errorf("event %d:\n got  %q\n want %q...", i, events[i], w)
		}
	}
	contains(t, ".swim.log", string(data), "| FAIL  precheck (exit 1)")
	gi, _ := os.ReadFile(filepath.Join(r.root, ".gitignore"))
	if out, _ := r.cmd("git", "check-ignore", ".swim.log").Output(); len(out) == 0 {
		t.Errorf(".swim.log should be git-ignored (.gitignore:\n%s)", gi)
	}
}

func TestLogCommand(t *testing.T) {
	r := newRepo(t, "")
	r.newLane("1", "first round", "--job", "first-job-01")
	r.mustSwim("run", "1")
	r.mustSwim("archive", "1", "first")
	r.newLane("1", "second round", "--force", "--job", "second-job-02")
	r.mustSwim("run", "1")

	// Current log only.
	out := r.mustSwim("log", "1")
	contains(t, "log 1", out, "job=second-job-02  second round", "== END PASS")
	if strings.Contains(out, "first-job-01") || strings.Contains(out, "==>") {
		t.Errorf("log 1 should be just agent1.log:\n%s", out)
	}
	if out != r.log(1) {
		t.Errorf("piped output should be the file verbatim")
	}

	// --all: archive first, then the current log, each with a header.
	out = r.mustSwim("log", "1", "--all")
	a, b := strings.Index(out, "==> agent1.prev-first.log <=="), strings.Index(out, "==> agent1.log <==")
	if a < 0 || b < 0 || a > b || strings.Index(out, "first-job-01") > b || strings.Index(out, "second-job-02") < b {
		t.Errorf("log 1 --all order:\n%s", out)
	}

	// By job, found in the archive.
	out = r.mustSwim("log", "first-job")
	contains(t, "log job", out, "==> agent1.prev-first.log <==", "job=first-job-01  first round")
	if strings.Contains(out, "second round") {
		t.Errorf("log by job leaked another round:\n%s", out)
	}

	// No argument: the project log.
	contains(t, "log", r.mustSwim("log"), "# swim project log", "archive", "swim 1  job=first-job-01")

	if out, code := r.swim("log", "2"); code == 0 || !strings.Contains(out, "no current log") {
		t.Errorf("log 2: %d %s", code, out)
	}
	if out, code := r.swim("log", "nosuchjob1"); code == 0 || !strings.Contains(out, "no log holds a round of job or run nosuchjob1") {
		t.Errorf("log unknown job: %d %s", code, out)
	}
}

func TestAllSkipsPassedJobs(t *testing.T) {
	r := newRepo(t, "")
	r.newLane("1", "passes")
	r.script(2, "fails until fixed", `run "check" test -e "$SWIM_ROOT/fixed"`)
	if _, code := r.swim("all"); code != 1 {
		t.Fatalf("first run should fail (lane 2)")
	}
	// Second `swim all`: lane 1 passed already, only lane 2 is retried.
	os.WriteFile(filepath.Join(r.root, "fixed"), nil, 0o644)
	out := r.mustSwim("all")
	if strings.Contains(out, "[1] started") || !strings.Contains(out, "[2] started") {
		t.Fatalf("second all should retry only lane 2:\n%s", out)
	}
	if n := strings.Count(r.log(1), "== ROUND "); n != 1 {
		t.Errorf("lane 1 ran %d times", n)
	}
	// Everything passed: nothing to run.
	out = r.mustSwim("all")
	contains(t, "third all", out, "nothing to run: every pending job has already passed (swim 1,2)")
	// Explicit lanes and --rerun still run passed rounds.
	r.mustSwim("run", "1")
	r.mustSwim("all", "--rerun")
	if n := strings.Count(r.log(1), "== ROUND "); n != 3 {
		t.Errorf("lane 1 ran %d times, want 3", n)
	}
	// A new round in the lane is pending again.
	r.newLane("1", "next round", "--force")
	if out := r.mustSwim("all"); !strings.Contains(out, "[1] started: next round") || strings.Contains(out, "[2] started") {
		t.Errorf("new round not picked up:\n%s", out)
	}
}

// A repo's lanes are the ones in use: a script, a current log or a running
// lane. There's no count to raise, and archiving drops a lane.
func TestLanesInUse(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "one", `run "ok" true`)
	r.script(6, "six", `run "ok" true`)
	out := r.mustSwim("all")
	contains(t, "all", out, "[1] started: one", "[6] started: six")
	st := r.mustSwim("status")
	contains(t, "status", st, "swim 1", "swim 6")
	if strings.Contains(st, "swim 3") {
		t.Errorf("status shows a lane not in use:\n%s", st)
	}
	// Stubbed, lane 6 is still in use while it has a log; archived, it isn't.
	r.mustSwim("stub", "6", "done")
	contains(t, "after stub", r.mustSwim("status"), "swim 6")
	r.mustSwim("archive", "6")
	if st := r.mustSwim("status"); strings.Contains(st, "swim 6") {
		t.Errorf("status after archive 6 still shows it:\n%s", st)
	}
	// Any lane can start: swim new 9 just works.
	r.newLane("9", "nine")
	contains(t, "after new 9", r.mustSwim("status"), "swim 9")
	// A lanes: setting is ignored, and doctor says so.
	os.WriteFile(filepath.Join(r.root, "swim.yml"), []byte("lanes: 4\n"), 0o644)
	contains(t, "doctor", must(r.swim("doctor")), "is no longer used", "[lanes-setting]")
}

func TestVersionAndLock(t *testing.T) {
	r := newRepo(t, "")
	if out := r.mustSwim("--version"); !regexp.MustCompile(`^swim 0\.(\d{8}|dev)(\.\d+)?`).MatchString(out) || strings.Contains(out, "warning") {
		t.Errorf("--version = %q", out)
	}
	lockPath := filepath.Join(r.root, ".swim.lock")
	r.script(1, "one", `run "ok" true`)
	r.mustSwim("status")
	if _, err := os.Stat(lockPath); err == nil {
		t.Fatal("status wrote a lock")
	}
	out := r.mustSwim("run", "1")
	contains(t, "first run", out, "wrote .swim.lock (syntax "+version.Current.String()+")")
	data, _ := os.ReadFile(lockPath)
	contains(t, ".swim.lock", string(data), fmt.Sprintf("script: %d", version.Current.Script), fmt.Sprintf("state: %d", version.Current.State), "version: 0.")
	if out := r.mustSwim("run", "1"); strings.Contains(out, "wrote .swim.lock") {
		t.Error("lock rewritten on second run")
	}
	contains(t, "lock", r.mustSwim("lock"), "lock:  syntax "+version.Current.String(), "ok:")
	contains(t, ".swim.log", r.mustSwim("log"), "lock         created .swim.lock: syntax "+version.Current.String())

	// An old-form lock (breaking: 7) is state 1, older than this swim's
	// (execution manifests): runs are refused until swim lock --upgrade
	// rewrites it in the new form.
	os.WriteFile(lockPath, []byte("breaking: 7\nversion: 7.20261013\n"), 0o644)
	if out, code := r.swim("run", "1"); code == 0 || !strings.Contains(out, "set up for an older swim") {
		t.Errorf("run with a breaking 7 lock: %d %s", code, out)
	}
	contains(t, "upgrade old form", r.mustSwim("lock", "--upgrade"), "breaking 7")
	data, _ = os.ReadFile(lockPath)
	contains(t, "rewritten lock", string(data), fmt.Sprintf("script: %d", version.Current.Script), fmt.Sprintf("state: %d", version.Current.State))
	r.mustSwim("run", "1")

	// A lock with a newer syntax: refuse to run, with instructions.
	os.WriteFile(lockPath, []byte(fmt.Sprintf("syntax: {script: %d, log: %d, state: %d}\nversion: 0.20270101.1\n", version.Current.Script, version.Current.Log, version.Current.State+1)), 0o644)
	for _, args := range [][]string{{"run", "1"}, {"all"}, {"1"}, {"new", "2", "x"}, {"lock", "--upgrade"}} {
		out, code := r.swim(args...)
		if code == 0 || !strings.Contains(out, "this repo needs a newer swim") || !strings.Contains(out, "go install github.com/gates-brightly/swimlane/cmd/swim@latest") {
			t.Errorf("swim %v with a newer lock: %d %s", args, code, out)
		}
	}
	if out, err := r.cmd("bash", "lane.1.sh").CombinedOutput(); err == nil || !strings.Contains(string(out), "needs a newer swim") {
		t.Errorf("direct run with a newer lock: %v %s", err, out)
	}
	// Read-only commands still work, with a warning.
	contains(t, "status", r.mustSwim("status"), "warning: this repo needs a newer swim", "swim 1")
	// The direct run's refusal is a round of its own that says so; no
	// other round ran.
	log := r.log(1)
	if refused := strings.Count(log, "\n  STOP  not started: this repo needs a newer swim"); refused != 1 || strings.Count(log, "== ROUND ")-refused != 3 {
		t.Errorf("a lane ran despite the lock mismatch, or its refusal isn't recorded once:\n%s", log)
	}
}

const v1Lane = `#!/usr/bin/env bash
# Round: Old template round
# Job:   old-job-0001
# Lane:  swim 1    Written: 2026-10-01
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 1

# 1. Snapshot (read-only)
snapshot "state" echo state

# 2. Checks
run "precheck" true

# 4. Verify
run "verify" true

summary
`

const v1Log = `=== ROUND START 2026-10-01T00:00:00Z swim 1 job=old-job-0001
Round: Old template round
=== STEP 2026-10-01T00:00:01Z precheck
$ true
--- output
--- exit 0 (0.0s)
PASS  precheck
=== SUMMARY 2026-10-01T00:00:02Z swim 1 pass=1 fail=0 skip=0 drift=0 exit=0 duration=1.0s
=== END
`

// A syntax 1 script or log (from before swim versioned its files) is
// refused, not rewritten.
func TestSyntax1Refused(t *testing.T) {
	r := newRepo(t, "")
	os.WriteFile(filepath.Join(r.root, "lane.1.sh"), []byte(v1Lane), 0o755)
	out, code := r.swim("status")
	if code == 0 || !strings.Contains(out, "lane.1.sh is swim syntax 1") {
		t.Errorf("a syntax 1 script: exit %d\n%s", code, out)
	}
	if script, _ := os.ReadFile(filepath.Join(r.root, "lane.1.sh")); string(script) != v1Lane {
		t.Error("the script was rewritten")
	}
	os.Remove(filepath.Join(r.root, "lane.1.sh"))
	os.MkdirAll(filepath.Join(r.root, ".swim", "logs"), 0o755)
	os.WriteFile(filepath.Join(r.root, ".swim", "logs", "agent1.log"), []byte(v1Log), 0o644)
	if out, code := r.swim("status"); code == 0 || !strings.Contains(out, "agent1.log is swim syntax 1") {
		t.Errorf("a syntax 1 log: exit %d\n%s", code, out)
	}
}

func TestSyntaxTooNew(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "fine", `run "ok" true`)
	os.WriteFile(filepath.Join(r.root, "lane.2.sh"), []byte("#!/usr/bin/env bash\n# swim: syntax 9\n# Round: from the future\n"), 0o755)
	for _, args := range [][]string{{"status"}, {"run", "1"}, {"all"}} {
		out, code := r.swim(args...)
		if code == 0 || !strings.Contains(out, "lane.2.sh uses swim syntax 9") || !strings.Contains(out, "go install github.com/gates-brightly/swimlane/cmd/swim@latest") {
			t.Errorf("swim %v with a syntax 9 script: %d %s", args, code, out)
		}
	}
	os.Remove(filepath.Join(r.root, "lane.2.sh"))
	os.MkdirAll(filepath.Join(r.root, ".swim", "logs"), 0o755)
	os.WriteFile(filepath.Join(r.root, ".swim", "logs", "agent3.log"), []byte("# swim lane log | syntax 7 | swim 3\n"), 0o644)
	if out, code := r.swim("status"); code == 0 || !strings.Contains(out, "agent3.log uses swim syntax 7") {
		t.Errorf("syntax 7 log: %d %s", code, out)
	}
}

func TestStagesWarnings(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "out of order", `stage change
run "change" true
stage check
run "check" true
stage verify
run "verify" true`)
	r.mustSwim("run", "1")
	contains(t, "agent1.log", r.log(1),
		"  WARN  change stage without a snapshot or check stage before it",
		"  WARN  stage check after change (expected order: snapshot, check, change, verify)",
		"stages: snapshot none | check PASS | change PASS | verify PASS")

	r.script(2, "bad stage", `stage deploy
run "never" true`)
	// swim lint refuses it before it starts; run directly, it stops there.
	out, code := r.swim("run", "2")
	if code == 0 || !strings.Contains(out, `unknown stage "deploy"`) {
		t.Fatalf("unknown stage: %d %s", code, out)
	}
	r.cmd("bash", "lane.2.sh").Run()
	if !strings.Contains(r.log(2), "  STOP  bad stage: deploy") || strings.Contains(r.log(2), "never") {
		t.Fatalf("unknown stage, run directly:\n%s", r.log(2))
	}
}

func TestTimeoutStopsRound(t *testing.T) {
	r := newRepo(t, "")
	p := filepath.Join(r.root, "lane.1.sh")
	r.script(1, "slow", `stage check
run "slow" `+blocked("slow")+`
run "after" true`)
	data, _ := os.ReadFile(p)
	limit := dur(time.Second)
	os.WriteFile(p, []byte(strings.Replace(string(data), "# Round: slow\n", "# Round: slow\n# Timeout: "+limit+"\n", 1)), 0o755)
	start := time.Now()
	out, code := r.swim("run", "1")
	if code == 0 || time.Since(start) > scaled(15*time.Second) {
		t.Fatalf("exit %d after %s:\n%s", code, time.Since(start), out)
	}
	log := r.log(1)
	contains(t, "agent1.log", log, "timeout: "+limit, "  FAIL  slow (timeout: Timeout "+limit+" reached)", "  STOP  timeout: the round's Timeout ("+limit+") ran out", "== END FAIL")
	if strings.Contains(log, "PASS  after") {
		t.Error("a step ran after the timeout")
	}
}

var runRE = regexp.MustCompile(`run=(r-\d{8}T\d{6}Z-[0-9a-f]{4})`)

func TestRunID(t *testing.T) {
	r := newRepo(t, "{2: [1]}")
	r.script(1, "one", `run "id" bash -c 'echo "id=$SWIM_RUN lanes=$SWIM_RUN_LANES"'`)
	r.script(2, "two", `run "id" bash -c 'echo "id=$SWIM_RUN lanes=$SWIM_RUN_LANES"'`)
	r.script(3, "three", `run "x" false`)
	r.script(4, "four", `run "x" true`)
	p := filepath.Join(r.root, "lane.4.sh")
	data, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(data), "# Round: four\n", "# Round: four\n# After: 3\n", 1)), 0o755)

	r.swim("all", "--plain")
	idRE := regexp.MustCompile(`\| id=(\S+) lanes=(.*)`)
	m1, m2 := idRE.FindStringSubmatch(r.log(1)), idRE.FindStringSubmatch(r.log(2))
	if m1 == nil || m2 == nil || m1[1] != m2[1] || !regexp.MustCompile(`^r-\d{8}T\d{6}Z-[0-9a-f]{4}$`).MatchString(m1[1]) {
		t.Fatalf("lanes in one run should share SWIM_RUN: %v %v", m1, m2)
	}
	run := m1[1]
	if m1[2] != "1 2 3 4" {
		t.Errorf("SWIM_RUN_LANES = %q (skipped lanes are part of the selection)", m1[2])
	}
	contains(t, "agent1.log", r.log(1), "   run: "+run+" | script: lane.1.sh", "== END PASS", "  run="+run)
	st := r.status()
	if st.LastRun != run || st.Get(1).Run != run || st.Get(4).Run != run || st.Get(4).State != status.Skipped {
		t.Errorf("status: last_run=%s lane1=%s lane4=%+v", st.LastRun, st.Get(1).Run, st.Get(4))
	}
	hist, _ := os.ReadFile(filepath.Join(r.root, ".swim.log"))
	for _, ev := range []string{"run ", "start ", "pass ", "fail ", "skip ", "run-done "} {
		found := false
		for _, line := range strings.Split(string(hist), "\n") {
			if f := strings.Fields(line); len(f) > 2 && f[2]+" " == ev && strings.Contains(line, "run="+run) {
				found = true
			}
		}
		if !found {
			t.Errorf(".swim.log has no %q event with run=%s:\n%s", ev, run, hist)
		}
	}

	// swim log <run>: every lane's round from that run.
	out := r.mustSwim("log", run)
	contains(t, "log <run>", out, "==> agent1.log <==", "==> agent2.log <==", "==> agent3.log <==", "id="+run)
	// swim status --run: lanes as of that run, including the skipped one.
	contains(t, "status --run", r.mustSwim("status", "--run", run), "run "+run, "swim 1", "PASS", "FAIL exit 1", "SKIP", "swim 3 failed")

	// The next run gets a new id; --run-id is honoured.
	r.mustSwim("run", "1", "--run-id", "pipeline-12345")
	if m := idRE.FindAllStringSubmatch(r.log(1), -1); len(m) != 2 || m[1][1] != "pipeline-12345" {
		t.Fatalf("--run-id: %v", m)
	}
	if out, code := r.swim("run", "1", "--run-id", "x y"); code == 0 || !strings.Contains(out, "--run-id") {
		t.Errorf("bad --run-id: %d %s", code, out)
	}
	// A lane run directly with bash is a run of its own.
	if out, err := r.cmd("bash", "lane.1.sh").CombinedOutput(); err != nil {
		t.Fatalf("direct run: %v %s", err, out)
	}
	ms := idRE.FindAllStringSubmatch(r.log(1), -1)
	if len(ms) != 3 || ms[2][1] == run || ms[2][1] == "pipeline-12345" || ms[2][2] != "1" {
		t.Fatalf("direct run id: %v", ms)
	}
}

func TestParallelLimit(t *testing.T) {
	long(t)
	r := newSerialRepo(t, "")
	for n := 1; n <= 10; n++ {
		r.script(n, fmt.Sprintf("worker %d", n), `run "work" `+inSlot(3, "go"))
	}
	run := start(t, r.cmd(bin, "all", "--parallel", "3", "--plain"), nil)
	// Three are inside their step at once, holding until released; the
	// rest wait for a slot.
	waitFor(t, func() bool { return inside(r) == 3 && states(r, 10)[status.Queued] == 7 })
	r.release("go")
	if code := run.wait(t); code != 0 {
		t.Fatalf("run: exit %d\n%s", code, run.out.String())
	}
	noOverlaps(t, r)
	s := run.out.String()
	contains(t, "output", s, "queued (", "swim summary")
	for n := 1; n <= 10; n++ {
		if st := r.status().Get(n).State; st != status.Passed {
			t.Errorf("swim %d = %s", n, st)
		}
	}
	contains(t, ".swim.log", r.mustSwim("log"), "max_parallel=3", "queued       swim ")

	// Config sets the default; --parallel 0 lifts it: all ten at once.
	cfg := filepath.Join(strings.TrimPrefix(envOf(r, "XDG_CONFIG_HOME"), ""), "swim", "config.yml")
	data, _ := os.ReadFile(cfg)
	os.WriteFile(cfg, []byte(strings.Replace(string(data), "defaults:\n", "defaults:\n  max_parallel: 2\n", 1)), 0o644)
	contains(t, "config", r.mustSwim("config"), "max_parallel: 2")
	for n := 1; n <= 10; n++ {
		r.script(n, fmt.Sprintf("worker %d", n), `run "work" `+inSlot(10, "go-all"))
	}
	all := start(t, r.cmd(bin, "all", "--rerun", "--parallel", "0", "--plain"), nil)
	waitFor(t, func() bool { return inside(r) == 10 })
	r.release("go-all")
	if code := all.wait(t); code != 0 {
		t.Fatalf("--parallel 0: exit %d\n%s", code, all.out.String())
	}
	if out, code := r.swim("all", "--parallel", "-1"); code == 0 || !strings.Contains(out, "--parallel") {
		t.Errorf("--parallel -1: %d %s", code, out)
	}
}

// withoutKey is m without key.
func withoutKey(m map[string]any, key string) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		if k != key {
			out[k] = v
		}
	}
	return out
}

// envOf is key's value in r.env: the last one, as exec uses it.
func envOf(r *repo, key string) string {
	val := ""
	for _, kv := range r.env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			val = v
		}
	}
	return val
}

// withLocks adds a "# Locks:" header line to lane n.
func withLocks(t *testing.T, r *repo, n int, locks string) {
	t.Helper()
	p := filepath.Join(r.root, fmt.Sprintf("lane.%d.sh", n))
	data, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(data), "\n# Round:", "\n# Locks: "+locks+"\n# Round:", 1)), 0o755)
}

// untilReleased is a bash fragment that blocks until r.release(name): it
// reads the FIFO $SWIM_ROOT/name that release serves.
func untilReleased(name string) string {
	return `rf="$SWIM_ROOT/` + name + `"; [ -p "$rf" ] || mkfifo "$rf" 2>/dev/null; cat "$rf" >/dev/null`
}

// exclusive is a step command for a lane that must be alone holding
// locks: it enters each (mkdir, which fails if another lane is inside, and
// says so in $SWIM_ROOT/overlaps), says it's inside (for inside), holds
// until r.release(rel), leaves, and then runs tail. Overlaps are caught
// exactly, not sampled.
func exclusive(locks []string, rel, tail string) string {
	var in, out []string
	for _, l := range locks {
		d := `"$SWIM_ROOT/in-` + strings.ReplaceAll(l, "/", "_") + `"`
		in = append(in, `mkdir `+d+` 2>/dev/null || echo "swim $SWIM_LANE: `+l+`" >> "$SWIM_ROOT/overlaps"`)
		out = append(out, `rmdir `+d+` 2>/dev/null`)
	}
	return `bash -c '` + strings.Join(in, "; ") + `; touch "$SWIM_ROOT/inside.$SWIM_LANE"; ` + untilReleased(rel) +
		`; rm -f "$SWIM_ROOT/inside.$SWIM_LANE"; ` + strings.Join(out, "; ") + "; " + or(tail, "true") + `'`
}

// inSlot is a step command for a lane of a run limited to k at once: it
// takes a free slot of k (mkdir), noting in $SWIM_ROOT/overlaps when none
// is free, says it's inside ($SWIM_ROOT/inside.N, for inside), holds
// until r.release(rel), and gives the slot back.
func inSlot(k int, rel string) string {
	return fmt.Sprintf(`bash -c 'i=0; while [ $i -lt %d ] && ! mkdir "$SWIM_ROOT/slot$i" 2>/dev/null; do i=$((i+1)); done; `+
		`[ $i -lt %d ] || echo "swim $SWIM_LANE: no slot free" >> "$SWIM_ROOT/overlaps"; touch "$SWIM_ROOT/inside.$SWIM_LANE"; %s; `+
		`rm -f "$SWIM_ROOT/inside.$SWIM_LANE"; rmdir "$SWIM_ROOT/slot$i" 2>/dev/null; true'`, k, k, untilReleased(rel))
}

// inside is how many lanes are in an inSlot or exclusive step now.
func inside(r *repo) int {
	m, _ := filepath.Glob(filepath.Join(r.root, "inside.*"))
	return len(m)
}

// noOverlaps fails if a lane entered a lock or slot another held.
func noOverlaps(t *testing.T, r *repo) {
	t.Helper()
	if data, err := os.ReadFile(filepath.Join(r.root, "overlaps")); err == nil {
		t.Errorf("lanes overlapped:\n%s", data)
	}
}

// states counts lanes 1..n by state.
func states(r *repo, n int) map[string]int {
	c := map[string]int{}
	st := r.status()
	if st == nil {
		return c // no status.yml yet
	}
	for i := 1; i <= n; i++ {
		if l := st.Get(i); l != nil {
			c[l.State]++
		}
	}
	return c
}

func TestResourceLocksWithinARun(t *testing.T) {
	long(t)
	r := newRepo(t, "")
	lock := []string{"orders-table"}
	r.script(1, "migrate table", `run "work" `+exclusive(lock, "go", "exit 1"))
	r.script(2, "back up table", `run "work" `+exclusive(lock, "go", ""))
	withLocks(t, r, 1, "orders-table")
	withLocks(t, r, 2, "orders-table")

	run := start(t, r.cmd(bin, "all", "--plain"), nil)
	// One holds the lock; the other waits for it.
	waitFor(t, func() bool { return inside(r) == 1 && states(r, 2)[status.Locked] == 1 })
	r.release("go")
	run.wait(t)
	out := run.out.String()
	noOverlaps(t, r)
	// A failure doesn't spread through a lock: lane 2 still ran and passed.
	if st := r.status(); st.Get(1).State != status.Failed || st.Get(2).State != status.Passed {
		t.Fatalf("states: 1=%s 2=%s\n%s", st.Get(1).State, st.Get(2).State, out)
	}
	contains(t, "output", out, "waiting for lock orders-table: swim ")
	contains(t, ".swim.log", r.mustSwim("log"), "locked       swim ")

	// Locks {a}, {a,b}, {b}: no deadlock; sharers never overlap.
	r2 := newRepo(t, "")
	for n, locks := range map[int][]string{1: {"a"}, 2: {"a", "b"}, 3: {"b"}} {
		r2.script(n, fmt.Sprintf("lane %d", n), `run "work" `+exclusive(locks, "go", ""))
		withLocks(t, r2, n, strings.Join(locks, ", "))
	}
	run2 := start(t, r2.cmd(bin, "all", "--plain"), nil)
	// Some lanes hold their locks, the rest wait: never all three locked
	// out (a deadlock), never 2 beside 1 or 3.
	waitFor(t, func() bool { in := inside(r2); return in >= 1 && in+states(r2, 3)[status.Locked] == 3 })
	r2.release("go")
	run2.wait(t) // a deadlock never ends: it fails at the deadline
	noOverlaps(t, r2)
	for n := 1; n <= 3; n++ {
		if st := r2.status().Get(n).State; st != status.Passed {
			t.Errorf("swim %d = %s", n, st)
		}
	}
	if out, _ := r2.swim("status"); strings.Contains(out, "held by") {
		t.Errorf("locks still held after the run:\n%s", out)
	}
}

func TestResourceLocksAcrossRuns(t *testing.T) {
	long(t)
	r := newRepo(t, "")
	lock := []string{"tf/orders"}
	r.script(1, "one", `run "work" `+exclusive(lock, "go", ""))
	r.script(2, "two", `run "work" `+exclusive(lock, "go", ""))
	withLocks(t, r, 1, "tf/orders")
	withLocks(t, r, 2, "tf/orders")

	// Two concurrent swim run processes respect the same lock.
	a := r.cmd(bin, "run", "1", "--plain")
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return laneOf(r, 1).State == status.Running })
	contains(t, "status while held", r.mustSwim("status"), "lock tf/orders held by swim 1", "locks: tf/orders")
	b := start(t, r.cmd(bin, "run", "2", "--plain"), nil)
	b.saw(t, "waiting for lock tf/orders: another run, swim 1")
	r.release("go")
	code := b.wait(t)
	a.Wait()
	if code != 0 {
		t.Fatalf("run 2: exit %d\n%s", code, b.out.String())
	}
	noOverlaps(t, r)

	// A killed run doesn't leave the lock behind.
	r.script(1, "one", `run "work" `+blocked("locks"))
	withLocks(t, r, 1, "tf/orders")
	k := r.cmd(bin, "run", "1", "--plain")
	k.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := k.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return laneOf(r, 1).CurrentStep == "work" })
	syscall.Kill(-k.Process.Pid, syscall.SIGKILL)
	k.Wait()
	// The launcher held the lock; it's gone, so the kernel let go of it.
	if st := r.mustSwim("status"); strings.Contains(st, "held by") {
		t.Fatalf("lock left behind by a killed run:\n%s", st)
	}
	if out, err := r.cmd(bin, "run", "2", "--plain").CombinedOutput(); err != nil {
		t.Fatalf("run 2 after the killed run: %v\n%s", err, out)
	}
}

func TestBadLockNameWarns(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "bad lock", `run "x" true`)
	withLocks(t, r, 1, "ok-name, bad*name")
	r.mustSwim("run", "1")
	contains(t, "agent1.log", r.log(1), `WARN  Locks: "bad*name" is not a lock name`)
}

func TestStepTimeoutAndRetry(t *testing.T) {
	long(t)
	r := newRepo(t, "")
	limit := dur(time.Second)
	r.script(1, "timeouts and retries", `run --timeout `+limit+` "slow" `+blocked("slow")+`
run --retry 2 --backoff 0.1s "flaky" bash -c 'test -f "$SWIM_ROOT/ok" || { touch "$SWIM_ROOT/ok"; exit 1; }'
run --retry 3 --backoff 0.1s --retry-on 7 "wrong code" bash -c 'exit 1'
gate --retry 1 --backoff 0.1s "gate passes on retry" bash -c 'test -f "$SWIM_ROOT/ok2" || { touch "$SWIM_ROOT/ok2"; exit 1; }'
run "after" true`)
	start := time.Now()
	r.swim("run", "1", "--plain")
	if took := time.Since(start); took > scaled(8*time.Second) {
		t.Fatalf("step limit didn't stop the step promptly: %s", took)
	}
	log := r.log(1)
	contains(t, "agent1.log", log,
		"  FAIL  slow (timeout: step limit "+limit+")",
		"the step's time limit ("+limit+") ran out",
		"  PASS  flaky (attempt 2/3)",
		"        -- attempt 1/3",
		"        -- exit 1 (",
		"; retry in ",
		"        -- attempt 2/3",
		"  FAIL  wrong code (exit 1, attempt 1/4)",
		"  PASS  gate passes on retry (attempt 2/2)",
		"  PASS  after")
	if strings.Contains(log, "-- attempt 2/4") {
		t.Error("--retry-on 7 retried exit 1")
	}
	if l := r.status().Get(1); l.Pass != 3 || l.Fail != 2 {
		t.Errorf("each step counts once: pass=%d fail=%d", l.Pass, l.Fail)
	}

	// Step-Timeout: in the header is the default; --timeout 0 turns it off.
	r.script(2, "default limit", `run "limited" `+blocked("limited")+`
run --timeout 0 "unlimited" sleep `+fmt.Sprint(scaled(1500*time.Millisecond).Seconds()))
	p := filepath.Join(r.root, "lane.2.sh")
	data, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(data), "# Round: default limit\n", "# Round: default limit\n# Step-Timeout: "+limit+"\n", 1)), 0o755)
	r.swim("run", "2", "--plain")
	contains(t, "agent2.log", r.log(2), "step-timeout: "+limit, "  FAIL  limited (timeout: step limit "+limit+")", "  PASS  unlimited")
}

func TestRetryRespectsRoundDeadline(t *testing.T) {
	r := newSerialRepo(t, "") // it times a round's Timeout: alone, not beside the parallel tests
	limit := dur(2 * time.Second)
	r.script(1, "deadline", `run --retry 5 --backoff `+dur(1500*time.Millisecond)+` "keeps failing" false
run "after" true`)
	p := filepath.Join(r.root, "lane.1.sh")
	data, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(data), "# Round: deadline\n", "# Round: deadline\n# Timeout: "+limit+"\n", 1)), 0o755)
	start := time.Now()
	r.swim("run", "1", "--plain")
	if took := time.Since(start); took > scaled(6*time.Second) {
		t.Fatalf("retries ran past the round's deadline: %s", took)
	}
	log := r.log(1)
	contains(t, "agent1.log", log, "no retry: the round's Timeout "+limit+" would pass first", "  FAIL  keeps failing (timeout: Timeout "+limit+" reached, attempt ", "  STOP  timeout: the round's Timeout ("+limit+") ran out")
	if strings.Contains(log, "PASS  after") {
		t.Error("the round continued after its timeout")
	}
}

func TestCtrlCDuringBackoff(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "backoff", `run --retry 3 --backoff 20s "fails" false
run "never" true`)
	c := r.cmd("bash", "lane.1.sh")
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		data, _ := os.ReadFile(filepath.Join(r.root, ".swim", "logs", "agent1.log.partial"))
		return strings.Contains(string(data), "retry in")
	})
	start := time.Now()
	syscall.Kill(-c.Process.Pid, syscall.SIGINT)
	c.Wait()
	if took := time.Since(start); took > scaled(5*time.Second) {
		t.Fatalf("Ctrl-C during backoff took %s", took)
	}
	log := r.log(1)
	contains(t, "agent1.log", log, "interrupted while waiting to retry", "== END INTERRUPTED")
	if strings.Contains(log, "never") {
		t.Error("the round continued after Ctrl-C")
	}
}

func TestBlockedCommands(t *testing.T) {
	r := newRepo(t, "")
	exec.Command("git", "-C", r.root, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "base").Run()
	head := func() string {
		out, _ := exec.Command("git", "-C", r.root, "rev-parse", "HEAD").Output()
		return strings.TrimSpace(string(out))
	}
	before := head()
	// swim blocks nothing by itself; a repo's blocked_commands are refused.
	cfg := filepath.Join(envOf(r, "XDG_CONFIG_HOME"), "swim", "config.yml")
	data, _ := os.ReadFile(cfg)
	os.WriteFile(cfg, []byte(strings.Replace(string(data), "defaults:\n", "defaults:\n  blocked_commands: [\"terraform destroy\", \"make publish\"]\n", 1)), 0o644)
	contains(t, "config", r.mustSwim("config"), `"terraform destroy"`, `"make publish"`)
	if strings.Contains(r.mustSwim("config"), "git push") {
		t.Error("git push is blocked by default")
	}

	// 1. Pre-flight: a literal blocked command fails the lane before it starts.
	r.script(1, "publish", `run "snapshot" echo before
run "publish" make publish`)
	r.script(2, "after publish", `run "x" true`)
	p := filepath.Join(r.root, "lane.2.sh")
	data, _ = os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(data), "# Round: after publish\n", "# Round: after publish\n# After: 1\n", 1)), 0o755)
	out, code := r.swim("all", "--plain")
	if code == 0 {
		t.Fatalf("blocked lane passed:\n%s", out)
	}
	contains(t, "summary", out, `blocked: lane.1.sh:7 matches "make publish"`, "[2] SKIP (swim 1 failed)")
	// A refused lane doesn't start: its log's one round is the refusal,
	// with no step.
	if log := r.log(1); strings.Count(log, "== ROUND ") != 1 || !strings.Contains(log, `STOP  not started: blocked: lane.1.sh:7 matches "make publish"`) || strings.Contains(log, "PASS  ") {
		t.Errorf("a refused lane must not start (its log has only the refusal):\n%s", log)
	}
	l := r.status().Get(1)
	if l.State != status.Failed || l.ExitCode == nil || *l.ExitCode != 87 || len(l.FailedSteps) != 1 || !strings.HasPrefix(l.FailedSteps[0], "BLOCKED  publish") {
		t.Errorf("status: %+v", l)
	}
	contains(t, ".swim.log", r.mustSwim("log"), `blocked: lane.1.sh:7 matches "make publish"`)

	// 2. A command built at runtime is refused at the step, and a plain run
	//    (not just gate) stops the round.
	r.script(3, "runtime", `CMD="terraform des""troy -auto-approve"
run "builds a destroy" bash -c "$CMD"
run "never" true`)
	r.swim("run", "3", "--plain")
	log := r.log(3)
	contains(t, "agent3.log", log, `  BLOCKED  builds a destroy (matched "terraform destroy"; blocked_commands)`, "  STOP  blocked command: builds a destroy", "== END FAIL", "exit=1")
	if strings.Contains(log, "PASS  never") {
		t.Errorf("the round continued after a blocked command:\n%s", log)
	}
	if head() != before {
		t.Error("the repo's history changed")
	}

	// 3. git is a lane's to run: a commit in a step and one outside any
	//    step both land, and the round carries on.
	r.script(4, "commits", `export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
run "commit" git -C "$SWIM_ROOT" commit -q --allow-empty -m one
git -C "$SWIM_ROOT" commit -q --allow-empty -m two
run "after" true`)
	out4, _ := r.swim("run", "4", "--plain")
	contains(t, "agent4.log", r.log(4), "  PASS  commit", "  PASS  after", "== END PASS")
	if head() == before {
		t.Errorf("the lane's commits didn't land:\n%s", out4)
	}
	if msgs, _ := exec.Command("git", "-C", r.root, "log", "--format=%s", "-2").Output(); strings.Fields(string(msgs))[0] != "two" {
		t.Errorf("the commit outside a step didn't land: %q", msgs)
	}
	if _, err := os.Stat(filepath.Join(r.root, ".swim", "bin", "git")); err == nil {
		t.Error("swim installed a git shim")
	}
}

func TestSecretMasking(t *testing.T) {
	r := newRepo(t, "")
	const tok = "ghp_testvalue123456"
	const listed = "dd-api-key-0042"
	cfg := filepath.Join(envOf(r, "XDG_CONFIG_HOME"), "swim", "config.yml")
	data, _ := os.ReadFile(cfg)
	conf := strings.Replace(string(data), "defaults:\n", "defaults:\n  secret_env: [DD_KEY]\n", 1)
	conf = regexp.MustCompile(`(?m)^  header_env: .*$`).ReplaceAllString(conf, "  header_env: [GITHUB_TOKEN, STAGE]")
	os.WriteFile(cfg, []byte(conf), 0o644)
	r.script(1, "leaky", `run "echo the token" bash -c 'echo "token=$GITHUB_TOKEN"; env | grep -E "GITHUB_TOKEN|DD_KEY"'
snapshot "state with secrets" bash -c 'echo "dd=$DD_KEY b64=$(printf %s "$GITHUB_TOKEN" | base64)"'
run "fail with $GITHUB_TOKEN in the label" false
guard SECRET_FLAG "reason mentions $DD_KEY"
summary`)
	c := r.cmd(bin, "run", "1", "--plain")
	c.Env = append(c.Env, "GITHUB_TOKEN="+tok, "DD_KEY="+listed)
	out, _ := c.CombinedOutput()
	n := r.cmd(bin, "note", "--lane", "1", "the token is "+tok)
	n.Env = append(n.Env, "GITHUB_TOKEN="+tok)
	if o, err := n.CombinedOutput(); err != nil {
		t.Fatalf("note: %v %s", err, o)
	}

	if strings.Contains(string(out), tok) || strings.Contains(string(out), listed) {
		t.Errorf("a secret reached the terminal stream:\n%s", out)
	}
	contains(t, "terminal", string(out), "token=***")
	// Nothing swim wrote, anywhere, holds the raw values (or the base64 form).
	b64, _ := exec.Command("bash", "-c", "printf %s "+tok+" | base64").Output()
	for _, root := range []string{filepath.Join(r.root, ".swim"), filepath.Join(r.root, ".swim.log")} {
		filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			body, _ := os.ReadFile(path)
			for _, raw := range []string{tok, listed, strings.TrimSpace(string(b64))} {
				if strings.Contains(string(body), raw) {
					t.Errorf("%s contains a secret (%q)", path, raw)
				}
			}
			return nil
		})
	}
	log := r.log(1)
	contains(t, "agent1.log", log, "masked: ", "GITHUB_TOKEN", "DD_KEY", "env: GITHUB_TOKEN=***(len 19)", "        | token=***", "  FAIL  fail with *** in the label", "reason mentions ***")
	contains(t, ".swim.log", r.mustSwim("log"), "the token is ***")
}

// withoutKeys drops YAML keys (and what's nested under them) from doc.
func withoutKeys(doc string, keys ...string) string {
	var out []string
	skip := -1 // the indent of the key being dropped
	for _, line := range strings.SplitAfter(doc, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		indent := len(line) - len(trimmed)
		if skip >= 0 && (indent > skip || strings.TrimSpace(line) == "" || strings.HasPrefix(trimmed, "- ") && indent == skip) {
			continue
		}
		skip = -1
		for _, k := range keys {
			if strings.HasPrefix(trimmed, k+":") {
				skip = indent
			}
		}
		if skip < 0 {
			out = append(out, line)
		}
	}
	return strings.Join(out, "")
}

func withHeader(t *testing.T, r *repo, n int, line string) {
	t.Helper()
	p := filepath.Join(r.root, fmt.Sprintf("lane.%d.sh", n))
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(p, []byte(strings.Replace(string(data), "\n", "\n"+line+"\n", 1)), 0o755)
}

func TestYAMLOutput(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "root", `run "hello" echo hi there`)
	r.script(2, "fails", `run "convert" false`)
	r.script(3, "after fails", `run "never" true`)
	r.script(4, "after root", `run "child" true`)
	withHeader(t, r, 2, "# After: 1")
	withHeader(t, r, 3, "# After: 2")
	withHeader(t, r, 4, "# After: 1")

	c := r.cmd(bin, "all", "--yaml")
	var out, errOut bytes.Buffer
	c.Stdout, c.Stderr = &out, &errOut
	err := c.Run()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
		t.Fatalf("exit: %v\n%s\n%s", err, out.String(), errOut.String())
	}
	type doc struct {
		Schema, Event, Run, Result string
		Lane                       int
		On                         []int
		Lanes                      []yaml.Node
	}
	dec := yaml.NewDecoder(&out)
	pos := map[string]int{} // "start 2" -> index
	var docs []doc
	var summary launcherSummary
	for i := 0; ; i++ {
		var n yaml.Node
		if err := dec.Decode(&n); err != nil {
			break
		}
		var d doc
		if err := n.Decode(&d); err != nil {
			t.Fatal(err)
		}
		if d.Schema != "swim.run/v1" {
			t.Fatalf("document %d has no schema: %+v", i, d)
		}
		if d.Event == "summary" {
			n.Decode(&summary)
		}
		docs = append(docs, d)
		pos[fmt.Sprintf("%s %d", d.Event, d.Lane)] = i
	}
	if len(docs) == 0 || docs[0].Event != "run" || docs[len(docs)-1].Event != "summary" {
		t.Fatalf("stream should open with run and close with summary: %+v", docs)
	}
	for _, n := range []int{1, 2, 4} {
		s, ok1 := pos[fmt.Sprintf("start %d", n)]
		f, ok2 := pos[fmt.Sprintf("finish %d", n)]
		if !ok1 || !ok2 || s > f {
			t.Errorf("swim %d: start %d finish %d", n, s, f)
		}
	}
	if pos["start 2"] < pos["finish 1"] || pos["start 4"] < pos["finish 1"] {
		t.Errorf("children started before their parent finished: %v", pos)
	}
	if _, ok := pos["start 3"]; ok {
		t.Errorf("swim 3 should never start")
	}
	if _, ok := pos["step 2"]; !ok {
		t.Errorf("no step events for swim 2")
	}
	if strings.Contains(errOut.String(), "swim summary") || strings.Contains(out.String(), "hi there") {
		t.Errorf("no table or lane output without --yaml-output:\n%s\n%s", out.String(), errOut.String())
	}

	// The summary matches status.yml.
	st := r.status()
	if summary.Run != st.LastRun || summary.Result != "failed" || len(summary.Lanes) != 4 {
		t.Fatalf("summary: %+v", summary)
	}
	for _, l := range summary.Lanes {
		if got := st.Get(l.Lane).State; got != l.Result {
			t.Errorf("swim %d: summary %s, status.yml %s", l.Lane, l.Result, got)
		}
	}

	// --yaml-output adds lane output lines.
	r.script(1, "root again", `run "hello" echo hi there`)
	if out := r.mustSwim("run", "1", "--yaml-output"); !strings.Contains(out, "event: output") || !strings.Contains(out, "line: hi there") {
		t.Errorf("--yaml-output:\n%s", out)
	}

	// log and status --yaml carry their schemas.
	var lg logparse.Doc
	if err := yaml.Unmarshal([]byte(r.mustSwim("log", "2", "--yaml")), &lg); err != nil {
		t.Fatal(err)
	}
	if lg.Schema != "swim.log/v1" || lg.Result != "failed" || len(lg.Results) != 1 || lg.Results[0].Command != "false" || *lg.Results[0].Exit != 1 {
		t.Errorf("log --yaml: %+v", lg)
	}
	contains(t, "status --yaml", r.mustSwim("status", "--yaml"), "schema: swim.status/v1", "last_run: ")
	if data, _ := os.ReadFile(filepath.Join(r.root, ".swim", "status.yml")); strings.Contains(string(data), "schema:") {
		t.Errorf("status.yml itself stays unchanged")
	}
}

type launcherSummary struct {
	Run, Result string
	Lanes       []struct {
		Lane   int
		Result string
	}
}

func TestArchiveSeveralLanes(t *testing.T) {
	r := newRepo(t, "")
	for n := 1; n <= 3; n++ {
		r.script(n, fmt.Sprintf("round %d", n), `run "a" true`)
		withHeader(t, r, n, fmt.Sprintf("# Job: aaaa000%d-1111-4222-8333-444455556666", n))
	}
	r.mustSwim("all")
	out := r.mustSwim("archive", "1", "2", "3")
	for n := 1; n <= 3; n++ {
		contains(t, "archive", out, fmt.Sprintf("agent%d.log -> .swim/logs/agent%d.prev-aaaa000%d-1111-4222-8333-444455556666.log", n, n, n))
		if st := r.status().Get(n); st.State != status.Idle || !strings.HasPrefix(st.LastArchive, fmt.Sprintf("agent%d.prev-", n)) {
			t.Errorf("swim %d after archive: %+v", n, st)
		}
	}
	// --as names every lane's archive; all or nothing when one can't be archived.
	r.mustSwim("all", "--rerun")
	if out, code := r.swim("archive", "1", "2", "4", "--as", "batch"); code == 0 || !strings.Contains(out, "nothing archived") || !strings.Contains(out, "swim 4 has nothing to archive") {
		t.Fatalf("a lane without a log: %d %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(r.root, ".swim", "logs", "agent1.log")); err != nil {
		t.Fatal("lane 1 was archived although lane 4 couldn't be")
	}
	contains(t, "--as", r.mustSwim("archive", "1", "2", "--as", "batch"), "agent1.prev-batch.log", "agent2.prev-batch.log")
	// A job id is a lane too, and the older "name after the lane" form still works.
	contains(t, "job id + old form", r.mustSwim("archive", "aaaa0003", "cut", "over"), "agent3.prev-cut-over.log")
}

// long puts a test in the long batch (make test-long, and CI): go test
// -short (make test) skips it. Tests that take 2s or more go there.
func long(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("in the long batch: make test-long")
	}
}

// repoRoot is the swim repository's root (for test programs it builds).
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// readFile is a file in r's working tree, trimmed ("" if it isn't there).
func readFile(t *testing.T, r *repo, name string) string {
	t.Helper()
	data, _ := os.ReadFile(filepath.Join(r.root, name))
	return strings.TrimSpace(string(data))
}

// noTTY starts a command in its own session, with no controlling terminal.
func noTTY() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
