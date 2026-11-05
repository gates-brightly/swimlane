package e2e

// A hostile lane under swim run (dev/tasks.md T3, T10): it runs as the
// operator, so it can write .swim/, call swim's hidden commands and set
// guard flags in its own environment. What swim run can
// refuse it refuses; what it can't, swim verify fails.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/chain"
)

// hostile writes lane 1 of the boundary fixture with prelude before its
// guarded command.
func (b *boundary) hostile(prelude string) {
	b.t.Helper()
	b.script(1, "drop orders", prelude+`
if guard `+boundaryGuard+` "drop table orders"; then step "drop" touch "`+b.marker+`"; fi
step "after" true`)
	withHeader(b.t, b.repo, 1, "# Guards:  "+boundaryGuard+" drop table orders")
}

// approvesOf are the approve entries for lane n.
func (b *boundary) approvesOf(n int) []chain.Link {
	var out []chain.Link
	for _, l := range b.links() {
		if l.Type == "approve" && l.Lane == n {
			out = append(out, l)
		}
	}
	return out
}

// A lane that calls _mark APPROVED itself is refused under swim run. One
// that hides from swim run (unsets SWIM_LAUNCHED) can mark itself
// approved: it runs as you, so it can do what you can. swim verify is
// what catches it: the round claims an approval no one signed.
func TestHostileLaneMarksApproved(t *testing.T) {
	b := newBoundaryRepo(t)
	b.hostile(`"$SWIM_BIN" _mark 1 APPROVED "drop table orders" "` + boundaryGuard + `=1" || echo "mark refused: $?"`)
	out := b.mustDryRun("run", "1", "--plain")
	contains(t, "output", out, "a lane can't approve itself", "mark refused: 2")
	if strings.Contains(b.log(1), "APPROVED") || len(b.approvesOf(1)) != 0 {
		t.Fatalf("the lane approved itself:\n%s", b.log(1))
	}
	b.noMarker(t, "_mark APPROVED")
	contains(t, "verify", b.mustSwim("verify"), "VERIFIED")

	b.hostile(`SWIM_LAUNCHED= SWIM_APPROVAL= "$SWIM_BIN" _mark 1 APPROVED "drop table orders" "` + boundaryGuard + `=1" || echo "mark refused: $?"`)
	b.mustDryRun("run", "1", "--plain")
	out, code := b.swim("verify")
	if code == 0 {
		t.Fatalf("verify passed a round that marked itself approved:\n%s\n%s", out, b.log(1))
	}
	contains(t, "verify", out, "that swim run didn't record", "NOT VERIFIED")
}

// T28: a lane that drops its approvals channel from its environment
// (SWIM_APPROVAL) and sets the flag gets a dry run, saying why. One that
// drops SWIM_LAUNCHED too looks like a lane run with bash, where the flag
// approves: swim can't stop a lane that runs as you, but swim verify
// fails the run, whose attestation granted no approval.
func TestHostileLaneDropsApprovals(t *testing.T) {
	b := newBoundaryRepo(t)
	b.hostile("unset SWIM_APPROVAL\nexport " + boundaryGuard + "=1")
	b.mustDryRun("run", "1", "--plain")
	b.noMarker(t, "unset SWIM_APPROVAL")
	contains(t, "unset SWIM_APPROVAL", b.log(1), "WARN  approval refused: "+boundaryGuard, "SKIP  drop table orders (dry run: this lane runs under swim run, but its approvals channel is gone")
	if len(b.approvesOf(1)) != 0 {
		t.Fatalf("approve entries %+v", b.approvesOf(1))
	}
	contains(t, "verify", b.mustSwim("verify"), "VERIFIED")

	b.hostile("unset SWIM_APPROVAL SWIM_LAUNCHED\nexport " + boundaryGuard + "=1")
	b.mustSwim("run", "1", "--plain")
	out, code := b.swim("verify")
	if code == 0 {
		t.Fatalf("verify passed a run whose lane approved itself by flag:\n%s\n%s", out, b.log(1))
	}
	contains(t, "verify", out, "that swim run didn't record", "NOT VERIFIED")

	b.hostile("")
	b.env = append(b.env, boundaryGuard+"=1")
	b.runLane(1, nil)
	if !b.dropped() {
		t.Fatalf("FLAG=1 bash lane.1.sh didn't approve:\n%s", b.log(1))
	}
}

// A swim run in another repo above a lane doesn't make it this repo's run:
// this repo's tests, run through swim ci, start lanes directly with their
// own flag, and that approves as it would from a shell (T28 refused it).
func TestOtherRepoRunDoesNotCount(t *testing.T) {
	b := newBoundaryRepo(t)
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	inner := "#!/usr/bin/env bash\ncd " + q(b.root) + " && exec env -i"
	for _, kv := range b.env {
		inner += " " + q(kv)
	}
	inner += " " + boundaryGuard + "=1 bash lane.1.sh\n"
	outer := newRepo(t)
	outer.write("inner.sh", inner, 0o755)
	outer.script(1, "test another repo", `step "inner lane" ./inner.sh`)
	withHeader(t, outer, 1, "# Includes: inner.sh")
	outer.mustSwim("run", "1", "--plain")
	if !b.dropped() {
		t.Fatalf("FLAG=1 bash lane.1.sh under another repo's swim run didn't approve:\n%s\nouter:\n%s", b.log(1), outer.log(1))
	}
	contains(t, "inner log", b.log(1), "APPROVED  drop table orders ("+boundaryGuard+"=1)")
}

// T28: when swim run can't start its approvals (a TMPDIR too long for a
// unix socket), every guard is a dry run, the lane's own flag included,
// and the run's attestation says why; verify holds.
func TestApprovalsUnavailable(t *testing.T) {
	b := newBoundaryRepo(t)
	long := filepath.Join(t.TempDir(), strings.Repeat("x", 110))
	if err := os.MkdirAll(long, 0o755); err != nil {
		t.Skip(err)
	}
	b.hostile("export " + boundaryGuard + "=1")
	b.env = append(b.env, "TMPDIR="+long)
	out := b.mustDryRun("run", "1", "--plain")
	contains(t, "output", out, "every guard runs as a dry run")
	b.noMarker(t, "the lane's flag with no approvals channel")
	contains(t, "lane1.log", b.log(1), "SKIP  drop table orders (dry run: this lane runs under swim run, but its approvals channel is gone")
	if len(b.approvesOf(1)) != 0 {
		t.Fatalf("approve entries: %+v", b.approvesOf(1))
	}
	var att *chain.Link
	for _, l := range b.links() {
		if l.Type == "run" {
			l := l
			att = &l
		}
	}
	if att == nil || att.AuthorityUnavailable == "" {
		t.Fatalf("attestation: %+v", att)
	}
	contains(t, "verify", b.mustSwim("verify", "--allow-unsigned"), "VERIFIED")
}

// FLAG=1 set inside the lane doesn't approve; set in the environment swim
// run started with, it does.
func TestHostileLaneSetsFlag(t *testing.T) {
	b := newBoundaryRepo(t)
	b.hostile(`export ` + boundaryGuard + `=1`)
	out := b.mustDryRun("run", "1", "--plain")
	b.noMarker(t, "FLAG=1 inside the lane")
	contains(t, "lane1.log", b.log(1), "SKIP  drop table orders (dry run: "+boundaryGuard+"=1 was set inside the lane, not in the environment swim run started with")
	contains(t, "output", out, "approval refused for lane 1: "+boundaryGuard+"=1 was set inside the lane")
	contains(t, ".swim.log", b.mustSwim("log"), "refused: "+boundaryGuard+"=1 was set inside the lane")

	// The operator's own flag approves, recorded by swim run.
	b.hostile("")
	b.env = append(b.env, boundaryGuard+"=1")
	b.mustSwim("run", "1", "--plain")
	if !b.dropped() {
		t.Fatalf("the operator's flag didn't approve:\n%s", b.log(1))
	}
	contains(t, "lane1.log", b.log(1), "APPROVED  drop table orders ("+boundaryGuard+"=1)")
	apps := b.approvesOf(1)
	if len(apps) != 1 || apps[0].Method != "flag" || apps[0].Manifest == "" {
		t.Fatalf("approve entries: %+v", apps)
	}
	var att *chain.Link
	for _, l := range b.links() {
		if l.Type == "run" && l.Run == apps[0].Run {
			l := l
			att = &l
		}
	}
	if att == nil || len(att.Approvals) != 1 || att.Approvals[0] != apps[0].Hash || len(att.Flags) != 1 || att.Flags[0] != boundaryGuard ||
		att.Manifests["1"] != apps[0].Manifest {
		t.Fatalf("run attestation: %+v", att)
	}
	contains(t, "verify", b.mustSwim("verify", "--allow-unsigned"), "VERIFIED")
}

// A lane that claims another job: the chain names the job swim run knows.
func TestHostileLaneWrongJob(t *testing.T) {
	b := newBoundaryRepo(t)
	b.hostile(`SWIM_ROUND_ID=not-my-job; export SWIM_ROUND_ID`)
	if out, code := b.swimEnv([]string{boundaryGuard + "=1"}, "run", "1", "--plain"); code != 0 || !b.dropped() {
		t.Fatalf("the flag didn't approve: %d\n%s\n%s", code, out, b.log(1))
	}
	apps := b.approvesOf(1)
	var round chain.Link
	for _, l := range b.links() {
		if l.Type == "round" && l.Lane == 1 {
			round = l
		}
	}
	if len(apps) != 1 || apps[0].Job == "not-my-job" || apps[0].Job != round.Job || apps[0].Method != "flag" {
		t.Fatalf("approve entry %+v for round job %s", apps, round.Job)
	}
}

// T10: lane 1 can't mark or finish lane 2's round, with its own token or
// with lane 2's read from /proc (the request comes from outside lane 2's
// process tree).
func TestLaneCantReportForAnother(t *testing.T) {
	r := newRepo(t)
	// The lanes signal each other through FIFOs: a write blocks until the
	// other side reads.
	r.script(2, "victim", `echo $$ > "$SWIM_ROOT/lane2.pid"
f="$SWIM_ROOT/lane2.ready"; [ -p "$f" ] || mkfifo "$f" 2>/dev/null; echo ready > "$f"
d="$SWIM_ROOT/lane1.done"; [ -p "$d" ] || mkfifo "$d" 2>/dev/null; cat "$d" >/dev/null
step "work" true`)
	steal := ""
	if runtime.GOOS == "linux" {
		steal = `
tok=$(tr '\0' '\n' < /proc/$(cat "$SWIM_ROOT/lane2.pid")/environ | sed -n 's/^SWIM_APPROVAL_TOKEN=//p')
[ -n "$tok" ] && echo "stole a token"
SWIM_APPROVAL_TOKEN=$tok "$SWIM_BIN" _mark 2 FAIL "forged with a stolen token" || echo "stolen mark refused: $?"`
	}
	r.script(1, "attacker", `f="$SWIM_ROOT/lane2.ready"; [ -p "$f" ] || mkfifo "$f" 2>/dev/null; cat "$f" >/dev/null
"$SWIM_BIN" _mark 2 FAIL "forged failure" || echo "mark refused: $?"
"$SWIM_BIN" _stage 2 change || echo "stage refused: $?"
"$SWIM_BIN" _finish 2 --exit 1 || echo "finish refused: $?"`+steal+`
d="$SWIM_ROOT/lane1.done"; [ -p "$d" ] || mkfifo "$d" 2>/dev/null; echo done > "$d"
step "own work" true`)
	out, code := r.swim("run", "1", "2", "--plain")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, "output", out, "refused: lane 1 can't report for lane 2", "mark refused: 2", "stage refused: 2", "finish refused: 2")
	if runtime.GOOS == "linux" {
		contains(t, "output", out, "stole a token", "outside lane 2's lane", "stolen mark refused: 2")
	}
	log2 := r.log(2)
	if strings.Contains(log2, "forged") || strings.Count(log2, "== END") != 1 || !strings.Contains(log2, "PASS  work") {
		t.Errorf("lane 1 reported for lane 2:\n%s", log2)
	}
	if st := r.status().Get(2); st == nil || st.State != "passed" {
		t.Errorf("lane 2's state: %+v", st)
	}
}

// A lane that throws junk at the broker: oversized, malformed and
// unfinished requests, a burst of requests with made-up tokens, and a
// request naming another lane. Each is declined or answered as the lane
// itself, and the run's own guard still works.
func TestBrokerHostileClients(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	r := newRepo(t)
	r.script(1, "hostile", `step "junk" python3 -c '
import json, os, socket, threading
path, tok = os.environ["SWIM_APPROVAL"], os.environ["SWIM_APPROVAL_TOKEN"]
def ask(payload):
    s = socket.socket(socket.AF_UNIX); s.connect(path)
    def send():
        try:
            s.sendall(payload)
        except OSError:
            pass  # the broker stopped reading: its answer says why
    threading.Thread(target=send, daemon=True).start()
    line = s.makefile().readline()
    s.close()
    return json.loads(line) if line else {}
big = json.dumps({"flag": "DROP", "desc": "x" * (2 << 20), "token": tok}).encode() + b"\n"
print("big:", ask(big).get("why"))
print("malformed:", ask(b"{not json\n").get("why"))
out = []
def bogus(i):
    out.append(ask(json.dumps({"flag": "DROP", "desc": "x", "token": "bogus%d" % i}).encode() + b"\n").get("approved", False))
ts = [threading.Thread(target=bogus, args=(i,)) for i in range(20)]
[t.start() for t in ts]; [t.join() for t in ts]
print("bogus approved:", any(out))
other = ask(json.dumps({"lane": 2, "flag": "DROP", "desc": "as lane 2", "token": tok}).encode() + b"\n")
print("as lane 2 answered for lane", other.get("lane"))
'
if guard DROP "drop the table"; then step "dropped" true; fi`)
	withHeader(t, r, 1, "# Guards:  DROP drop the table")
	out, code := r.swimEnv([]string{"DROP=1"}, "run", "1", "--plain")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, "output", out, "big: request too large", "malformed: malformed request", "bogus approved: False", "as lane 2 answered for lane 1")
	contains(t, "lane1.log", r.log(1), "APPROVED  drop the table (DROP=1", "PASS  dropped")
}
