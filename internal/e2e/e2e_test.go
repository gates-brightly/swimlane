// Package e2e drives the built swim binary against a scratch repo.
package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	// The tests run a built binary; importing its code makes `go test`
	// rerun them (instead of reusing a cached pass) when that code changes.
	_ "github.com/gates-brightly/swimlane/internal/cli"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/version"
)

var bin string

func TestMain(m *testing.M) {
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

type repo struct {
	t    *testing.T
	root string
	env  []string
}

func newRepo(t *testing.T, deps string) *repo {
	t.Helper()
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	xdg := t.TempDir()
	r := &repo{t: t, root: root, env: append(os.Environ(),
		"XDG_CONFIG_HOME="+xdg,
		"PATH="+filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"),
		"NO_COLOR=1", "SWIM_ROOT=", "SWIM_LANE=", "STEP_LOG=", "SWIM_BIN=",
	)}
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
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

// lane script writes lane.N.sh with the given round and body.
func (r *repo) script(n int, round, body string) {
	r.t.Helper()
	s := fmt.Sprintf("#!/usr/bin/env bash\n# swim: syntax 2\n# Round: %s\n_swim_lib=$(\"${SWIM_BIN:-swim}\" lib) || exit 1; eval \"$_swim_lib\"\nlane_init %d\n%s\nsummary\n", round, n, body)
	if err := os.WriteFile(filepath.Join(r.root, fmt.Sprintf("lane.%d.sh", n)), []byte(s), 0o755); err != nil {
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
	f := r.status()
	if len(f.Lanes) != 4 || f.Lanes[3].State != status.Idle {
		t.Fatalf("status lanes: %+v", f.Lanes)
	}
}

func TestFullRunWithDependencies(t *testing.T) {
	r := newRepo(t, "{2: [1], 4: [2]}")
	r.script(1, "Lane one", `snapshot "state" echo state-v1
run "slow" sleep 1.5`)
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
	deadline := time.Now().Add(5 * time.Second)
	var mid *status.File
	for time.Now().Before(deadline) {
		f, err := status.Load(r.root)
		if err == nil && len(f.Lanes) == 4 && f.Get(1).CurrentStep == "slow" && f.Get(2).State == status.Waiting {
			mid = f
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if mid == nil {
		t.Fatalf("never saw the mid-run state; output so far:\n%s", out.String())
	}
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
	want := map[int]string{1: status.Passed, 2: status.Passed, 3: status.Failed, 4: status.Passed}
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

	// status --yaml is the raw file.
	raw, _ := os.ReadFile(status.Path(r.root))
	if got := r.mustSwim("status", "--yaml"); got != string(raw) {
		t.Errorf("status --yaml differs from the file")
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
	// A skipped lane records the round it skipped, so plan sees a retry, not a new job.
	if f.Get(4).Round != "four" || f.Get(4).FinishedAt == nil {
		t.Errorf("skipped lane should record its round: %+v", f.Get(4))
	}
	contains(t, "plan after skips", r.mustSwim("plan"), "[~] swim 4  four")
	if r.log(2) != "" {
		t.Error("skipped lane wrote a log")
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

	if out, code := r.swim("run", "9"); code == 0 || !strings.Contains(out, "lanes are numbered 1..4") {
		t.Errorf("run 9: %d %s", code, out)
	}
}

func TestAfterHeaderDiamond(t *testing.T) {
	r := newRepo(t, "") // no config deps: the scripts declare them
	r.script(1, "root", `run "write" bash -c 'sleep 0.5; echo root > out1'`)
	r.script(2, "child a", `gate "root output" test -s out1`)
	r.script(3, "child b", `gate "root output" test -s out1`)
	r.script(4, "join", `gate "children" true`)
	addHeader := func(n int, line string) {
		p := filepath.Join(r.root, fmt.Sprintf("lane.%d.sh", n))
		data, _ := os.ReadFile(p)
		os.WriteFile(p, []byte(strings.Replace(string(data), "\n", "\n"+line+"\n", 1)), 0o755)
	}
	addHeader(2, "# After: 1")
	addHeader(3, "# After: swim 1")
	addHeader(4, "# After: 2, 3")

	out := r.mustSwim("all") // every pending lane, like `swim run`
	contains(t, "output", out, "[2] waiting on swim 1", "[4] waiting on swim 2, 3")
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
	r.mustSwim("new", "1", "root", "--job", "root-job-0001")
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
	r.mustSwim("new", "1", "next root", "--force")
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
	r := newRepo(t, "")
	r.mustSwim("new", "1", "first round")
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
	r.script(2, "long", `run "sleep" sleep 3`)
	bg := r.cmd("bash", "lane.2.sh")
	if err := bg.Start(); err != nil {
		t.Fatal(err)
	}
	defer bg.Wait()
	waitFor(t, func() bool { return r.status().Get(2).State == status.Running })
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
	r.script(1, "interrupt me", `run "long" sleep 30
run "never" true`)
	c := r.cmd("bash", "lane.1.sh")
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return r.status().Get(1).CurrentStep == "long" })
	time.Sleep(200 * time.Millisecond)
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
	r.script(1, "killed", `run "long" sleep 30`)
	c := r.cmd("bash", "lane.1.sh")
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return r.status().Get(1).CurrentStep == "long" })
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
	full := r.mustSwim("--help")
	contains(t, "--help", full, "PLANNER WORKFLOW", "WORKER RULES", "SAFETY RULES", "READING RESULTS", "COMMAND DETAIL")
	contains(t, "run --help", r.mustSwim("run", "--help"), "waiting on swim N")
	contains(t, "help status", r.mustSwim("help", "status"), "--rebuild")
	if out, code := r.swim("bogus"); code != 2 || !strings.Contains(out, "unknown command") {
		t.Errorf("bogus: %d %s", code, out)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("condition not met within 5s")
}

var jobLineRE = regexp.MustCompile(`(?m)^job: (\S+)$`)

func TestJobIDsAndPinning(t *testing.T) {
	r := newRepo(t, "")
	out := r.mustSwim("new", "1", "pinned round")
	m := jobLineRE.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("new printed no job id:\n%s", out)
	}
	job := m[1]
	script, _ := os.ReadFile(filepath.Join(r.root, "lane.1.sh"))
	contains(t, "lane.1.sh", string(script), "# swim: syntax 2", "# Job:     "+job)
	if l := r.status().Get(1); l.PendingJob != job {
		t.Errorf("pending_job = %q", l.PendingJob)
	}

	// Steps see the job id, so they can tag resources as "ours".
	body := strings.Replace(string(script), `run "precheck" true`, `run "tag" bash -c 'echo "tag=$SWIM_JOB"'`, 1)
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
	r.mustSwim("new", "1", "different round", "--force", "--job", "orders-cutover-2")
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
	r.mustSwim("new", "1", "first goal", "--job", "job-one-0001")
	body, _ := os.ReadFile(filepath.Join(r.root, "lane.1.sh"))
	os.WriteFile(filepath.Join(r.root, "lane.1.sh"), []byte(strings.Replace(string(body), `run "precheck" true`, `run "precheck" false`, 1)), 0o755)
	r.mustSwim("new", "2", "second goal", "--job", "job-two-0002")
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
		"init lanes=4",
		"new swim 1 job=job-one-0001 first goal",
		"new swim 2 job=job-two-0002 second goal",
		fmt.Sprintf("lock created .swim.lock: breaking %d", version.Breaking),
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
	r.mustSwim("new", "1", "first round", "--job", "first-job-01")
	r.mustSwim("run", "1")
	r.mustSwim("archive", "1", "first")
	r.mustSwim("new", "1", "second round", "--force", "--job", "second-job-02")
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
	r.mustSwim("new", "1", "passes")
	r.script(2, "fails until fixed", `run "check" test -e fixed`)
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
	r.mustSwim("new", "1", "next round", "--force")
	if out := r.mustSwim("all"); !strings.Contains(out, "[1] started: next round") || strings.Contains(out, "[2] started") {
		t.Errorf("new round not picked up:\n%s", out)
	}
}

func TestJobsBeyondConfiguredLanes(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "inside", `run "ok" true`)
	r.script(6, "beyond", `run "ok" true`)
	// No terminal: no prompt, no config change, a notice with the command.
	out := r.mustSwim("all")
	contains(t, "notice", out, "1 job(s) waiting beyond swim 4", "lane.6.sh  beyond", "swim config --lanes 6")
	if r.log(6) != "" {
		t.Fatal("lane 6 ran without lanes being raised")
	}
	if out, code := r.swim("new", "6", "x"); code == 0 || !strings.Contains(out, "swim config --lanes 6") {
		t.Errorf("new 6: %d %s", code, out)
	}

	contains(t, "config --lanes", r.mustSwim("config", "--lanes", "6"), "lanes: 4 -> 6")
	out = r.mustSwim("all")
	if !strings.Contains(out, "[6] started: beyond") || strings.Contains(out, "waiting beyond") {
		t.Errorf("after raising lanes:\n%s", out)
	}
	if len(r.status().Lanes) != 6 {
		t.Errorf("status lanes = %d", len(r.status().Lanes))
	}
	// Can't drop a lane holding a pending round; can once it's stubbed.
	r.mustSwim("new", "5", "pending in five")
	if out, code := r.swim("config", "--lanes", "4"); code == 0 || !strings.Contains(out, "lane.5.sh holds a pending round") {
		t.Errorf("reduce over pending: %d %s", code, out)
	}
	r.mustSwim("stub", "5", "x")
	r.mustSwim("stub", "6", "x")
	r.mustSwim("config", "--lanes", "4")
	contains(t, ".swim.log", r.mustSwim("log"), "lanes        4 -> 6", "lanes        6 -> 4")
}

func TestPlan(t *testing.T) {
	r := newRepo(t, "")
	head := func(n int, after string) {
		p := filepath.Join(r.root, fmt.Sprintf("lane.%d.sh", n))
		data, _ := os.ReadFile(p)
		os.WriteFile(p, []byte(strings.Replace(string(data), "\n", "\n# After: "+after+"\n", 1)), 0o755)
	}
	r.script(1, "root", `run "ok" true`)
	r.script(2, "child a", `run "ok" true`)
	r.script(3, "child b", `run "ok" true`)
	r.script(4, "join", `if guard PLAN_ALLOW_X "do x"; then run "x" true; fi`)
	head(2, "1")
	head(3, "1")
	head(4, "2 3")

	out := r.mustSwim("plan")
	contains(t, "plan", out,
		"swim plan · ", "swim all",
		"[+] run: a new job",
		"[+] swim 1  root",
		"├── [+] swim 2  child a",
		"│   └── [+] swim 4  join  wait [2,3]",
		"guard PLAN_ALLOW_X=1 (unset: dry run)",
		"└── [+] swim 3  child b",
		"    └── [+] swim 4 (shown above)",
		"Plan: 4 to run, 0 to retry, 0 to rerun, 0 to skip.")
	if r.log(1) != "" || r.status().Get(1).State != status.Idle {
		t.Fatal("plan ran something")
	}

	c := r.cmd(bin, "plan")
	c.Env = append(c.Env, "PLAN_ALLOW_X=1")
	if out, _ := c.CombinedOutput(); !strings.Contains(string(out), "PLAN_ALLOW_X=1 (set: approved)") {
		t.Errorf("guard set not shown:\n%s", out)
	}

	// Lane 4 alone: its dependencies haven't passed, so it would be skipped.
	contains(t, "plan 4", r.mustSwim("plan", "4"), "swim run 4", "[-] swim 4  join", "skip: swim 2's pending round has not passed", "0 to run, 0 to retry, 0 to rerun, 1 to skip.")

	// Lane 3 fails once: it's a retry; the passed lanes are summarised.
	r.script(3, "child b", `run "flaky" false`)
	head(3, "1")
	r.swim("all", "--plain")
	r.script(3, "child b", `run "ok" true`)
	head(3, "1")
	out = r.mustSwim("plan")
	// Lane 4 was skipped below lane 3: the same job comes back, so it's a retry too.
	contains(t, "retry", out, "[~] retry: the job ran before and didn't pass", "[~] swim 3  child b", "└── [~] swim 4  join")
	contains(t, "retry", out, "Plan: 0 to run, 2 to retry, 0 to rerun, 0 to skip.", "2 items have completed with no remaining work.")
	if strings.Contains(out, "swim 1  root") {
		t.Errorf("completed lanes should be summarised, not listed:\n%s", out)
	}
	contains(t, "rerun", r.mustSwim("plan", "--rerun"), "[+/-] swim 1  root", "2 to rerun")

	// After a full run, nothing is left.
	r.mustSwim("all", "--plain")
	contains(t, "plan after run", r.mustSwim("plan"), "No changes. 4 items have completed with no remaining work.")

	// A cycle fails the plan just as it would fail the run.
	head(1, "4")
	if out, code := r.swim("plan", "--rerun"); code == 0 || !strings.Contains(out, "dependency cycle") {
		t.Errorf("cycle: %d %s", code, out)
	}
}

func TestOldRootLogsMigrate(t *testing.T) {
	r := newRepo(t, "")
	os.WriteFile(filepath.Join(r.root, "agent1.log"), []byte("=== ROUND START 2026-10-01T00:00:00Z swim 1 job=old-job-0001\nRound: old\n"), 0o644)
	os.WriteFile(filepath.Join(r.root, "agent1.prev-earlier.log"), []byte("old archive\n"), 0o644)
	out := r.mustSwim("status")
	contains(t, "status", out, "moved 2 lane log(s) to .swim/logs/")
	for _, f := range []string{"agent1.log", "agent1.prev-earlier.log"} {
		if _, err := os.Stat(filepath.Join(r.root, f)); err == nil {
			t.Errorf("%s still at the root", f)
		}
		if _, err := os.Stat(filepath.Join(r.root, ".swim", "logs", f)); err != nil {
			t.Errorf("%s not in .swim/logs: %v", f, err)
		}
	}
	contains(t, "log 1 --all", r.mustSwim("log", "1", "--all"), "==> agent1.prev-earlier.log <==", "job=old-job-0001  old")
	// Nothing operational left at the root after a run.
	r.script(2, "x", `run "ok" true`)
	r.mustSwim("run", "2")
	if m, _ := filepath.Glob(filepath.Join(r.root, "agent*.log")); len(m) > 0 {
		t.Errorf("logs at root: %v", m)
	}
}

func TestVersionAndLock(t *testing.T) {
	r := newRepo(t, "")
	if out := r.mustSwim("--version"); !regexp.MustCompile(fmt.Sprintf(`^swim %d\.(\d{8}|dev)`, version.Breaking)).MatchString(out) {
		t.Errorf("--version = %q", out)
	}
	lockPath := filepath.Join(r.root, ".swim.lock")
	r.script(1, "one", `run "ok" true`)
	r.mustSwim("plan")
	if _, err := os.Stat(lockPath); err == nil {
		t.Fatal("plan wrote a lock")
	}
	out := r.mustSwim("run", "1")
	contains(t, "first run", out, fmt.Sprintf("wrote .swim.lock (breaking version %d)", version.Breaking))
	data, _ := os.ReadFile(lockPath)
	contains(t, ".swim.lock", string(data), fmt.Sprintf("breaking: %d", version.Breaking), fmt.Sprintf("version: %d.", version.Breaking))
	if out := r.mustSwim("run", "1"); strings.Contains(out, "wrote .swim.lock") {
		t.Error("lock rewritten on second run")
	}
	contains(t, "lock", r.mustSwim("lock"), fmt.Sprintf("lock:  breaking version %d", version.Breaking), "ok:")
	contains(t, ".swim.log", r.mustSwim("log"), fmt.Sprintf("lock         created .swim.lock: breaking %d", version.Breaking))

	// A lock from a newer breaking version: refuse to run, with instructions.
	os.WriteFile(lockPath, []byte(fmt.Sprintf("breaking: %d\nversion: %d.20270101\n", version.Breaking+1, version.Breaking+1)), 0o644)
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
	contains(t, "plan", r.mustSwim("plan"), "warning: this repo needs a newer swim")
	if strings.Count(r.log(1), "== ROUND ") != 2 {
		t.Error("a lane ran despite the lock mismatch")
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

func TestSyntaxMigration(t *testing.T) {
	r := newRepo(t, "")
	os.WriteFile(filepath.Join(r.root, "lane.1.sh"), []byte(v1Lane), 0o755)
	os.MkdirAll(filepath.Join(r.root, ".swim", "logs"), 0o755)
	os.WriteFile(filepath.Join(r.root, ".swim", "logs", "agent1.log"), []byte(v1Log), 0o644)

	out := r.mustSwim("status")
	contains(t, "status", out, "migrated 2 file(s) to syntax 2 (originals in .swim/migrations/", "lane.1.sh", ".swim/logs/agent1.log")
	script, _ := os.ReadFile(filepath.Join(r.root, "lane.1.sh"))
	contains(t, "migrated lane.1.sh", string(script), "#!/usr/bin/env bash\n# swim: syntax 2\n", "# Created: 2026-10-01", "# 1. Snapshot (read-only)\nstage snapshot\n", "# 2. Checks\nstage check\n", "# 4. Verify\nstage verify\n")
	if !strings.HasPrefix(r.log(1), "# swim lane log | syntax 2 | swim 1\n") || !strings.Contains(r.log(1), "== END PASS") {
		t.Fatalf("migrated log:\n%s", r.log(1))
	}
	baks, _ := filepath.Glob(filepath.Join(r.root, ".swim", "migrations", "*", "lane.1.sh"))
	if len(baks) != 1 {
		t.Fatalf("backups: %v", baks)
	}
	if b, _ := os.ReadFile(baks[0]); string(b) != v1Lane {
		t.Fatal("backup is not the original")
	}
	contains(t, ".swim.log", r.mustSwim("log"), "migrate      2 file(s) to syntax 2")
	if out := r.mustSwim("status"); strings.Contains(out, "migrated") {
		t.Errorf("second load migrated again:\n%s", out)
	}

	// The migrated round runs, with its stages.
	r.mustSwim("run", "1")
	log := r.log(1)
	contains(t, "run after migration", log, "-- stage snapshot", "-- stage check", "-- stage verify", "stages: snapshot PASS | check PASS | change none | verify PASS")
	if l := r.status().Get(1); strings.Join(l.Stages, ",") != "snapshot PASS,check PASS,change none,verify PASS" {
		t.Errorf("status stages: %v", l.Stages)
	}
}

func TestSyntaxTooNew(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "fine", `run "ok" true`)
	os.WriteFile(filepath.Join(r.root, "lane.2.sh"), []byte("#!/usr/bin/env bash\n# swim: syntax 9\n# Round: from the future\n"), 0o755)
	for _, args := range [][]string{{"status"}, {"run", "1"}, {"all"}, {"plan"}} {
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
	out, code := r.swim("run", "2")
	if code == 0 || !strings.Contains(r.log(2), "  STOP  bad stage: deploy") || strings.Contains(r.log(2), "never") {
		t.Fatalf("unknown stage: %d %s\n%s", code, out, r.log(2))
	}
}

func TestTimeoutStopsRound(t *testing.T) {
	r := newRepo(t, "")
	p := filepath.Join(r.root, "lane.1.sh")
	r.script(1, "slow", `stage check
run "slow" sleep 20
run "after" true`)
	data, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(data), "# Round: slow\n", "# Round: slow\n# Timeout: 1s\n", 1)), 0o755)
	start := time.Now()
	out, code := r.swim("run", "1")
	if code == 0 || time.Since(start) > 15*time.Second {
		t.Fatalf("exit %d after %s:\n%s", code, time.Since(start), out)
	}
	log := r.log(1)
	contains(t, "agent1.log", log, "timeout: 1s", "  FAIL  slow (timeout: Timeout 1s reached)", "  STOP  timeout: the round's Timeout (1s) ran out", "== END FAIL")
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
	r := newRepo(t, "")
	r.mustSwim("config", "--lanes", "10")
	for n := 1; n <= 10; n++ {
		r.script(n, fmt.Sprintf("sleeper %d", n), `run "sleep" sleep 1`)
	}
	c := r.cmd(bin, "all", "--parallel", "3", "--plain")
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	start := time.Now()
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error)
	go func() { done <- c.Wait() }()
	most := 0
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
loop:
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("run: %v\n%s", err, out.String())
			}
			break loop
		case <-tick.C:
			pids, _ := filepath.Glob(filepath.Join(r.root, ".swim", "lane*.pid"))
			most = max(most, len(pids))
		}
	}
	took := time.Since(start)
	if most > 3 || most < 2 {
		t.Errorf("at most 3 lanes should run at once; saw %d", most)
	}
	if took < 3500*time.Millisecond || took > 9*time.Second {
		t.Errorf("10 one-second lanes, 3 at a time, should take about 4s; took %s", took)
	}
	s := out.String()
	contains(t, "output", s, "queued (", "swim summary")
	for n := 1; n <= 10; n++ {
		if st := r.status().Get(n).State; st != status.Passed {
			t.Errorf("swim %d = %s", n, st)
		}
	}
	contains(t, ".swim.log", r.mustSwim("log"), "max_parallel=3", "queued       swim ")

	// Config sets the default; --parallel 0 lifts it.
	cfg := filepath.Join(strings.TrimPrefix(envOf(r, "XDG_CONFIG_HOME"), ""), "swim", "config.yml")
	data, _ := os.ReadFile(cfg)
	os.WriteFile(cfg, []byte(strings.Replace(string(data), "defaults:\n", "defaults:\n  max_parallel: 2\n", 1)), 0o644)
	contains(t, "config", r.mustSwim("config"), "max_parallel: 2")
	start = time.Now()
	r.mustSwim("all", "--rerun", "--parallel", "0", "--plain")
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("--parallel 0 should run all 10 at once; took %s", took)
	}
	if out, code := r.swim("all", "--parallel", "-1"); code == 0 || !strings.Contains(out, "--parallel") {
		t.Errorf("--parallel -1: %d %s", code, out)
	}
}

func envOf(r *repo, key string) string {
	for _, kv := range r.env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// withLocks adds a "# Locks:" header line to lane n.
func withLocks(t *testing.T, r *repo, n int, locks string) {
	t.Helper()
	p := filepath.Join(r.root, fmt.Sprintf("lane.%d.sh", n))
	data, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(data), "\n# Round:", "\n# Locks: "+locks+"\n# Round:", 1)), 0o755)
}

// overlapWatcher samples lane pidfiles until stop is closed, recording the
// pairs of lanes that were ever running at the same time.
func overlapWatcher(r *repo, stop chan struct{}) <-chan map[[2]int]bool {
	out := make(chan map[[2]int]bool, 1)
	go func() {
		seen := map[[2]int]bool{}
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				out <- seen
				return
			case <-tick.C:
				var live []int
				for n := 1; n <= 9; n++ {
					if _, err := os.Stat(filepath.Join(r.root, ".swim", fmt.Sprintf("lane%d.pid", n))); err == nil {
						live = append(live, n)
					}
				}
				for i := range live {
					for j := i + 1; j < len(live); j++ {
						seen[[2]int{live[i], live[j]}] = true
					}
				}
			}
		}
	}()
	return out
}

func TestResourceLocksWithinARun(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "migrate table", `run "work" bash -c 'sleep 1; exit 1'`)
	r.script(2, "back up table", `run "work" sleep 1`)
	withLocks(t, r, 1, "orders-table")
	withLocks(t, r, 2, "orders-table")
	contains(t, "plan", r.mustSwim("plan"), "locks orders-table", "Lock orders-table: swim 1, 2 take turns")

	stop := make(chan struct{})
	seen := overlapWatcher(r, stop)
	start := time.Now()
	out, _ := r.swim("all", "--plain")
	close(stop)
	took := time.Since(start)
	if (<-seen)[[2]int{1, 2}] {
		t.Error("lanes sharing a lock ran at the same time")
	}
	if took < 1900*time.Millisecond || took > 6*time.Second {
		t.Errorf("two one-second lanes taking turns should take about 2s; took %s", took)
	}
	// A failure doesn't spread through a lock: lane 2 still ran and passed.
	if st := r.status(); st.Get(1).State != status.Failed || st.Get(2).State != status.Passed {
		t.Fatalf("states: 1=%s 2=%s\n%s", st.Get(1).State, st.Get(2).State, out)
	}
	contains(t, "output", out, "waiting for lock orders-table: swim ")
	contains(t, ".swim.log", r.mustSwim("log"), "locked       swim ")

	// Locks {a}, {a,b}, {b}: no deadlock; sharers never overlap.
	r2 := newRepo(t, "")
	for n, locks := range map[int]string{1: "a", 2: "a, b", 3: "b"} {
		r2.script(n, fmt.Sprintf("lane %d", n), `run "work" sleep 0.5`)
		withLocks(t, r2, n, locks)
	}
	stop2 := make(chan struct{})
	seen2 := overlapWatcher(r2, stop2)
	done := make(chan string, 1)
	go func() { o, _ := r2.swim("all", "--plain"); done <- o }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("deadlock: run with overlapping lock sets never finished")
	}
	close(stop2)
	pairs := <-seen2
	if pairs[[2]int{1, 2}] || pairs[[2]int{2, 3}] {
		t.Errorf("lanes sharing a lock overlapped: %v", pairs)
	}
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
	r := newRepo(t, "")
	r.script(1, "one", `run "work" sleep 1.5`)
	r.script(2, "two", `run "work" sleep 0.2`)
	withLocks(t, r, 1, "tf/orders")
	withLocks(t, r, 2, "tf/orders")

	// Two concurrent swim run processes respect the same lock.
	stop := make(chan struct{})
	seen := overlapWatcher(r, stop)
	a := r.cmd(bin, "run", "1", "--plain")
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return r.status().Get(1).State == status.Running })
	contains(t, "status while held", r.mustSwim("status"), "lock tf/orders held by swim 1", "locks: tf/orders")
	out, err := r.cmd(bin, "run", "2", "--plain").CombinedOutput()
	a.Wait()
	close(stop)
	if err != nil {
		t.Fatalf("run 2: %v\n%s", err, out)
	}
	if (<-seen)[[2]int{1, 2}] {
		t.Error("two runs overlapped lanes holding the same lock")
	}
	contains(t, "run 2 output", string(out), "waiting for lock tf/orders: another run, swim 1")

	// A killed run doesn't leave the lock behind.
	r.script(1, "one", `run "work" sleep 30`)
	withLocks(t, r, 1, "tf/orders")
	k := r.cmd(bin, "run", "1", "--plain")
	k.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := k.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return r.status().Get(1).CurrentStep == "work" })
	syscall.Kill(-k.Process.Pid, syscall.SIGKILL)
	k.Wait()
	start := time.Now()
	if out, err := r.cmd(bin, "run", "2", "--plain").CombinedOutput(); err != nil || time.Since(start) > 5*time.Second {
		t.Fatalf("lock left behind by a killed run: %v after %s\n%s", err, time.Since(start), out)
	}
}

func TestBadLockNameWarns(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "bad lock", `run "x" true`)
	withLocks(t, r, 1, "ok-name, bad*name")
	r.mustSwim("run", "1")
	contains(t, "agent1.log", r.log(1), `WARN  Locks: "bad*name" is not a lock name`)
}
