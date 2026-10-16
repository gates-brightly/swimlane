package e2e

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/status"
)

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
		case <-time.After(30 * time.Second):
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

// waitLong is waitFor with room for a slow machine.
func waitLong(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("condition not met within 20s")
}

// swim pause lets the step finish, then holds the round before its next
// one; swim resume (swim unpause) lets it go on. The log has PAUSE and
// RESUME, which count toward nothing; .swim.log says unpause whichever
// name was typed; verify holds.
func TestPauseResume(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "three steps", `run "one" sleep 0.5
run "two" true
run "three" true`)
	wait := startRun(t, r, "run", "1", "--plain")
	waitLong(t, func() bool { return laneOf(r, 1).CurrentStep == "one" })
	contains(t, "pause", r.mustSwim("pause", "1", "--wait", "--note", "checking the plan"), `swim 1: pausing after "one"`)
	l := laneOf(r, 1)
	if l.PausedAt == "" || l.PausedAfter != "one" || l.NextStep != "two" || l.State != "running" {
		t.Fatalf("status while paused: %+v", l)
	}
	contains(t, "status", r.mustSwim("status", "1"), "paused", `after "one"; next: "two" (checking the plan)`, "resume: swim unpause 1")
	time.Sleep(700 * time.Millisecond)
	log := r.log(1)
	contains(t, "held", log, "PASS  one", `PAUSE  by `, `(next: "two"; checking the plan)`)
	if strings.Contains(log, "PASS  two") {
		t.Fatal("step two ran while paused")
	}
	contains(t, "resume", r.mustSwim("resume", "1"), "swim 1: resumed", `next step "two"`)
	if out, code := wait(); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	log = r.log(1)
	contains(t, "resumed", log, "RESUME  by ", "PASS  two", "PASS  three", "pass=3 fail=0 skip=0")
	contains(t, ".swim.log", r.mustSwim("log"), "pause ", "unpause ")
	if l := laneOf(r, 1); l.PausedAt != "" || l.PausedS <= 0 {
		t.Errorf("after: paused_at %q paused_s %v", l.PausedAt, l.PausedS)
	}
	contains(t, "verify", r.mustSwim("verify"), "consistent  yes")
	// The chain's round entry and the timeline carry the paused time.
	links, _ := chain.Read(r.root)
	var paused float64
	for _, l := range links {
		if l.Type == "round" && l.Lane == 1 {
			paused = l.PausedS
		}
	}
	if paused <= 0 {
		t.Error("the round's chain entry has no paused_s")
	}
	contains(t, "timeline", r.mustSwim("timeline"), "paused ", "╌ paused")
	contains(t, "timeline --yaml", r.mustSwim("timeline", "--yaml"), "pauses:", "duration_s:")
	contains(t, "not running", must(r.swim("pause", "1")), "swim 1 isn't running")
	contains(t, "not paused", r.mustSwim("unpause", "1"), "swim 1 isn't paused")
}

// swim interrupt on a paused round ends it as interrupted, its handlers
// run; nothing after the pause runs.
func TestPauseThenInterrupt(t *testing.T) {
	r := newRepo(t, "")
	r.write("lane.1.sh", `#!/usr/bin/env bash
# swim: syntax 3
# Round: paused, then ended
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 1
on_failure() { run "rollback" true; }
run "one" sleep 0.5
run "two" true
summary
`, 0o755)
	wait := startRun(t, r, "run", "1", "--plain")
	waitLong(t, func() bool { return laneOf(r, 1).CurrentStep == "one" })
	r.mustSwim("pause", "1", "--wait")
	r.mustSwim("interrupt", "1")
	out, code := wait()
	if code == 0 {
		t.Fatalf("exit 0 after an interrupt:\n%s", out)
	}
	log := r.log(1)
	contains(t, "log", log, "PAUSE  by ", "PASS  rollback", "END INTERRUPTED")
	if strings.Contains(log, "PASS  two") || strings.Contains(log, "RESUME") {
		t.Errorf("the paused round went on:\n%s", log)
	}
}

// swim run on a paused lane: on the same job it says unpause or
// --replace; on a new job, without a terminal, it refuses unless
// --replace, which ends the paused round (interrupted) and runs the new.
func TestPauseReplace(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "job A", `run "a1" sleep 0.5
run "a2" true`)
	withHeader(t, r, 1, "# Job: aaaaaaaa-0000-4000-8000-00000000000a")
	wait := startRun(t, r, "run", "1", "--plain")
	waitLong(t, func() bool { return laneOf(r, 1).CurrentStep == "a1" })
	r.mustSwim("pause", "1", "--wait")
	if out, code := r.swim("run", "1", "--plain"); code == 0 || !strings.Contains(out, "paused on this job: swim unpause 1 resumes it") {
		t.Errorf("same job: exit %d\n%s", code, out)
	}
	r.script(1, "job B", `run "b1" true`)
	withHeader(t, r, 1, "# Job: bbbbbbbb-0000-4000-8000-00000000000b")
	if out, code := r.swim("run", "1", "--plain"); code == 0 || !strings.Contains(out, "--replace ends it") {
		t.Errorf("no terminal, no --replace: exit %d\n%s", code, out)
	}
	out := r.mustSwim("run", "1", "--replace", "--plain")
	contains(t, "replace", out, "swim 1: interrupted job aaaaaaaa (paused); starting bbbbbbbb")
	if _, code := wait(); code == 0 {
		t.Error("the replaced run exited 0")
	}
	all := r.mustSwim("log", "1", "--all")
	contains(t, "both rounds", all, "job A", "END INTERRUPTED", "job B", "PASS  b1")
	links, _ := chain.Read(r.root)
	replaced := ""
	for _, l := range links {
		if l.Type == "round" && strings.HasPrefix(l.Job, "aaaaaaaa") {
			replaced = l.ReplacedBy
		}
	}
	if !strings.HasPrefix(replaced, "bbbbbbbb") {
		t.Errorf("job A's round entry: replaced_by %q", replaced)
	}
	if strings.Contains(all, "PASS  a2") {
		t.Error("job A went on after it was replaced")
	}
}

// A lane paused while it waits in a run doesn't start until unpaused; the
// round's Timeout: doesn't count paused time.
func TestPauseBeforeStartAndTimeout(t *testing.T) {
	r := newSerialRepo(t, "") // a 3s round Timeout: alone, not beside the parallel tests
	r.script(1, "first", `run "first" sleep 0.5`)
	r.script(2, "second", `run "s1" sleep 1
run "s2" true`)
	withHeader(t, r, 2, "# After: 1")
	withHeader(t, r, 2, "# Timeout: 3s")
	wait := startRun(t, r, "run", "1", "2", "--plain")
	waitLong(t, func() bool { return laneOf(r, 2).State == "waiting" })
	contains(t, "pause waiting", r.mustSwim("pause", "2"), "swim 2: paused before it starts")
	waitLong(t, func() bool { return laneOf(r, 1).State == "passed" })
	time.Sleep(600 * time.Millisecond)
	if r.log(2) != "" {
		t.Fatal("swim 2 started while paused")
	}
	r.mustSwim("unpause", "2")
	// Paused again mid-round for longer than its Timeout: it still passes.
	waitLong(t, func() bool { return laneOf(r, 2).CurrentStep == "s1" })
	r.mustSwim("pause", "2", "--wait")
	time.Sleep(3500 * time.Millisecond)
	r.mustSwim("unpause", "2")
	if out, code := wait(); code != 0 {
		t.Fatalf("exit %d:\n%s\n%s", code, out, r.log(2))
	}
	contains(t, "swim 2", r.log(2), "PASS  s2", "pass=2 fail=0")
}

// swim pause --all: workers claim no batch until swim unpause --all.
func TestPauseAllHoldsWorkers(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "queued", `run "q" true`)
	contains(t, "pause --all", r.mustSwim("pause", "--all"), "paused the repo")
	r.mustSwim("queue", "add", "1")
	c := r.cmd(bin, "watch", "--no-tui", "--interval", "100ms")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Process.Kill(); c.Wait() })
	time.Sleep(1500 * time.Millisecond)
	if len(doneEntries(t, r)) != 0 || r.log(1) != "" {
		t.Fatal("a worker claimed a batch while the repo was paused")
	}
	contains(t, "unpause --all", r.mustSwim("unpause", "--all"), "unpaused the repo")
	waitLong(t, func() bool { return len(doneEntries(t, r)) == 1 })
	if e := doneEntries(t, r); e[0].Result.Result != "passed" {
		t.Fatalf("after unpause --all: %+v", e[0].Result)
	}
	contains(t, ".swim.log", r.mustSwim("log"), "pause-all", "unpause-all")
}

// An SDK lane holds at its next in-process step (begin_step) as a bash
// lane holds at its next run.
func TestPauseSDKLane(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	r := newRepo(t, "")
	r.env = append(r.env, "PYTHONPATH="+filepath.Join(repoRoot(), "sdk", "python"))
	r.write("lane.1.py", `#!/usr/bin/env python3
# Round: sdk pause
import swim
with swim.round() as r:
    r.run("one", ["sleep", "0.5"])
    with r.step("two") as s:
        s.log("in process")
    r.run("three", ["true"])
`, 0o755)
	wait := startRun(t, r, "run", "1", "--plain")
	waitLong(t, func() bool { return laneOf(r, 1).CurrentStep == "one" })
	r.mustSwim("pause", "1", "--wait")
	if l := laneOf(r, 1); l.NextStep != "two" {
		t.Errorf("held before %q, want the in-process step two", l.NextStep)
	}
	time.Sleep(500 * time.Millisecond)
	if strings.Contains(r.log(1), "two") && !strings.Contains(r.log(1), `next: "two"`) {
		t.Fatal("the in-process step ran while paused")
	}
	r.mustSwim("unpause", "1")
	if out, code := wait(); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, "log", r.log(1), "PAUSE  by ", "RESUME  by ", "PASS  two", "PASS  three")
}

// An SDK lane's in-process step doesn't start past its round's Timeout:
// the round ends as a timeout, as swim step ends a bash lane's.
func TestSDKStepHonoursTimeout(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	// Python's start-up and the SDK's hello come out of the round's
	// Timeout: 3s, and alone, leaves step one room on a loaded machine.
	r := newSerialRepo(t, "")
	r.env = append(r.env, "PYTHONPATH="+filepath.Join(repoRoot(), "sdk", "python"))
	r.write("lane.1.py", `#!/usr/bin/env python3
# Round: sdk timeout
# Timeout: 3s
import swim, time
with swim.round() as r:
    r.run("one", ["true"])
    time.sleep(3.5)  # the program's own work: not a step
    with r.step("two") as s:
        s.log("never")
`, 0o755)
	r.swim("run", "1", "--plain")
	log := r.log(1)
	contains(t, "log", log, "PASS  one", "STOP  timeout: the round's Timeout (3s) ran out (before two)")
	if strings.Contains(log, "PASS  two") {
		t.Errorf("an in-process step started past the round's Timeout:\n%s", log)
	}
}
