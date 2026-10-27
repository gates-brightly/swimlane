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
	all, err := chain.Read(r.root)
	var links []chain.Link
	for _, l := range all {
		if l.Type == "round" {
			links = append(links, l)
		}
	}
	if err != nil || len(links) != 2 || links[0].SignedBy() == "" || links[0].SignedBy() == links[1].SignedBy() {
		t.Fatalf("want two rounds signed by different keys: %v %+v", err, links)
	}
	contains(t, "verify", r.mustSwim("verify", "--levels", "--allow-unsigned"), "consistent  yes")

	// lookup.yml without the old key: a warning, and --strict fails.
	var kept []string
	for _, l := range strings.Split(string(lookup), "\n") {
		if !strings.HasPrefix(l, "  old:") && !strings.Contains(l, " old\"") {
			kept = append(kept, l)
		}
	}
	os.WriteFile(filepath.Join(r.root, "lookup.yml"), []byte(strings.Join(kept, "\n")), 0o644)
	contains(t, "verify, old key unlisted", r.mustSwim("verify", "--levels", "--allow-unsigned"), "entries signed by a key not in lookup.yml", "consistent  yes")
	if out, code := r.swim("verify", "--levels", "--allow-unsigned", "--strict"); code != 1 {
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
	waitFor(t, func() bool { return laneOf(r, 1).CurrentStep == "long" })
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

// A lane killed outright (no traps) is ended by the next swim run or swim
// watch: its cut-off step and END INTERRUPTED go to the log, the chain
// records the round as interrupted, and the lane isn't left running.
func TestReapDeadLane(t *testing.T) {
	r := newRepo(t, "")
	kill := func() {
		r.script(1, "killed", `run "long" sleep 30`)
		c := r.cmd("bash", "lane.1.sh")
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool {
			l := r.status().Get(1)
			return l.State == "running" && l.PID == c.Process.Pid && l.CurrentStep == "long"
		})
		syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		c.Wait()
		if l := r.status().Get(1); l.State != "running" {
			t.Fatalf("killed lane: state %q, want running until reaped\n%s", l.State, r.log(1))
		}
	}

	kill()
	out := r.mustSwim("watch", "--once")
	contains(t, "watch", out, "swim 1: its process died without ending its round")
	if l := r.status().Get(1); l.State != "interrupted" || l.ExitCode == nil || *l.ExitCode != 137 {
		t.Fatalf("after watch: %+v", l)
	}
	contains(t, "log", r.log(1), "cut off: swim step was killed", "== END INTERRUPTED")
	links, _ := chain.Read(r.root)
	if last := links[len(links)-1]; last.Type != "round" || last.Result != "interrupted" || last.Exit == nil || *last.Exit != 137 {
		t.Errorf("chain's last entry: %+v", last.Entry)
	}

	// swim run does the same before it starts anything; the lane is free.
	kill()
	r.script(1, "rerun", `run "quick" true`)
	out = r.mustSwim("run", "1")
	contains(t, "run", out, "swim 1: its process died without ending its round")
	if strings.Count(r.log(1), "== END INTERRUPTED") != 2 {
		t.Errorf("want both killed rounds ended:\n%s", r.log(1))
	}
	if l := r.status().Get(1); l.State != "passed" {
		t.Errorf("rerun: state %q", l.State)
	}
	// Nothing left to reap: no message.
	if out := r.mustSwim("run", "1"); strings.Contains(out, "died") {
		t.Errorf("reaped a finished lane:\n%s", out)
	}
}
