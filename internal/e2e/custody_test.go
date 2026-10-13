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

	if out, code := r.swim("sign", "1", "--guards", "FIN_ALLOW_TYPO", "--key", keys["zach"]); code == 0 || !strings.Contains(out, "has no guard FIN_ALLOW_TYPO") {
		t.Fatalf("typo guard: %d %s", code, out)
	}
	contains(t, "first signature", r.mustSwim("sign", "1", "--guards", "FIN_ALLOW_DROP", "--key", keys["zach"]),
		"signed lane.1.sh.signature as zach", "not yet:  FIN_ALLOW_DROP: lane.1.sh.signature needs 2 signature(s) from platform-admin, has 1")
	r.mustSwim("run", "1")
	contains(t, "one of two", r.log(1), "SKIP  drop table old_orders (dry run: lane.1.sh.signature needs 2 signature(s) from platform-admin, has 1; set FIN_ALLOW_DROP=1 to approve)")

	// A valid signature from a key not in lookup.yml doesn't count.
	contains(t, "mallory", r.mustSwim("sign", "1", "--key", keys["mallory"]), "not in lookup.yml: it won't count")
	if out, code := r.swim("sign", "1", "--guards", "OTHER", "--key", keys["alice"]); code == 0 || !strings.Contains(out, "co-signing signs that statement as is") {
		t.Fatalf("co-sign changing the statement: %d %s", code, out)
	}
	contains(t, "second signature", r.mustSwim("sign", "1", "--key", keys["alice"]), "approved: FIN_ALLOW_DROP (signed by alice, zach)")
	r.mustSwim("run", "1")
	contains(t, "approved", r.log(1), "APPROVED  drop table old_orders (approved by signature: ", "zach (platform-admin)", "alice (platform-admin)", "PASS  drop")

	hist := r.mustSwim("history", "1")
	contains(t, "history", hist, "approve  swim 1  FIN_ALLOW_DROP", "via signature", "round  swim 1  passed exit 0", "signed by zach (platform-admin)")
	contains(t, "verify", r.mustSwim("verify"), "verified: every link, signature, log and script checks out", "3 signatories")

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
	contains(t, "stale", r.log(1), "lane.1.sh.signature approves another version of lane.1.sh")
	contains(t, "stale verify", r.mustSwim("verify"), "stale: lane.1.sh changed since it was signed")
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
	// Nothing signed: nothing to do.
	contains(t, "nothing", r.mustSwim("watch", "--once", "--signed"), "swim watch")
	// Alice approves remotely by dropping a signature; the watch runs it.
	r.mustSwim("sign", "1", "--guards", "FIN_ALLOW_DROP", "--key", keys["alice"])
	out := r.mustSwim("watch", "--once", "--signed")
	contains(t, "watch", out, "queued swim 1", "signed by alice", "passed; consumed")
	contains(t, "agent1.log", r.log(1), "APPROVED  drop table old_orders (approved by signature: alice)", "PASS  drop")
	// Already ran: not queued again.
	if out := r.mustSwim("watch", "--once", "--signed"); strings.Contains(out, "queued swim 1") {
		t.Errorf("queued the same signed approval twice:\n%s", out)
	}
	contains(t, "keys", r.mustSwim("keys"), "alice", "zach", "this machine signs with")
}
