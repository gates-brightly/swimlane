package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/launcher"
)

// broker is a scripted stand-in for swim run's approval broker.
type broker struct {
	*approval.Server
	mu    sync.Mutex
	asked []approval.Request
}

func newBroker(t *testing.T, answer func(approval.Request, <-chan struct{}) bool) *broker {
	t.Helper()
	b := &broker{}
	srv, err := approval.Listen(func(req approval.Request, cancel <-chan struct{}) approval.Response {
		b.mu.Lock()
		b.asked = append(b.asked, req)
		b.mu.Unlock()
		output.Notify() // a wait on b.requests() checks again
		return approval.Response{Approved: answer(req, cancel), By: "tester"}
	})
	if err != nil {
		t.Fatal(err)
	}
	b.Server = srv
	t.Cleanup(srv.Close)
	return b
}

func (b *broker) requests() []approval.Request {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]approval.Request(nil), b.asked...)
}

func TestGuardAsksForApproval(t *testing.T) {
	r := newRepo(t)
	r.script(1, "guarded", `if guard DROP_A "drop table a - 2026-10-12"; then run "drop a" true; fi
if guard DROP_B "drop table b - 2026-10-12"; then run "drop b" true; fi
if guard PRESET "preset flag"; then run "preset step" true; fi
if guard OFF "explicitly off"; then run "off step" true; fi`)
	b := newBroker(t, func(req approval.Request, _ <-chan struct{}) bool { return req.Flag == "DROP_A" })
	c := r.cmd("bash", "lane.1.sh")
	tok := b.Register(approval.Caller{Lane: 1, Guards: []string{"DROP_A", "DROP_B", "PRESET", "OFF"}})
	c.Env = append(c.Env, approval.EnvVar+"="+b.Path(), approval.TokenEnv+"="+tok, "PRESET=1", "OFF=0")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("lane: %v\n%s", err, out)
	}
	log := r.log(1)
	contains(t, "agent1.log", log,
		"  APPROVED  drop table a - 2026-10-12 (approved at the prompt by tester)",
		"  PASS  drop a",
		"  SKIP  drop table b - 2026-10-12 (dry run: declined at the prompt by tester; set DROP_B=1 to approve)",
		"  APPROVED  preset flag (PRESET=1)", "  PASS  preset step",
		"  SKIP  explicitly off (dry run: set OFF=1 to approve)")
	if strings.Contains(log, "PASS  drop b") || strings.Contains(log, "PASS  off step") {
		t.Errorf("a declined guard ran its step:\n%s", log)
	}
	// Only the unset flags were asked about, with the lane and description.
	asked := b.requests()
	if len(asked) != 2 || asked[0].Flag != "DROP_A" || asked[0].Lane != 1 || asked[0].Desc != "drop table a - 2026-10-12" || asked[1].Flag != "DROP_B" {
		t.Errorf("asked: %+v", asked)
	}
	hist, _ := os.ReadFile(filepath.Join(r.root, ".swim.log"))
	contains(t, ".swim.log", string(hist), "approve      swim 1", "drop table a - 2026-10-12  (DROP_A, by tester)",
		"decline      swim 1", "drop table b - 2026-10-12  (declined at the prompt by tester)")
	contains(t, "to-approve hint", string(out), "to approve: DROP_B=1 swim run 1")
}

func TestApprovalGivesUpWhenStopped(t *testing.T) {
	r := newRepo(t)
	r.script(1, "guarded", `if guard SLOW "slow approval"; then run "never" true; fi
run "after" true`)
	b := newBroker(t, func(_ approval.Request, cancel <-chan struct{}) bool { <-cancel; return false })
	c := r.cmd("bash", "lane.1.sh")
	c.Env = append(c.Env, approval.EnvVar+"="+b.Path(), approval.TokenEnv+"="+b.Register(approval.Caller{Lane: 1, Guards: []string{"SLOW"}}))
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(b.requests()) == 1 })
	if st := r.status().Get(1).CurrentStep; st != "awaiting approval: slow approval" {
		t.Errorf("status current_step while waiting: %q", st)
	}
	// A stop request (Ctrl-C under swim run, or swim interrupt) ends the wait.
	os.WriteFile(filepath.Join(r.root, ".swim", "lane1.stop"), nil, 0o644)
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case <-done:
	case <-time.After(waitDeadline):
		t.Fatal("the guard kept waiting after the stop request")
	}
	contains(t, "agent1.log", r.log(1), "SKIP  slow approval (dry run: not approved: the lane is stopping; set SLOW=1 to approve)")
	if strings.Contains(r.log(1), "PASS  never") {
		t.Error("the unapproved step ran")
	}
}

func TestNoPromptWithoutTerminal(t *testing.T) {
	r := newRepo(t)
	r.script(1, "guarded", `if guard FLAG_X "risky"; then run "risky" true; fi`)
	r.script(2, "other", `run "x" true`)
	// swim all with no terminal: no broker, lanes don't prompt on their own.
	out := r.mustDryRun("all", "--plain")
	contains(t, "agent1.log", r.log(1), "SKIP  risky (dry run: set FLAG_X=1 to approve)")
	if strings.Contains(out, "requires human approval") {
		t.Errorf("prompted without a terminal:\n%s", out)
	}
	// --no-prompt is the per-run "never ask"; approvals: is no setting.
	if _, code := r.swim("config", "approvals", "flags"); code == 0 {
		t.Error("approvals: is no longer a setting")
	}
}

// Approved up front by its flag (FLAG=1 swim run N): no one is asked, so
// it's loud where people look; FLAG=0 declines. -y is gone.
func TestFlagApproved(t *testing.T) {
	r := newRepo(t)
	r.script(1, "guarded", `if guard ALLOW_A "change a"; then run "a changed" true; fi
if guard ALLOW_OFF "explicitly off"; then run "off ran" true; fi`)
	r.script(2, "guarded too", `if guard ALLOW_B "change b"; then run "b changed" true; fi`)
	c := r.cmd(bin, "all", "--plain")
	c.Env = append(c.Env, "SWIM_USER=tester", "ALLOW_A=1", "ALLOW_B=1", "ALLOW_OFF=0")
	out, err := c.CombinedOutput()
	if c.ProcessState.ExitCode() != launcher.ExitDryRun { // ALLOW_OFF=0 dry-runs
		t.Fatalf("ALLOW_A=1 ALLOW_B=1 swim all: %v\n%s", err, out)
	}
	contains(t, "agent1.log", r.log(1), "PASS  a changed", "SKIP  explicitly off (dry run: set ALLOW_OFF=1 to approve)")
	if strings.Contains(r.log(1), "PASS  off ran") {
		t.Error("a guard whose flag was set to 0 was approved")
	}
	contains(t, "agent2.log", r.log(2), "PASS  b changed")
	if l := r.status().Get(1); strings.Join(l.FlagApproved, " ") != "ALLOW_A" {
		t.Errorf("status lane 1: flag_approved %v", l.FlagApproved)
	}
	contains(t, "status", r.mustSwim("status"), "approved without a prompt: ALLOW_A (flag set)", "approved without a prompt: ALLOW_B (flag set)")
	contains(t, "status --yaml", r.mustSwim("status", "2", "--yaml"), "flag_approved: [ALLOW_B]")
	for _, args := range [][]string{{"all", "-y"}, {"run", "1", "--yes"}, {"queue", "1", "-y"}, {"watch", "-y"}} {
		if out, code := r.swim(args...); code != 2 {
			t.Errorf("swim %v: -y is gone, want a usage error: %d\n%s", args, code, out)
		}
	}
}

// A guard whose approval socket can't be reached declines: the step is a
// dry run, never approved by default.
func TestGuardDeclinesOnSocketError(t *testing.T) {
	r := newRepo(t)
	r.script(1, "guarded", `if guard ALLOW_X "change x"; then run "x changed" true; fi`)
	c := r.cmd("bash", "lane.1.sh")
	c.Env = append(c.Env, approval.EnvVar+"="+filepath.Join(t.TempDir(), "no-such-socket"))
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("lane: %v\n%s", err, out)
	}
	contains(t, "agent1.log", r.log(1), "SKIP  change x (dry run: not approved: approval: ")
	if strings.Contains(r.log(1), "PASS  x changed") {
		t.Error("the guarded step ran without an approval")
	}
}
