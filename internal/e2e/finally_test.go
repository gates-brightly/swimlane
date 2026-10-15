package e2e

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/chain"
)

// finallyLane writes lane n as a finally lane: it records its context (and
// when it ran) in finally.env, then passes or fails.
func finallyLane(t *testing.T, r *repo, n int, refs, result string) {
	t.Helper()
	r.script(n, "report the run", `run "context" bash -c 'env | grep "^SWIM_FINALLY" | sort > finally.env; date +%s.%N > finally.at'
run "result" `+result)
	withHeader(t, r, n, "# Finally: "+refs)
}

// A finally lane runs once the lanes it names have ended, however they
// ended: pulled in by naming them, never skipped for them, it sees each
// outcome; the ordinary lanes decide the run, and both failing exits 89.
func TestFinallyRun(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "one", `run "ok" true`)
	r.script(2, "two", `run "breaks" false`)
	r.script(3, "three", `run "never" true`)
	withHeader(t, r, 3, "# After: 2")
	finallyLane(t, r, 4, "1 2 3", "true")

	out, code := r.swim("run", "1", "2", "3", "--plain")
	if code != 1 {
		t.Fatalf("exit %d, want 1 (an ordinary lane failed):\n%s", code, out)
	}
	contains(t, "summary", out, "finally: 1 passed; the run exits 1")
	env := readFile(t, r, "finally.env")
	contains(t, "context", env, "SWIM_FINALLY=fail", "SWIM_FINALLY_1=pass", "SWIM_FINALLY_2=fail", "SWIM_FINALLY_3=skip", "SWIM_FINALLY_1_JOB=")
	if l := r.status().Get(4); l.State != "passed" || l.FinallySaw != "1 pass, 2 fail, 3 skip" {
		t.Errorf("status lane 4: %s %q", l.State, l.FinallySaw)
	}
	contains(t, "status", r.mustSwim("status"), "finally: 1 pass, 2 fail, 3 skip")
	contains(t, ".swim.log", r.mustSwim("log"), "finally_passed=1 finally_failed=0")
	found := false
	links, _ := chain.Read(r.root)
	for _, l := range links {
		if l.Type == "round" && l.Lane == 4 && l.Finally["2"] == "fail" && l.Finally["3"] == "skip" {
			found = true
		}
	}
	if !found {
		t.Error("lane 4's round entry doesn't record what it saw")
	}

	// The finally lane fails too: 89.
	finallyLane(t, r, 4, "1 2 3", "false")
	if out, code := r.swim("run", "1", "2", "3", "--plain"); code != 89 {
		t.Errorf("exit %d, want 89 (an ordinary and a finally lane failed):\n%s", code, out)
	}
	// --no-finally leaves it out; run alone, it starts at once.
	os.Remove(filepath.Join(r.root, "finally.env"))
	r.swim("run", "1", "--no-finally", "--plain")
	if readFile(t, r, "finally.env") != "" {
		t.Error("--no-finally ran the finally lane")
	}
	finallyLane(t, r, 4, "1 2 3", "true")
	r.mustSwim("run", "4", "--plain")
	contains(t, "alone", readFile(t, r, "finally.env"), "SWIM_FINALLY_1=\n", "SWIM_FINALLY=fail")

	// swim plan marks it; a queued batch takes it along.
	contains(t, "plan", r.mustSwim("plan", "1"), "[F] finally, once swim 1 2 3 end (however they end)")
	contains(t, "queue", r.mustSwim("queue", "add", "1"), "with the finally lanes naming them: swim 4")
}

// The first Ctrl-C doesn't keep a finally lane from starting: it runs once
// the stopped lane has ended, and sees it interrupted.
func TestFinallyAfterCtrlC(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "long", `run "a" sleep 1
run "b" true`)
	finallyLane(t, r, 2, "1", "true")
	c := r.cmd(bin, "run", "1", "--plain")
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer c.Process.Kill()
	waitFor(t, func() bool { return r.status().Get(1).CurrentStep == "a" })
	time.Sleep(150 * time.Millisecond)
	syscall.Kill(-c.Process.Pid, syscall.SIGINT)
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("run didn't end:\n%s", out.String())
	}
	contains(t, "finally ran", readFile(t, r, "finally.env"), "SWIM_FINALLY_1=interrupted")
	if l := r.status().Get(2); l.State != "passed" {
		t.Errorf("finally lane after Ctrl-C: %s (%s)\n%s", l.State, l.Reason, out.String())
	}
}

// A finally job runs after an earlier job failed, and sees how each ended;
// the lane still fails.
func TestFinallyJob(t *testing.T) {
	r := newRepo(t, "")
	r.write("lanes.stack.sh", `#!/usr/bin/env bash
# swim: syntax 2
# Round: test stack

# swim job 1.1 -----
# Round: provision
run "provision" true

# swim job 1.2 -----
# Round: test
run "test" false

# swim job 1.3 -----
# Round: more tests
run "more" true

# swim job 1.9 finally -----
# Round: tear down
run "teardown" bash -c 'env | grep "^SWIM_FINALLY" | sort > teardown.env'
`, 0o644)
	out, code := r.swim("run", "1", "--plain")
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	env := readFile(t, r, "teardown.env")
	contains(t, "teardown", env, "SWIM_FINALLY_1_1=pass", "SWIM_FINALLY_1_2=fail", "SWIM_FINALLY_1_3=skip", "SWIM_FINALLY=fail")
	if l := r.status().Get(1); l.State != "failed" || strings.Contains(strings.Join(l.FailedSteps, " "), "1.9") {
		t.Errorf("lane: %s %v", l.State, l.FailedSteps)
	}
}
