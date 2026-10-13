package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/chain"
)

// people makes an ssh key per name and a lookup.yml listing them, with
// extra appended (groups, require).
func people(t *testing.T, r *repo, extra string, names ...string) map[string]string {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not installed")
	}
	dir := t.TempDir()
	keys := map[string]string{}
	var b strings.Builder
	b.WriteString("users:\n")
	for _, n := range names {
		p := filepath.Join(dir, n)
		if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", n, "-f", p).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		pub, _ := os.ReadFile(p + ".pub")
		keys[n] = p
		fmt.Fprintf(&b, "  %s:\n    keys: [%q]\n", n, strings.TrimSpace(string(pub)))
	}
	b.WriteString(extra)
	os.WriteFile(filepath.Join(r.root, "lookup.yml"), []byte(b.String()), 0o644)
	return keys
}

func dropLane(t *testing.T, r *repo) {
	r.script(1, "drop old table", `if guard FIN_ALLOW_DROP "drop table old_orders"; then run "drop" true; fi
run "after" true`)
	withHeader(t, r, 1, "# Guards:  FIN_ALLOW_DROP drop table old_orders")
}

func TestSignedApprovalAndVerify(t *testing.T) {
	r := newRepo(t, "")
	keys := people(t, r, "groups:\n  platform-admin: [zach, alice]\nrequire:\n  - guards: [FIN_ALLOW_*]\n    group: platform-admin\n    count: 2\n", "zach", "alice")
	keys["mallory"] = filepath.Join(t.TempDir(), "mallory") // a key lookup.yml doesn't list
	exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "mallory", "-f", keys["mallory"]).Run()
	r.mustSwim("config", "signing", "auto")
	r.mustSwim("config", "signing_key", keys["zach"]) // the runner
	dropLane(t, r)

	if out, code := r.swim("approve", "1", "--guards", "FIN_ALLOW_TYPO", "--key", keys["zach"]); code == 0 || !strings.Contains(out, "has no guard FIN_ALLOW_TYPO") {
		t.Fatalf("typo guard: %d %s", code, out)
	}
	contains(t, "first approval", r.mustSwim("approve", "1", "--guards", "FIN_ALLOW_DROP", "--key", keys["zach"]),
		"approved lane.1.sh", "as zach", "not yet:  FIN_ALLOW_DROP: approval needs 2 signature(s) from platform-admin, has 1")
	r.mustSwim("run", "1")
	contains(t, "one of two", r.log(1), "SKIP  drop table old_orders (dry run: approval needs 2 signature(s) from platform-admin, has 1; set FIN_ALLOW_DROP=1 to approve)")

	// A valid signature from a key not in lookup.yml doesn't count.
	contains(t, "mallory", r.mustSwim("approve", "1", "--key", keys["mallory"]), "not in lookup.yml: it won't count")
	if out, code := r.swim("approve", "1", "--all", "--key", keys["alice"]); code == 0 || !strings.Contains(out, "approving again signs that statement as is") {
		t.Fatalf("co-approve changing the statement: %d %s", code, out)
	}
	contains(t, "second approval", r.mustSwim("approve", "1", "--key", keys["alice"]), "approved: FIN_ALLOW_DROP (alice, zach)")
	contains(t, "list", r.mustSwim("approve"), "swim 1 (lane.1.sh", "approves FIN_ALLOW_DROP", "approved by zach, unknown", "alice")

	// Alice revokes before it runs: not enough any more.
	contains(t, "revoke", r.mustSwim("revoke", "1", "--key", keys["alice"]), "revoked your approval")
	r.mustSwim("run", "1")
	if strings.Contains(r.log(1), "APPROVED") {
		t.Fatal("a revoked approval still counted")
	}
	if out, code := r.swim("revoke", "1", "--key", keys["alice"]); code == 0 || !strings.Contains(out, "no standing approval there to revoke") {
		t.Errorf("revoking twice: %d %s", code, out)
	}
	r.mustSwim("approve", "1", "--key", keys["alice"]) // and approves again
	r.mustSwim("run", "1")
	contains(t, "approved", r.log(1), "APPROVED  drop table old_orders (approved by ", "zach (platform-admin)", "alice (platform-admin)", "(signed)", "PASS  drop")

	hist := r.mustSwim("history", "1")
	contains(t, "history", hist, "approved  swim 1 script", "revoked  swim 1 script", "approve  swim 1  FIN_ALLOW_DROP", "via signature", "round  swim 1  passed exit 0", "signed by zach (platform-admin)")
	contains(t, "verify", r.mustSwim("verify"), "verified: every link, signature, log and script checks out", "script, approves FIN_ALLOW_DROP; 3 standing of 5 events")

	// Tampering is provable.
	logPath := filepath.Join(r.root, ".swim", "logs", "agent1.log")
	orig, _ := os.ReadFile(logPath)
	os.WriteFile(logPath, []byte(strings.Replace(string(orig), "PASS  drop", "PASS  dorp", 1)), 0o644)
	if out, code := r.swim("verify"); code != 1 || !strings.Contains(out, "log for run") || !strings.Contains(out, "was changed after the round ended") {
		t.Errorf("edited log: %d %s", code, out)
	}
	os.WriteFile(logPath, orig, 0o644)
	cp := chain.Path(r.root)
	corig, _ := os.ReadFile(cp)
	os.WriteFile(cp, []byte(strings.Replace(string(corig), `"FIN_ALLOW_DROP"`, `"FIN_ALLOW_OTHER"`, 1)), 0o644)
	if out, code := r.swim("verify"); code != 1 || !strings.Contains(out, "signature doesn't match the entry") || !strings.Contains(out, "prev hash doesn't match") {
		t.Errorf("edited chain: %d %s", code, out)
	}
	os.WriteFile(cp, corig, 0o644)
	r.mustSwim("verify")

	// Changing the script makes the signature stale: the guard says so.
	r.script(1, "drop old table v2", `if guard FIN_ALLOW_DROP "drop table old_orders"; then run "drop" true; fi`)
	withHeader(t, r, 1, "# Guards:  FIN_ALLOW_DROP drop table old_orders")
	r.mustSwim("run", "1")
	contains(t, "stale", r.log(1), "approves another version of lane.1.sh")
	contains(t, "stale verify", r.mustSwim("verify"), "stale: lane.1.sh changed since")
}

func TestChainRecordsEveryApproval(t *testing.T) {
	r := newRepo(t, "")
	keys := people(t, r, "", "zach")
	r.mustSwim("config", "signing", "auto")
	r.mustSwim("config", "signing_key", keys["zach"])
	r.script(1, "flagged", `if guard ALLOW_A "change a"; then run "a" true; fi`)
	c := r.cmd(bin, "run", "1")
	c.Env = append(c.Env, "ALLOW_A=1")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	r.script(1, "auto", `if guard ALLOW_B "change b"; then run "b" true; fi`)
	r.mustSwim("run", "1", "-y")
	links, err := chain.Read(r.root)
	if err != nil {
		t.Fatal(err)
	}
	var methods []string
	for _, l := range links {
		if l.Type == "approve" {
			methods = append(methods, l.Method+":"+l.Guard)
			if l.Sig == "" || l.Approvers[0].Key == "" {
				t.Errorf("approve entry not signed by the runner: %+v", l.Entry)
			}
		}
	}
	if strings.Join(methods, " ") != "flag:ALLOW_A auto:ALLOW_B" {
		t.Errorf("approve methods: %v", methods)
	}
	if !strings.Contains(r.log(1), "| sha256: ") {
		t.Errorf("the round header should name the script's hash")
	}
	// Each script that ran is kept, by hash.
	files, _ := filepath.Glob(filepath.Join(r.root, ".swim", "scripts", "*.sh"))
	if len(files) != 2 {
		t.Errorf("kept scripts: %v", files)
	}

	// signing: required without a key refuses to start a round.
	r.mustSwim("config", "signing", "required")
	r.mustSwim("config", "signing_key", filepath.Join(t.TempDir(), "missing"))
	if out, code := r.swim("run", "1"); code == 0 || !strings.Contains(out, "signing: required") {
		t.Errorf("required without a key: %d %s", code, out)
	}
	// signing: off records unsigned links; verify --strict flags them.
	r.mustSwim("config", "signing", "off")
	r.mustSwim("run", "1", "-y")
	if out, code := r.swim("verify", "--strict"); code != 1 || !strings.Contains(out, "unsigned entries") {
		t.Errorf("--strict with unsigned entries: %d %s", code, out)
	}
	r.mustSwim("verify")
}

func TestWatchSigned(t *testing.T) {
	r := newRepo(t, "")
	keys := people(t, r, "", "zach", "alice")
	r.mustSwim("config", "signing", "auto")
	r.mustSwim("config", "signing_key", keys["zach"])
	dropLane(t, r)
	// Nothing approved: nothing to do.
	contains(t, "nothing", r.mustSwim("watch", "--once", "--approved"), "swim watch")
	// Alice approves remotely (the approvals file arrives); the watch runs it.
	r.mustSwim("approve", "1", "--guards", "FIN_ALLOW_DROP", "--key", keys["alice"])
	out := r.mustSwim("watch", "--once", "--approved")
	contains(t, "watch", out, "queued swim 1", "approved by alice", "passed; consumed")
	contains(t, "agent1.log", r.log(1), "APPROVED  drop table old_orders (approved by alice (signed))", "PASS  drop")
	// Already ran: not queued again.
	if out := r.mustSwim("watch", "--once", "--approved"); strings.Contains(out, "queued swim 1") {
		t.Errorf("queued the same signed approval twice:\n%s", out)
	}
	contains(t, "keys", r.mustSwim("keys"), "alice", "zach", "this machine signs with")
}

func TestStandingApproval(t *testing.T) {
	r := newRepo(t, "")
	keys := people(t, r, "groups:\n  platform-admin: [zach, alice]\nrequire:\n  - guards: [FIN_ALLOW_*]\n    group: platform-admin\n    count: 2\n", "zach", "alice", "bob")
	r.mustSwim("config", "signing", "auto")
	r.mustSwim("config", "signing_key", keys["zach"])
	dropLane(t, r)
	if out, code := r.swim("approve", "--auto", "nobody", "--key", keys["zach"]); code == 0 || !strings.Contains(out, "no user nobody in lookup.yml") {
		t.Fatalf("standing for an unknown user: %d %s", code, out)
	}
	// Zach approves every job bob queues. One platform-admin isn't enough
	// for FIN_ALLOW_* on its own...
	contains(t, "standing", r.mustSwim("approve", "--auto", "bob", "--key", keys["zach"]), "approved every job bob queues (standing", "approves every guard")
	contains(t, "queue add", r.mustSwim("queue", "add", "1", "--key", keys["bob"]), "signed by bob")
	r.mustSwim("watch", "--once")
	contains(t, "one admin", r.log(1), "SKIP  drop table old_orders (dry run: approval needs 2 signature(s) from platform-admin, has 1")
	// ...but with alice's approval of this script it is: they count together.
	r.mustSwim("approve", "1", "--guards", "FIN_ALLOW_DROP", "--key", keys["alice"])
	r.mustSwim("queue", "add", "1", "--key", keys["bob"])
	r.mustSwim("watch", "--once")
	contains(t, "standing + script", r.log(1), "APPROVED  drop table old_orders (approved by ", "alice (platform-admin)", "zach (platform-admin)", "(signed, and standing for jobs queued by bob)")
	// The standing approval isn't consumed: it applies to the next batch too
	// (with a rule it meets alone).
	r.script(2, "small change", `if guard ALLOW_SMALL "small change"; then run "small" true; fi`)
	r.mustSwim("queue", "add", "2", "--key", keys["bob"])
	r.mustSwim("watch", "--once")
	contains(t, "standing alone", r.log(2), "APPROVED  small change (approved by zach (platform-admin) (standing approval for jobs queued by bob))")
	// A batch alice queues doesn't get bob's standing approval.
	r.script(2, "small change 2", `if guard ALLOW_SMALL "small change 2"; then run "small" true; fi`)
	r.mustSwim("queue", "add", "2", "--key", keys["alice"])
	r.mustSwim("watch", "--once")
	contains(t, "not bob", r.log(2), "SKIP  small change 2 (dry run: set ALLOW_SMALL=1 to approve)")
	// A forged run: point a lane at bob's signed entry from an earlier batch
	// (for another version of the script). The entry must hold this lane's
	// exact script, so the standing approval doesn't apply.
	done, _ := filepath.Glob(filepath.Join(r.root, ".swim", "queue", "done", "*.yml"))
	var bobs string
	for _, d := range done {
		if data, _ := os.ReadFile(d); strings.Contains(string(data), "by: bob") && strings.Contains(string(data), "lane: 2") {
			bobs = d
		}
	}
	forged := filepath.Join(r.root, ".swim", "queue", "forged.running")
	data, err := os.ReadFile(bobs)
	if err != nil {
		t.Fatalf("bob's earlier entry: %v", err)
	}
	os.WriteFile(forged, data, 0o644)
	c := r.cmd("bash", "lane.2.sh")
	c.Env = append(c.Env, "SWIM_QUEUE_ENTRY="+forged)
	c.CombinedOutput()
	os.Remove(forged)
	contains(t, "forged", r.log(2), "SKIP  small change 2 (dry run:")

	// Zach revokes: bob's next batch dry-runs.
	contains(t, "revoke --auto", r.mustSwim("revoke", "--auto", "bob", "--key", keys["zach"]), "revoked your approval in approvals/auto.bob.signature")
	r.script(2, "small change 3", `if guard ALLOW_SMALL "small change 3"; then run "small" true; fi`)
	r.mustSwim("queue", "add", "2", "--key", keys["bob"])
	r.mustSwim("watch", "--once")
	contains(t, "revoked", r.log(2), "SKIP  small change 3 (dry run:")
	contains(t, "verify", r.mustSwim("verify"), "verified: every link")
}
