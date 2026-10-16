package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A lane that exits before lane_init (a setup line that fails, a broken
// library line) reaches the record: its reason, with the last line it
// printed, in status, a round of its own in its log, and .swim.log. Read
// back with swim status, swim log and swim report, never the terminal.
func TestLaneThatNeverStartsIsRecorded(t *testing.T) {
	r := newRepo(t, "")
	r.write("lane.1.sh", `#!/usr/bin/env bash
# swim: syntax 2
# Round: never starts
echo "fetching setup"
echo "setup: cannot reach vault.internal" >&2
exit 3
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 1
summary
`, 0o755)
	if out, code := r.swim("run", "1", "--plain"); code == 0 {
		t.Fatalf("passed:\n%s", out)
	}
	const why = "not started: the lane script exited before lane_init (exit 3): setup: cannot reach vault.internal"
	if l := r.status().Get(1); l.State != "failed" || l.Reason != why {
		t.Errorf("status: %s, reason %q", l.State, l.Reason)
	}
	contains(t, "swim log 1", r.mustSwim("log", "1"), "== ROUND", "never starts", "STOP  "+why,
		"| fetching setup", "| setup: cannot reach vault.internal", "== END FAIL")
	contains(t, "swim status", r.mustSwim("status"), "not started")
	contains(t, "swim report", r.mustSwim("report"), why)
	contains(t, ".swim.log", r.mustSwim("log"), "not started: the lane script exited before lane_init")
}

// A lane swim refuses at lane_init (here: the repo's lock pins a script
// syntax this swim doesn't read, run directly with bash) says so in
// status and its log, with the fix.
func TestLockRefusalIsRecorded(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "locked out", `run "never" true`)
	r.mustSwim("run", "1", "--plain") // writes the lock
	lock := filepath.Join(r.root, ".swim.lock")
	data, err := os.ReadFile(lock)
	if err != nil {
		t.Fatal(err)
	}
	newer := regexp.MustCompile(`script: \d+`).ReplaceAll(data, []byte("script: 99"))
	os.WriteFile(lock, newer, 0o644)
	if out, err := r.cmd("bash", "lane.1.sh").CombinedOutput(); err == nil {
		t.Fatalf("ran with a newer lock:\n%s", out)
	}
	l := r.status().Get(1)
	if l.State != "failed" || !strings.HasPrefix(l.Reason, "not started: ") {
		t.Fatalf("status: %s, reason %q", l.State, l.Reason)
	}
	log := r.mustSwim("log", "1")
	contains(t, "swim log 1", log, "STOP  "+l.Reason, "Update swim, then run again")
	if strings.Count(log, "PASS  never") != 1 {
		t.Errorf("the refused round ran its step:\n%s", log)
	}
}

// A command that fails inside an outcome handler, outside its steps, is a
// WARN in the log: here ls, and the call to cleanup (which bash names by
// its last command, false). A failed step isn't one, and neither is the
// false inside cleanup a second time.
func TestHandlerFailureOutsideAStepWarns(t *testing.T) {
	r := newRepo(t, "")
	handlerScript(t, r, 1, "", `cleanup() { false; }
on_failure() {
  run "rollback" false
  cleanup
  ls /no-such-dir-for-swim
}`, `run "probe" false`)
	r.swim("run", "1", "--plain")
	log := r.log(1)
	contains(t, "log", log, "WARN  outside a step: ls /no-such-dir-for-swim", "WARN  outside a step: false", "FAIL  rollback")
	if n := strings.Count(log, "outside a step"); n != 2 {
		t.Errorf("%d outside-a-step WARNs, want 2 (the handler's own commands):\n%s", n, log)
	}
}
