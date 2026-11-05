package e2e

import (
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/launcher"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/testwait"
	"gopkg.in/yaml.v3"
)

// The scenarios run whole workflows at scale: real lane scripts (from
// e2e/scenarios, or generated: dag99_test.go) through the scheduler,
// dependencies and lane library together. Everything is offline:
// e2e/lib/fakebin/curl serves e2e/fixtures/pages, and the lanes do their
// work with e2etool (e2e/cmd/e2etool). Skipped with -short.

// e2eDir is the repo's e2e/ directory.
func e2eDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "e2e")
}

var e2etoolOnce sync.Once

// newScenario is a repo for a scenario: e2etool, built once next to the
// swim under test, and the fake curl first on PATH.
func newScenario(t *testing.T) *repo {
	t.Helper()
	if testing.Short() {
		t.Skip("scenarios skipped with -short")
	}
	var buildErr error
	var buildOut []byte
	e2etoolOnce.Do(func() {
		c := exec.Command("go", "build", "-buildvcs=false", "-o", filepath.Join(filepath.Dir(bin), "e2etool"), "./e2e/cmd/e2etool")
		c.Dir = filepath.Join(e2eDir(), "..")
		buildOut, buildErr = c.CombinedOutput()
	})
	if buildErr != nil {
		t.Fatalf("building e2etool: %v\n%s", buildErr, buildOut)
	}
	r := newRepo(t)
	r.env = append(r.env,
		"PATH="+filepath.Join(e2eDir(), "lib", "fakebin")+string(os.PathListSeparator)+filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"),
		"E2E_FIXTURES="+filepath.Join(e2eDir(), "fixtures"))
	return r
}

// linuxOnly skips a dag99 variant that tests the scheduler (locks,
// retries, a parallel cap) on 99 lanes on macOS: 100-140s each on a macOS
// runner, the long pole of CI there (T32). What macOS adds, bash 3.2 and
// BSD userland, plain dag99, -interrupt and -force still cover.
func linuxOnly(t *testing.T) {
	if runtime.GOOS == "darwin" && os.Getenv("SWIM_E2E_ALL_SCENARIOS") == "" {
		t.Skip("Linux only (T32); SWIM_E2E_ALL_SCENARIOS=1 runs it")
	}
}

// lanesFrom copies e2e/scenarios/NAME/lanes/lane.*.sh into r.
func (r *repo) lanesFrom(name string) {
	r.t.Helper()
	paths, _ := filepath.Glob(filepath.Join(e2eDir(), "scenarios", name, "lanes", "lane.*.sh"))
	if len(paths) == 0 {
		r.t.Fatalf("no lanes in e2e/scenarios/%s/lanes", name)
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			r.t.Fatal(err)
		}
		r.write(filepath.Base(p), string(data), 0o644)
	}
}

// swimStdout is swimEnv's stdout alone, for output to parse.
func (r *repo) swimStdout(env []string, args ...string) (string, int) {
	c := r.cmd(bin, args...)
	c.Env = append(append([]string(nil), r.env...), env...)
	out, err := c.Output()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		r.t.Fatalf("swim %v: %v", args, err)
	}
	return string(out), code
}

// commitLanes commits every lane script.
func (r *repo) commitLanes(msg string) {
	r.t.Helper()
	paths, _ := filepath.Glob(filepath.Join(r.root, "lane.*.sh"))
	for i, p := range paths {
		paths[i] = filepath.Base(p)
	}
	r.git(append([]string{"add", "-f"}, paths...)...)
	r.git("commit", "-q", "-m", msg)
}

// runResult is a `swim run --yaml` stream's outcome: each lane's result
// (passed, failed, skipped, interrupted) and the lanes that started after
// a stop was requested.
type runResult struct {
	lanes     map[int]string
	afterStop []int
	stream    string
}

func parseRun(t *testing.T, stream string) runResult {
	t.Helper()
	res := runResult{lanes: map[int]string{}, stream: stream}
	dec := yaml.NewDecoder(strings.NewReader(stream))
	stopped, summary := false, false
	for {
		var doc map[string]any
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("swim run --yaml: %v\n%s", err, stream)
		}
		switch doc["event"] {
		case "stop_requested", "force_quit":
			stopped = true
		case "start":
			if stopped {
				res.afterStop = append(res.afterStop, doc["lane"].(int))
			}
		case "summary":
			data, _ := yaml.Marshal(doc)
			var s launcher.Summary
			if err := yaml.Unmarshal(data, &s); err != nil {
				t.Fatal(err)
			}
			for _, l := range s.Lanes {
				res.lanes[l.Lane] = l.Result
			}
			summary = true
		}
	}
	if !summary {
		t.Fatalf("no summary event in the stream:\n%s", stream)
	}
	return res
}

// with is the lanes whose result is one of results, as "1 2 3".
func (res runResult) with(results ...string) string {
	var ns []int
	for n, got := range res.lanes {
		for _, want := range results {
			if got == want {
				ns = append(ns, n)
			}
		}
	}
	sort.Ints(ns)
	return joinInts(ns, " ")
}

// runYAML is swim ARGS --yaml (run or all), with extra environment.
func (r *repo) runYAML(env []string, args ...string) (runResult, int) {
	r.t.Helper()
	out, code := r.swimStdout(env, append(args, "--yaml")...)
	return parseRun(r.t, out), code
}

// stepOutput is the output of the steps labelled label in lane n's latest
// round.
func (r *repo) stepOutput(n int, label string) string {
	r.t.Helper()
	out, _ := r.swimStdout(nil, "log", fmt.Sprint(n), "--yaml")
	var d logparse.Doc
	if err := yaml.Unmarshal([]byte(out), &d); err != nil {
		r.t.Fatalf("swim log %d --yaml: %v", n, err)
	}
	var lines []string
	for _, res := range d.Results {
		if res.Step && strings.Contains(res.Label, label) {
			lines = append(lines, res.Output...)
		}
	}
	return strings.Join(lines, "\n")
}

// rounds is how many rounds lane n's log has.
func (r *repo) rounds(n int) int {
	out, _ := r.swimEnv(nil, "log", fmt.Sprint(n), "--raw")
	return len(regexp.MustCompile(`(?m)^== ROUND `).FindAllString(out, -1))
}

func eq(t *testing.T, what string, got, want any) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("%s:\n got:  %v\n want: %v", what, got, want)
	}
}

func matches(t *testing.T, what, text, re string) {
	t.Helper()
	if !regexp.MustCompile("(?m)" + re).MatchString(text) {
		t.Errorf("%s: no line matching /%s/ in:\n%s", what, re, text)
	}
}

func seqList(a, b int) string {
	var ns []int
	for i := a; i <= b; i++ {
		ns = append(ns, i)
	}
	return joinInts(ns, " ")
}

func allLanes() []string { return strings.Fields(seqList(1, 99)) }

// checkAudit checks lane 99's audit of a run where every lane passed.
func checkAudit(t *testing.T, r *repo) string {
	t.Helper()
	audit := r.stepOutput(99, "audit the DAG")
	matches(t, "audit passed", audit, `^PASS: every node done, every edge honoured, every value matches`)
	matches(t, "edges all ok", audit, `^edges: 197 checked, 197 ok`)
	for _, l := range strings.Split(audit, "\n") {
		if strings.HasPrefix(l, "scheduler latency") || strings.HasPrefix(l, "makespan") ||
			strings.HasPrefix(l, "concurrency") || strings.HasPrefix(l, "locks") {
			t.Log(l)
		}
	}
	return audit
}

// TestScenarioDag99: 99 lanes through the scheduler, every edge and value
// audited, the independent roots started together.
func TestScenarioDag99(t *testing.T) {
	r := newScenario(t)
	dag99(t, r, false)
	res, code := r.runYAML(nil, append([]string{"run"}, allLanes()...)...)
	eq(t, "swim run exit", code, 0)
	eq(t, "lanes passed", res.with("passed"), seqList(1, 99))
	audit := checkAudit(t, r)
	matches(t, "values all match", audit, `^values: 89 recomputed, 89 match`)
	matches(t, "roots started together", audit, `^roots in this run \[1, 8, 20, 21, 22\]: .* ok$`)
	matches(t, "lane 1 recorded its stages", must(r.swim("log", "1", "--raw")), `^   stages: .*check PASS \| change PASS \| verify PASS`)
	matches(t, "audit lane recorded its stages", must(r.swim("log", "99", "--raw")), `^   stages: .*check PASS \| change none \| verify PASS`)
}

// dagFail are the lanes the failure scenarios fail on purpose: 23 heads
// the 15-lane chain, 38 is the wide fan-in, and 41 is downstream of both,
// so it is skipped, not failed.
var dagFail = []int{23, 38, 41}

// dagFailEnv injects dagFail.
func dagFailEnv() []string { return []string{"E2E_FAIL=" + joinInts(dagFail, " ")} }

// wantFailed are the dagFail lanes not downstream of another.
func wantFailed(t *testing.T, r *repo) (failed, skipped []int) {
	skipped = descendants(t, r, dagFail...)
	down := map[int]bool{}
	for _, n := range skipped {
		down[n] = true
	}
	for _, n := range dagFail {
		if !down[n] {
			failed = append(failed, n)
		}
	}
	return failed, skipped
}

// TestScenarioDag99Cascade: with lanes failing, the skipped lanes are
// exactly their descendants, and everything else passes.
func TestScenarioDag99Cascade(t *testing.T) {
	r := newScenario(t)
	dag99(t, r, false)
	res, code := r.runYAML(dagFailEnv(), append([]string{"run"}, allLanes()...)...)
	failed, skipped := wantFailed(t, r)
	eq(t, "swim run exit", code, 1)
	eq(t, "failed = injected lanes not downstream of another", res.with("failed"), joinInts(failed, " "))
	eq(t, "skipped = descendants of the failures", res.with("skipped"), joinInts(skipped, " "))
	eq(t, "every lane has a result", len(res.lanes), 99)
	eq(t, "the rest passed", len(strings.Fields(res.with("passed"))), 99-len(failed)-len(skipped))
	t.Logf("%d lanes skipped downstream of %v", len(skipped), dagFail)
}

// TestScenarioDag99Parallel: with --parallel 8, never more than 8 lanes
// run at once (the audit measures it), and every dag99 check still holds.
func TestScenarioDag99Parallel(t *testing.T) {
	linuxOnly(t)
	r := newScenario(t)
	dag99(t, r, false)
	res, code := r.runYAML([]string{"E2E_MAX_PARALLEL=8"}, append([]string{"run", "--parallel", "8"}, allLanes()...)...)
	eq(t, "swim run exit", code, 0)
	eq(t, "lanes passed", res.with("passed"), seqList(1, 99))
	audit := checkAudit(t, r)
	matches(t, "values all match", audit, `^values: 89 recomputed, 89 match`)
	matches(t, "never more than 8 at once", audit, `^concurrency: at most [1-8] lanes running at once \(limit 8\)  ok$`)
	matches(t, "lanes queued for a slot", res.stream, `^event: queued$`)
}

// TestScenarioDag99Locks: lanes take locks from a pool of five; lanes
// sharing one never overlap, and lock waits never skip a lane.
func TestScenarioDag99Locks(t *testing.T) {
	linuxOnly(t)
	r := newScenario(t)
	dag99(t, r, true)
	res, code := r.runYAML(nil, append([]string{"run"}, allLanes()...)...)
	eq(t, "swim run exit", code, 0)
	eq(t, "every lane passed (lock waits never skip)", res.with("passed"), seqList(1, 99))
	audit := checkAudit(t, r)
	matches(t, "no lanes sharing a lock overlapped", audit, `^locks: [0-9]+ lanes hold locks, [0-9]+ sharing pairs checked, 0 overlapped$`)
	matches(t, "lanes waited for locks", res.stream, `^lock: '?pool/`)
}

// TestScenarioDag99Retry: swim all with lanes failing, then again with
// them fixed, reruns exactly the lanes that didn't pass, never one that
// did; a third swim all has nothing to do.
func TestScenarioDag99Retry(t *testing.T) {
	linuxOnly(t)
	r := newScenario(t)
	dag99(t, r, false)
	first, code := r.runYAML(dagFailEnv(), "all")
	eq(t, "first swim all exit (failures injected)", code, 1)
	redo := first.with("failed", "skipped", "interrupted")
	if redo == "" {
		t.Fatal("the injected failures left nothing to redo")
	}
	retry, code := r.runYAML(nil, "all")
	eq(t, "retry exit", code, 0)
	eq(t, "every retried lane passed", retry.with("passed"), redo)
	eq(t, "retry ran exactly the lanes that didn't pass", retry.with("passed", "failed", "skipped", "interrupted"), redo)
	matches(t, "audit passed on the retry", r.stepOutput(99, "audit the DAG"), `^PASS: every node done, every edge honoured, every value matches`)
	var rerun []string
	for _, n := range strings.Fields(first.with("passed")) {
		var k int
		fmt.Sscan(n, &k)
		if r.rounds(k) != 1 {
			rerun = append(rerun, n)
		}
	}
	eq(t, "lanes that passed first time were never rerun", rerun, []string(nil))
	out, code := r.swim("all", "--plain")
	eq(t, "third swim all exit", code, 0)
	matches(t, "third swim all: nothing to run", out, `^swim: nothing to run: every pending job has already passed`)
}

// startDag99 starts swim all --yaml on dag99 with lane 1 held mid-run
// (E2E_HOLD), and waits until it holds, so a signal lands while the run
// is certainly in progress, however fast the machine is. release ends the
// hold; wait returns the stream and exit code.
func startDag99(t *testing.T, r *repo) (c *exec.Cmd, stream *syncBuffer, release func(), wait func(time.Duration) (int, bool)) {
	t.Helper()
	hold := filepath.Join(r.root, ".scenario", "hold")
	os.MkdirAll(filepath.Dir(hold), 0o755) // watched before lane 1 writes hold.held in it
	c = r.cmd(bin, "all", "--yaml")
	c.Env = append(c.Env, "E2E_HOLD="+hold)
	stream = &syncBuffer{}
	c.Stdout = stream
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
		output.Notify()
	}()
	release = func() { os.WriteFile(hold, nil, 0o644) }
	t.Cleanup(func() { release(); c.Process.Kill(); <-done })
	wait = func(within time.Duration) (int, bool) {
		select {
		case <-done:
			return code, true
		case <-time.After(within):
			return 0, false
		}
	}
	exited := func() bool { _, ok := wait(0); return ok }
	waitFor(t, func() bool { _, err := os.Stat(hold + ".held"); return err == nil || exited() })
	if exited() {
		t.Fatalf("swim ended before lane 1 held at E2E_HOLD:\n%s", stream)
	}
	return c, stream, release, wait
}

// TestScenarioDag99Interrupt: one Ctrl-C mid-run, when a random lane
// outside lane 1's subtree (it runs while lane 1 holds) starts or ends.
// Every lane ends passed, interrupted at a step boundary, or skipped: no
// step is cut off mid-command, and no lane starts after the signal.
func TestScenarioDag99Interrupt(t *testing.T) {
	r := newScenario(t)
	dag99(t, r, false)
	under1 := map[int]bool{}
	for _, n := range descendants(t, r, 1) {
		under1[n] = true
	}
	var free []int
	for n := 2; n <= 98; n++ {
		if !under1[n] {
			free = append(free, n)
		}
	}
	c, stream, release, wait := startDag99(t, r)
	lane := free[rand.Intn(len(free))]
	log := filepath.Join(r.root, ".swim", "logs", fmt.Sprintf("lane%d.log", lane))
	when := fmt.Sprintf("lane %d started", lane)
	if rand.Intn(2) == 0 {
		waitFor(t, func() bool { _, err := os.Stat(log); return err == nil })
	} else {
		when = fmt.Sprintf("lane %d ended", lane)
		waitFor(t, func() bool { data, _ := os.ReadFile(log); return regexp.MustCompile(`(?m)^== END`).Match(data) })
	}
	c.Process.Signal(os.Interrupt)
	release() // lane 1's held step ends; the lane stops at the next boundary
	code, ok := wait(waitDeadline)
	if !ok {
		t.Fatal("swim didn't end after Ctrl-C")
	}
	res := parseRun(t, stream.String())
	if code == 0 {
		t.Error("interrupted run exited 0")
	}
	eq(t, "every lane passed, interrupted or skipped", len(strings.Fields(res.with("passed", "interrupted", "skipped"))), 99)
	eq(t, "no lane started after the signal", res.afterStop, []int(nil))
	logs, _ := filepath.Glob(filepath.Join(r.root, ".swim", "logs", "lane*.log"))
	for _, l := range logs {
		data, _ := os.ReadFile(l)
		if m := regexp.MustCompile(`(?m)^  FAIL  .*interrupted\)`).Find(data); m != nil {
			t.Errorf("%s: a step was cut off mid-command: %s", filepath.Base(l), m)
		}
	}
	matches(t, "the stop was requested", res.stream, `^event: stop_requested$`)
	t.Logf("Ctrl-C once %s (lane 1 held)", when)
}

// TestScenarioDag99Force: Ctrl-C twice with interrupt_grace 1s: swim
// exits, with every lane process gone, within the grace plus 2s (scaled:
// 99 lanes stop beside the rest of the suite) of the second press.
func TestScenarioDag99Force(t *testing.T) {
	r := newScenario(t)
	dag99(t, r, false)
	r.mustSwim("config", "interrupt_grace", "1s")
	c, stream, release, wait := startDag99(t, r)
	c.Process.Signal(os.Interrupt)
	// The second press counts as one only once swim has taken the first.
	waitFor(t, func() bool { return regexp.MustCompile(`(?m)^event: stop_requested$`).MatchString(stream.String()) })
	c.Process.Signal(os.Interrupt)
	t0 := time.Now()
	limit := time.Second + scaled(2*time.Second)
	code, ok := wait(limit + time.Second)
	took := time.Since(t0)
	release() // a held step force quit missed would end now, not linger
	if !ok {
		code, ok = wait(waitDeadline)
	}
	if !ok {
		t.Fatal("swim didn't end after a force quit")
	}
	if took > limit {
		t.Errorf("swim took %.2fs to exit after the second Ctrl-C, over %s", took.Seconds(), limit)
	}
	if code == 0 {
		t.Error("force-quit run exited 0")
	}
	// A killed group can take a moment to go: one still there after 2s
	// (scaled) survived.
	out, _ := exec.Command("pgrep", "-f", filepath.Join(r.root, ".swim", "runs")+"/").Output()
	for _, p := range strings.Fields(string(out)) {
		var pid int
		fmt.Sscan(p, &pid)
		if !testwait.Exited(pid, scaled(2*time.Second)) {
			t.Errorf("lane process %d survived the force quit", pid)
			syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	res := parseRun(t, stream.String())
	matches(t, "force quit was announced", res.stream, `^event: force_quit$`)
	eq(t, "lanes passed, interrupted or skipped", len(strings.Fields(res.with("passed", "interrupted", "skipped"))), 99)
	t.Logf("force quit took %.2fs", took.Seconds())
}

// TestScenarioCIGitHub: dag99 with failures under swim ci as GitHub
// Actions: an ::error per failed lane, one notice for the skipped ones, a
// job summary row and a JUnit suite for every lane.
func TestScenarioCIGitHub(t *testing.T) {
	r := newScenario(t)
	dag99(t, r, false)
	r.commitLanes("lanes")
	out := t.TempDir()
	ciOut, code := r.swimEnv(append(dagFailEnv(), "GITHUB_ACTIONS=true", "CI=true", "GITHUB_STEP_SUMMARY="+filepath.Join(out, "summary.md")),
		"ci", "--junit", filepath.Join(out, "swim.xml"))
	eq(t, "swim ci exit (failures injected)", code, 1)
	failed, _ := wantFailed(t, r)
	var errs []int
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^::error title=swim ([0-9]+)%3A`).FindAllStringSubmatch(ciOut, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			var n int
			fmt.Sscan(m[1], &n)
			errs = append(errs, n)
		}
	}
	sort.Ints(errs)
	eq(t, "an ::error for each failed lane", errs, failed)
	eq(t, "one notice for all the skipped lanes", len(regexp.MustCompile(`(?m)^::notice title=swim%3A [0-9]* lanes skipped::`).FindAllString(ciOut, -1)), 1)
	matches(t, "failed lanes stay open", ciOut, `^==== lane 23 FAIL - `)
	matches(t, "passed lanes fold", ciOut, `^::group::lane 1 PASS - `)
	summary, _ := os.ReadFile(filepath.Join(out, "summary.md"))
	eq(t, "job summary has every lane", len(regexp.MustCompile(`(?m)^\| lane [0-9]* \|`).FindAll(summary, -1)), 99)
	junit, _ := os.ReadFile(filepath.Join(out, "swim.xml"))
	eq(t, "JUnit has a suite per lane", strings.Count(string(junit), "<testsuite "), 99)
	matches(t, "results tagged with the commit", must(r.swim("status", "--yaml")), `^    commit: `+r.git("rev-parse", "HEAD"))
}

// TestScenarioCIChanged: dag99 passes; then a commit touches lanes 30 and
// 60, and swim ci --changed=<base> runs exactly those two.
func TestScenarioCIChanged(t *testing.T) {
	r := newScenario(t)
	dag99(t, r, false)
	r.commitLanes("lanes")
	_, code := r.swim("all", "--plain")
	eq(t, "first run exit", code, 0)
	base := r.git("rev-parse", "HEAD")
	for _, n := range []string{"30", "60"} {
		f, _ := os.OpenFile(filepath.Join(r.root, "lane."+n+".sh"), os.O_APPEND|os.O_WRONLY, 0)
		f.WriteString("\n# touched by the test\n")
		f.Close()
	}
	r.commitLanes("touch 30 and 60")
	out, code := r.swimEnv([]string{"CI=true"}, "ci", "--changed="+base)
	eq(t, "swim ci exit", code, 0)
	matches(t, "selected the changed lanes", out, `^selected: --changed: since [0-9a-f]+; changed: lane\.30\.sh, lane\.60\.sh$`)
	matches(t, "ran lanes 30 and 60", out, `^run r-[0-9TZ-]+[0-9a-f]* · lanes 30, 60$`)
	var bad []int
	for n := 1; n <= 99; n++ {
		want := 1
		if n == 30 || n == 60 {
			want = 2
		}
		if r.rounds(n) != want {
			bad = append(bad, n)
		}
	}
	eq(t, "only the changed lanes ran again", bad, []int(nil))
}

// TestScenarioBlocked: swim blocks only a repo's blocked_commands: a
// literal match is refused before the lane starts, one built at runtime
// at the step. Blocked lanes fail and their dependents are skipped; a lane
// that commits passes and its commits land.
func TestScenarioBlocked(t *testing.T) {
	r := newScenario(t)
	r.lanesFrom("blocked")
	r.repoConfig("blocked_commands: [\"terraform destroy\"]\n")
	r.git("commit", "-q", "--allow-empty", "-m", "base")
	head := r.git("rev-parse", "HEAD")
	status := func() string { return r.git("status", "--porcelain") }
	before := status()
	res, code := r.runYAML(nil, "run", "1", "2", "3", "4", "5", "6")
	eq(t, "swim run exit", code, 1)
	eq(t, "blocked lanes failed", res.with("failed"), "1 2")
	eq(t, "their dependents were skipped", res.with("skipped"), "4 5")
	eq(t, "the git lanes passed", res.with("passed"), "3 6")
	matches(t, "lane 1 refused before starting", res.stream, `blocked: lane\.1\.sh:[0-9]+ matches "terraform destroy"`)
	log1 := must(r.swim("log", "1", "--raw"))
	eq(t, "lane 1 never started (one round, the refusal's)", r.rounds(1), 1)
	matches(t, "lane 1's round says why", log1, `^  STOP  not started: blocked: lane\.1\.sh:[0-9]+ matches "terraform destroy"`)
	eq(t, "lane 1 ran no step", len(regexp.MustCompile(`(?m)^  (PASS|FAIL)  `).FindAllString(log1, -1)), 0)
	if l := laneOf(r, 1); l.ExitCode == nil || *l.ExitCode != 87 {
		t.Errorf("lane 1 exit code = %v, want 87", l.ExitCode)
	}
	log2 := must(r.swim("log", "2", "--raw"))
	matches(t, "lane 2 BLOCKED at the step", log2, `^  BLOCKED  tear down \(matched "terraform destroy"; blocked_commands\)`)
	if strings.Contains(log2, "PASS  after the blocked command") {
		t.Error("lane 2 ran its next step after the blocked one")
	}
	eq(t, "lane 3's two commits landed", r.git("rev-list", "--count", head+"..HEAD"), "2")
	eq(t, "working tree unchanged", status(), before)
}

// TestScenarioFlaky: steps that fail their first N calls pass on the
// right attempt with enough --retry, and fail after the last with too
// few; skips spread as usual.
func TestScenarioFlaky(t *testing.T) {
	r := newScenario(t)
	r.lanesFrom("flaky")
	res, code := r.runYAML(nil, "run", "1", "2", "3", "4")
	eq(t, "swim run exit", code, 1)
	eq(t, "passed", res.with("passed"), "1 3")
	eq(t, "failed", res.with("failed"), "2")
	eq(t, "skipped", res.with("skipped"), "4")
	matches(t, "lane 1 passed on its third attempt", must(r.swim("log", "1", "--raw")), `^  PASS  fails twice \(attempt 3/3\)`)
	log2 := must(r.swim("log", "2", "--raw"))
	matches(t, "lane 2 failed after its third attempt", log2, `^  FAIL  fails three times \(exit 1, attempt 3/3\)`)
	eq(t, "every attempt is in the log", len(regexp.MustCompile(`(?m)^        -- attempt `).FindAllString(log2, -1)), 3)
	matches(t, "attempt output kept", r.stepOutput(1, "fails twice"), `call 3`)
}
