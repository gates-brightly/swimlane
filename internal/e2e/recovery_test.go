package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/gates-brightly/swimlane/internal/chain"
)

// The scenarios in docs/recovery.md that are cheap to reproduce. Others are
// covered elsewhere: TestQueueYesOrphanRemove (an orphaned queue entry),
// TestVersionAndLock (a lock mismatch), TestSignedApprovalAndVerify (swim
// verify failing), TestInterruptRecordsAndStopsRound (an interrupted round).

// A lost runner key: entries signed with the old key still verify, because
// each entry carries its signer's public key. Dropping the old key from
// lookup.yml makes verify warn (and --strict fail), not break.
func TestRecoveryLostRunnerKey(t *testing.T) {
	r := newRepo(t, "")
	keys := people(t, r, "", "old", "new")
	lookup, _ := os.ReadFile(filepath.Join(r.root, "lookup.yml"))
	r.mustSwim("config", "signing", "auto")
	r.mustSwim("config", "signing_key", keys["old"])
	r.script(1, "one", `run "ok" true`)
	r.mustSwim("run", "1")

	// The old private key is gone; the runner signs with a new one.
	os.Remove(keys["old"])
	r.mustSwim("config", "signing_key", keys["new"])
	r.mustSwim("run", "1")
	links, err := chain.Read(r.root)
	if err != nil || len(links) != 2 || links[0].Signer == "" || links[0].Signer == links[1].Signer {
		t.Fatalf("want two rounds signed by different keys: %v %+v", err, links)
	}
	contains(t, "verify", r.mustSwim("verify"), "consistent  yes")

	// lookup.yml without the old key: a warning, and --strict fails.
	var kept []string
	for _, l := range strings.Split(string(lookup), "\n") {
		if !strings.HasPrefix(l, "  old:") && !strings.Contains(l, " old\"") {
			kept = append(kept, l)
		}
	}
	os.WriteFile(filepath.Join(r.root, "lookup.yml"), []byte(strings.Join(kept, "\n")), 0o644)
	contains(t, "verify, old key unlisted", r.mustSwim("verify"), "entries signed by a key not in lookup.yml", "consistent  yes")
	if out, code := r.swim("verify", "--strict"); code != 1 {
		t.Errorf("verify --strict with an unlisted signer: %d %s", code, out)
	}
}

// A lane whose process died (a closed terminal, a crash) leaves a stale
// pid file: swim doctor reports it, --fix removes it, and swim status
// --rebuild records the round as interrupted.
func TestRecoveryStaleRunningLane(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "killed", `run "long" sleep 30`)
	c := r.cmd("bash", "lane.1.sh")
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return r.status().Get(1).CurrentStep == "long" })
	syscall.Kill(-c.Process.Pid, syscall.SIGKILL) // no traps run
	c.Wait()

	contains(t, "status", r.mustSwim("status"), "running?")
	out, _ := r.swim("doctor")
	contains(t, "doctor", out, "[stale-pid]")
	r.swim("doctor", "--fix")
	if _, err := os.Stat(filepath.Join(r.root, ".swim", "lane1.pid")); err == nil {
		t.Error("swim doctor --fix left the stale pid file")
	}
	r.mustSwim("status", "--rebuild")
	if l := r.status().Get(1); l.State != "interrupted" {
		t.Errorf("after rebuild: state %q, want interrupted", l.State)
	}
	// The lane is free again.
	r.script(1, "rerun", `run "quick" true`)
	r.mustSwim("run", "1")
}
