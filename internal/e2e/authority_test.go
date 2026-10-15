package e2e

// A hostile lane under swim run (tasks.md T3, T10): it runs as the
// operator, so it can write approvals/ and lookup.yml, call swim's hidden
// commands and set guard flags in its own environment. None of that may
// produce an approved guard, or a chain entry swim verify accepts.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/sigfile"
)

// hostile writes lane 1 of the boundary fixture with prelude before its
// guarded command.
func (b *boundary) hostile(prelude string) {
	b.t.Helper()
	b.script(1, "drop orders", prelude+`
if guard `+boundaryGuard+` "drop table orders"; then run "drop" touch "`+b.marker+`"; fi
run "after" true`)
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

// A lane that writes approvals/ or lookup.yml once the run has started:
// swim run decides from what it saw at the start, so neither counts.
func TestHostileLaneWritesApprovals(t *testing.T) {
	b := newBoundaryRepo(t)
	// The lane puts a valid approval of itself in place as it starts.
	saved := filepath.Join(t.TempDir(), "saved")
	os.Mkdir(saved, 0o755)
	b.hostile(`cp "` + saved + `"/*.signature approvals/`)
	b.approve(1)
	files, _ := filepath.Glob(filepath.Join(sigfile.Dir(b.root), "*.signature"))
	if len(files) != 1 {
		t.Fatalf("approval files: %v", files)
	}
	os.Rename(files[0], filepath.Join(saved, filepath.Base(files[0])))
	b.mustSwim("run", "1", "--plain")
	b.noMarker(t, "an approval the lane wrote")
	contains(t, "agent1.log", b.log(1), "SKIP  drop table orders (dry run: set "+boundaryGuard+"=1 to approve)")
	if len(b.approvesOf(1)) != 0 {
		t.Errorf("an approve entry for the lane's own approval: %+v", b.approvesOf(1))
	}
	// In place before the next run starts, the same file counts.
	b.mustSwim("run", "1", "--plain")
	if !b.dropped() {
		t.Fatalf("the approval in place at the start didn't count:\n%s", b.log(1))
	}
	os.Remove(b.marker)
	for _, f := range files {
		os.Remove(f)
	}

	// lookup.yml without the approver at the start; the lane restores it.
	lk, _ := os.ReadFile(filepath.Join(b.root, "lookup.yml"))
	fullLookup := filepath.Join(t.TempDir(), "lookup.yml")
	os.WriteFile(fullLookup, lk, 0o644)
	os.WriteFile(filepath.Join(b.root, "lookup.yml"), []byte("users: {}\n"), 0o644)
	b.hostile(`cp "` + fullLookup + `" lookup.yml`)
	b.approve(1)
	os.WriteFile(filepath.Join(b.root, "lookup.yml"), []byte("users: {}\n"), 0o644)
	b.mustSwim("run", "1", "--plain")
	b.noMarker(t, "a lookup.yml the lane wrote")

	// The same approval in place before the run starts does count.
	os.WriteFile(filepath.Join(b.root, "lookup.yml"), lk, 0o644)
	b.mustSwim("run", "1", "--plain")
	if !b.dropped() {
		t.Fatalf("a real approval didn't run it:\n%s", b.log(1))
	}
	contains(t, "verify", b.mustSwim("verify"), "consistent  yes")
}

// A lane that calls _mark APPROVED itself is refused under swim run, and
// so is one that hides from swim run (unsets SWIM_LAUNCHED): swim started
// it, whatever its environment says (T28).
func TestHostileLaneMarksApproved(t *testing.T) {
	b := newBoundaryRepo(t)
	b.hostile(`"$SWIM_BIN" _mark 1 APPROVED "drop table orders" "` + boundaryGuard + `=1" || echo "mark refused: $?"`)
	out := b.mustSwim("run", "1", "--plain")
	contains(t, "output", out, "a lane can't approve itself", "mark refused: 2")
	if strings.Contains(b.log(1), "APPROVED") || len(b.approvesOf(1)) != 0 {
		t.Fatalf("the lane approved itself:\n%s", b.log(1))
	}
	b.noMarker(t, "_mark APPROVED")

	b.hostile(`SWIM_LAUNCHED= SWIM_APPROVAL= "$SWIM_BIN" _mark 1 APPROVED "drop table orders" "` + boundaryGuard + `=1" || echo "mark refused: $?"`)
	out = b.mustSwim("run", "1", "--plain")
	contains(t, "hidden", out, "a lane can't approve itself", "mark refused: 2")
	b.noMarker(t, "_mark APPROVED outside swim run's view")
	if len(b.approvesOf(1)) != 0 {
		t.Fatalf("approve entries: %+v", b.approvesOf(1))
	}
	contains(t, "verify", b.mustSwim("verify"), "consistent  yes")
}

// T28: a lane that drops its approvals channel from its environment
// (SWIM_APPROVAL, or SWIM_LAUNCHED too) and sets the flag gets a dry run,
// saying why, and no approval reaches the chain. Run directly with bash,
// with no swim run, the flag still approves.
func TestHostileLaneDropsApprovals(t *testing.T) {
	b := newBoundaryRepo(t)
	for _, unset := range []string{"SWIM_APPROVAL", "SWIM_APPROVAL SWIM_LAUNCHED"} {
		b.hostile("unset " + unset + "\nexport " + boundaryGuard + "=1")
		b.mustSwim("run", "1", "--plain")
		b.noMarker(t, "unset "+unset)
		contains(t, "unset "+unset, b.log(1), "WARN  approval refused: "+boundaryGuard, "SKIP  drop table orders (dry run: this lane runs under swim run, but its approvals channel is gone")
		if len(b.approvesOf(1)) != 0 {
			t.Fatalf("unset %s: approve entries %+v", unset, b.approvesOf(1))
		}
	}
	contains(t, "verify", b.mustSwim("verify"), "consistent  yes")

	b.hostile("")
	b.env = append(b.env, boundaryGuard+"=1")
	b.runLane(1, nil)
	if !b.dropped() {
		t.Fatalf("FLAG=1 bash lane.1.sh didn't approve:\n%s", b.log(1))
	}
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
	out := b.mustSwim("run", "1", "--plain")
	contains(t, "output", out, "every guard runs as a dry run")
	b.noMarker(t, "the lane's flag with no approvals channel")
	contains(t, "agent1.log", b.log(1), "SKIP  drop table orders (dry run: this lane runs under swim run, but its approvals channel is gone")
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
	contains(t, "verify", b.mustSwim("verify"), "consistent  yes")
}

// FLAG=1 set inside the lane doesn't approve; set in the environment swim
// run started with, it does.
func TestHostileLaneSetsFlag(t *testing.T) {
	b := newBoundaryRepo(t)
	b.hostile(`export ` + boundaryGuard + `=1`)
	out := b.mustSwim("run", "1", "--plain")
	b.noMarker(t, "FLAG=1 inside the lane")
	contains(t, "agent1.log", b.log(1), "SKIP  drop table orders (dry run: "+boundaryGuard+"=1 was set inside the lane, not in the environment swim run started with")
	contains(t, "output", out, "approval refused for swim 1: "+boundaryGuard+"=1 was set inside the lane")
	contains(t, ".swim.log", b.mustSwim("log"), "refused: "+boundaryGuard+"=1 was set inside the lane")

	// The operator's own flag approves, recorded by swim run.
	b.hostile("")
	b.env = append(b.env, boundaryGuard+"=1")
	b.mustSwim("run", "1", "--plain")
	if !b.dropped() {
		t.Fatalf("the operator's flag didn't approve:\n%s", b.log(1))
	}
	contains(t, "agent1.log", b.log(1), "APPROVED  drop table orders ("+boundaryGuard+"=1)")
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
	contains(t, "verify", b.mustSwim("verify"), "consistent  yes")
}

// A lane that claims another job: the chain names the job swim run knows.
func TestHostileLaneWrongJob(t *testing.T) {
	b := newBoundaryRepo(t)
	b.hostile(`SWIM_JOB=not-my-job; export SWIM_JOB`)
	b.mustSwim("run", "1", "-y", "--plain")
	if !b.dropped() {
		t.Fatalf("-y didn't approve:\n%s", b.log(1))
	}
	apps := b.approvesOf(1)
	var round chain.Link
	for _, l := range b.links() {
		if l.Type == "round" && l.Lane == 1 {
			round = l
		}
	}
	if len(apps) != 1 || apps[0].Job == "not-my-job" || apps[0].Job != round.Job || apps[0].Method != "auto" {
		t.Fatalf("approve entry %+v for round job %s", apps, round.Job)
	}
}

// T10: lane 1 can't mark or finish lane 2's round, with its own token or
// with lane 2's read from /proc (the request comes from outside lane 2's
// process tree).
func TestLaneCantReportForAnother(t *testing.T) {
	r := newRepo(t, "")
	r.script(2, "victim", `echo $$ > lane2.pid
while [ ! -e lane1.done ]; do sleep 0.1; done
run "work" true`)
	steal := ""
	if runtime.GOOS == "linux" {
		steal = `
tok=$(tr '\0' '\n' < /proc/$(cat lane2.pid)/environ | sed -n 's/^SWIM_APPROVAL_TOKEN=//p')
[ -n "$tok" ] && echo "stole a token"
SWIM_APPROVAL_TOKEN=$tok "$SWIM_BIN" _mark 2 FAIL "forged with a stolen token" || echo "stolen mark refused: $?"`
	}
	r.script(1, "attacker", `while [ ! -e lane2.pid ]; do sleep 0.1; done
"$SWIM_BIN" _mark 2 FAIL "forged failure" || echo "mark refused: $?"
"$SWIM_BIN" _stage 2 change || echo "stage refused: $?"
"$SWIM_BIN" _finish 2 --exit 1 || echo "finish refused: $?"`+steal+`
touch lane1.done
run "own work" true`)
	out, code := r.swim("run", "1", "2", "--plain")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, "output", out, "refused: lane 1 can't report for lane 2", "mark refused: 2", "stage refused: 2", "finish refused: 2")
	if runtime.GOOS == "linux" {
		contains(t, "output", out, "stole a token", "outside swim 2's lane", "stolen mark refused: 2")
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
	r := newRepo(t, "")
	r.script(1, "hostile", `run "junk" python3 -c '
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
if guard DROP "drop the table"; then run "dropped" true; fi`)
	withHeader(t, r, 1, "# Guards:  DROP drop the table")
	out, code := r.swim("run", "1", "-y", "--plain")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, "output", out, "big: request too large", "malformed: malformed request", "bogus approved: False", "as lane 2 answered for lane 1")
	contains(t, "agent1.log", r.log(1), "APPROVED  drop the table (approved by swim run -y", "PASS  dropped")
}
