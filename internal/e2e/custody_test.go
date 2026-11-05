package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/chain"
)

// people makes an ssh key per name (its comment the name), and returns
// each private key's path.
func people(t *testing.T, r *repo, names ...string) map[string]string {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not installed")
	}
	dir := t.TempDir()
	keys := map[string]string{}
	for _, n := range names {
		p := filepath.Join(dir, n)
		if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", n, "-f", p).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		keys[n] = p
	}
	return keys
}

func dropLane(t *testing.T, r *repo) {
	r.script(1, "drop old table", `if guard FIN_ALLOW_DROP "drop table old_orders"; then run "drop" true; fi
run "after" true`)
	withHeader(t, r, 1, "# Guards:  FIN_ALLOW_DROP drop table old_orders")
}

// The chain records each approval and round: swim history reads it like
// git log, and swim verify catches an edited log or chain entry.
func TestHistoryAndVerify(t *testing.T) {
	r := newRepo(t)
	keys := people(t, r, "zach")
	r.mustSwim("config", "signing", "auto")
	r.mustSwim("config", "signing_key", keys["zach"]) // the runner
	dropLane(t, r)
	r.mustSwimEnv([]string{"FIN_ALLOW_DROP=1"}, "run", "1")
	contains(t, "approved", r.log(1), "APPROVED  drop table old_orders (FIN_ALLOW_DROP=1", "PASS  drop")

	hist := r.mustSwim("history", "1")
	contains(t, "history", hist, "approve FIN_ALLOW_DROP for swim 1 by ", "via flag",
		"round swim 1 passed (exit 0)", "Signer: SHA256:", "(zach)", "Date:   ", "    Action:    drop table old_orders")
	if !strings.HasPrefix(hist, "entry ") || strings.Index(hist, "round swim 1 passed") > strings.Index(hist, "approve FIN_ALLOW_DROP") {
		t.Errorf("history should start with the newest entry, like git log:\n%s", hist)
	}
	// --oneline, -n and --reverse, as in git log.
	// The newest: the run's attestation, its round, the approval.
	one := strings.Split(strings.TrimSpace(r.mustSwim("history", "--oneline", "-n", "3")), "\n")
	if len(one) != 3 || !regexp.MustCompile(`^[0-9a-f]{12} run r-\S+ attested: swim 1, 1 approval$`).MatchString(one[0]) ||
		!regexp.MustCompile(`^[0-9a-f]{12} round swim 1 passed \(exit 0\)$`).MatchString(one[1]) ||
		!strings.Contains(one[2], " approve FIN_ALLOW_DROP for swim 1") {
		t.Errorf("history --oneline -n 3: %q", one)
	}
	rev := strings.Split(strings.TrimSpace(r.mustSwim("history", "--oneline", "-3", "--reverse")), "\n")
	if len(rev) != 3 || rev[0] != one[2] || rev[2] != one[0] {
		t.Errorf("history --oneline -3 --reverse: %q, want %q reversed", rev, one)
	}
	contains(t, "verify", r.mustSwim("verify", "--allow-unsigned"), "VERIFIED")

	// Tampering is provable.
	logPath := filepath.Join(r.root, ".swim", "logs", "agent1.log")
	orig, _ := os.ReadFile(logPath)
	os.WriteFile(logPath, []byte(strings.Replace(string(orig), "PASS  drop", "PASS  dorp", 1)), 0o644)
	if out, code := r.swim("verify", "--allow-unsigned"); code != 1 || !strings.Contains(out, "log for run") || !strings.Contains(out, "was changed after the round ended") {
		t.Errorf("edited log: %d %s", code, out)
	}
	os.WriteFile(logPath, orig, 0o644)
	cp := chain.Path(r.root)
	corig, _ := os.ReadFile(cp)
	os.WriteFile(cp, []byte(strings.Replace(string(corig), `"FIN_ALLOW_DROP"`, `"FIN_ALLOW_OTHER"`, 1)), 0o644)
	if out, code := r.swim("verify", "--allow-unsigned"); code != 1 || !strings.Contains(out, "signature doesn't match the entry") || !strings.Contains(out, "prev hash doesn't match") {
		t.Errorf("edited chain: %d %s", code, out)
	}
	os.WriteFile(cp, corig, 0o644)
	r.mustSwim("verify", "--allow-unsigned")
}

func TestChainRecordsEveryApproval(t *testing.T) {
	r := newRepo(t)
	keys := people(t, r, "zach")
	r.mustSwim("config", "signing", "auto")
	r.mustSwim("config", "signing_key", keys["zach"])
	r.script(1, "flagged", `if guard ALLOW_A "change a"; then run "a" true; fi`)
	c := r.cmd(bin, "run", "1")
	c.Env = append(c.Env, "ALLOW_A=1")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	r.script(1, "queued", `if guard ALLOW_B "change b"; then run "b" true; fi`)
	withHeader(t, r, 1, "# Guards:  ALLOW_B change b")
	r.mustSwim("queue", "1", "--approve", "ALLOW_B")
	r.mustSwim("watch", "--once")
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
	if strings.Join(methods, " ") != "flag:ALLOW_A queue:ALLOW_B" {
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
	r.mustSwimEnv([]string{"ALLOW_B=1"}, "run", "1")
	if out, code := r.swim("verify", "--allow-unsigned", "--strict"); code != 1 || !strings.Contains(out, "unsigned entries") {
		t.Errorf("--strict with unsigned entries: %d %s", code, out)
	}
	r.mustSwim("verify", "--allow-unsigned")
}

// Unsigned entries aren't BROKEN: ones a later signed entry links to are a
// warning (they can't change unseen), and ones at the end, which nothing
// vouches for, fail as UNSIGNED.
func TestVerifyUnsigned(t *testing.T) {
	r := newRepo(t)
	keys := people(t, r, "zach")
	r.script(1, "hello", `run "hi" true`)
	out, _ := r.swim("run", "1", "--plain") // signing: off
	contains(t, "unsigned run", out, "PASS")
	out, code := r.swim("verify")
	if code == 0 || !strings.Contains(out, "UNSIGNED: ") || strings.Contains(out, "BROKEN") {
		t.Fatalf("trailing unsigned: %d\n%s", code, out)
	}
	r.mustSwim("config", "signing", "auto")
	r.mustSwim("config", "signing_key", keys["zach"])
	r.mustSwim("run", "1", "--plain")
	out = r.mustSwim("verify")
	contains(t, "covered unsigned", out, "VERIFIED", "can't change unseen")
	if strings.Contains(out, "BROKEN") || strings.Contains(out, "UNSIGNED:") {
		t.Errorf("covered unsigned entries reported as a failure:\n%s", out)
	}
}

// swim verify --force acknowledges gaps and deletions in a signed amend
// entry: they show as AMENDED (by whom, not the original signer), and
// verify passes. Changed content isn't amended, and a gap after the
// amendment is BROKEN again.
func TestVerifyForceAmendsGaps(t *testing.T) {
	r := newRepo(t)
	keys := people(t, r, "zach")
	r.mustSwim("config", "signing", "auto")
	r.mustSwim("config", "signing_key", keys["zach"])
	r.script(1, "hello", `run "hi" true`)
	for i := 0; i < 3; i++ {
		r.mustSwim("run", "1", "--plain")
	}
	chainPath := filepath.Join(r.root, ".swim", "chain.jsonl")
	drop := func(i int) {
		data, _ := os.ReadFile(chainPath)
		ls := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		ls = append(ls[:i], ls[i+1:]...)
		os.WriteFile(chainPath, []byte(strings.Join(ls, "\n")+"\n"), 0o644)
	}
	drop(1) // an entry removed from the middle
	os.Remove(filepath.Join(r.root, ".swim", "logs", "agent1.log"))
	out, code := r.swim("verify")
	if code == 0 || !strings.Contains(out, "BROKEN: entry #3 (line 2): entry #2 is missing before it (removed)") {
		t.Fatalf("gap: %d\n%s", code, out)
	}
	out = r.mustSwim("verify", "--force")
	contains(t, "force", out, "amended", "signed by SHA256:", "(zach)", "AMENDED: entry #3", "not the original signer", "VERIFIED")
	out = r.mustSwim("verify")
	contains(t, "after", out, "AMENDED: ", "acknowledged by SHA256:", "(zach)", "VERIFIED")
	if strings.Contains(out, "BROKEN") {
		t.Errorf("an amended gap is still BROKEN:\n%s", out)
	}
	contains(t, "history", r.mustSwim("history", "--oneline", "-n", "1"), "amend:", "acknowledged by SHA256:")

	// Nothing left: --force records nothing.
	contains(t, "again", r.mustSwim("verify", "--force"), "no gaps or deleted items to amend")

	// A gap after the amendment isn't covered by it.
	data, _ := os.ReadFile(chainPath)
	n := strings.Count(string(data), "\n")
	drop(n - 2)
	if out, code := r.swim("verify"); code == 0 || !strings.Contains(out, "BROKEN: ") {
		t.Errorf("a new gap after the amendment: %d\n%s", code, out)
	}
}
