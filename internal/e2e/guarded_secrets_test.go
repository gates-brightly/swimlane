package e2e

// Guarded secrets: a secret swim.yml guards is released to a lane only
// once that lane's guard was approved in this run, by the launcher.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/chain"
)

const (
	gsFlag  = "GS_ALLOW"
	gsValue = "guarded-pr0d-value"
)

// guardedRepo is a repo whose swim.yml guards PROD_DB by GS_ALLOW (with
// extra appended to its entry), holding the secret.
func guardedRepo(t *testing.T, extra string) *repo {
	t.Helper()
	r := newRepo(t, "")
	secretsKey(t, r)
	r.write("swim.yml", "secrets:\n  PROD_DB: { guard: "+gsFlag+extra+" }\n", 0o644)
	r.setSecret("PROD_DB", gsValue)
	return r
}

// guardedLane writes lane n: body, with GS_ALLOW in its # Guards:.
func guardedLane(t *testing.T, r *repo, n int, body string) {
	t.Helper()
	r.script(n, "uses the prod db", body)
	withHeader(t, r, n, "# Guards:  "+gsFlag+" use the prod db")
}

const gsApproved = `if guard ` + gsFlag + ` "use the prod db"; then
  DB=$(swim secret get PROD_DB) || stop "no db"
  run "use" bash -c "echo db=$DB"
fi
run "after" true`

// links of a repo's chain.
func chainLinks(t *testing.T, r *repo) []chain.Link {
	t.Helper()
	links, err := chain.Read(r.root)
	if err != nil {
		t.Fatal(err)
	}
	return links
}

// secretEntries are the chain's secret-released and secret-refused entries.
func secretEntries(t *testing.T, r *repo, typ string) []chain.Link {
	var out []chain.Link
	for _, l := range chainLinks(t, r) {
		if l.Type == typ {
			out = append(out, l)
		}
	}
	return out
}

// noValue fails if the guarded value is in any file under the repo.
func noValue(t *testing.T, r *repo) {
	t.Helper()
	filepath.Walk(r.root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		if data, _ := os.ReadFile(p); strings.Contains(string(data), gsValue) {
			t.Errorf("%s holds the guarded secret", p)
		}
		return nil
	})
}

func TestGuardedSecretRefused(t *testing.T) {
	r := guardedRepo(t, "")
	cases := []struct{ name, body string }{
		// The lane skips the guard and asks for the secret.
		{"no guard", `DB=$(swim secret get PROD_DB) || stop "refused"
run "use" bash -c "echo db=$DB"`},
		// The lane sets the flag itself: guard declines it, and the broker
		// doesn't count it either.
		{"lane exports the flag", `export ` + gsFlag + `=1
guard ` + gsFlag + ` "use the prod db"
DB=$(swim secret get PROD_DB) || stop "refused"
run "use" bash -c "echo db=$DB"`},
		// No one to ask: the guard is a dry run.
		{"guard not approved", gsApproved + `
DB=$(swim secret get PROD_DB) || stop "refused"
run "use" bash -c "echo db=$DB"`},
		// The lane hides from swim run's approvals and sets the flag (T28).
		{"lane drops its approval channel", `unset SWIM_APPROVAL SWIM_LAUNCHED
export ` + gsFlag + `=1
guard ` + gsFlag + ` "use the prod db"
DB=$(swim secret get PROD_DB) || stop "refused"
run "use" bash -c "echo db=$DB"`},
	}
	for i, c := range cases {
		n := i + 1
		guardedLane(t, r, n, c.body)
		cmd := r.cmd(bin, "run", string(rune('0'+n)), "--plain")
		cmd.SysProcAttr = noTTY()
		out, _ := cmd.CombinedOutput()
		log := r.log(n)
		contains(t, c.name+": agent log", log, "WARN  secret PROD_DB refused (PROD_DB is guarded by "+gsFlag+", which isn't approved in this run)", "STOP  refused")
		contains(t, c.name+": output", string(out), "refused: PROD_DB is guarded by "+gsFlag)
		if strings.Contains(log, "PASS  use") {
			t.Errorf("%s: the step that needed it ran:\n%s", c.name, log)
		}
	}
	// .lane.N.rc setting the flag counts no more than an export.
	guardedLane(t, r, 5, cases[0].body)
	r.write(".lane.5.rc", gsFlag+"=1\n", 0o644)
	r.swim("run", "5", "--plain")
	contains(t, ".lane.5.rc", r.log(5), "WARN  secret PROD_DB refused")

	refused := secretEntries(t, r, "secret-refused")
	if len(refused) < 4 {
		t.Fatalf("secret-refused entries: %d", len(refused))
	}
	for _, l := range refused {
		if l.Secret != "PROD_DB" || l.Guard != gsFlag || l.Lane == 0 || l.Run == "" || !strings.Contains(l.Action, "isn't approved") {
			t.Errorf("refused entry: %+v", l.Entry)
		}
	}
	if len(secretEntries(t, r, "secret-released")) != 0 {
		t.Error("a release was recorded")
	}
	hist, _ := os.ReadFile(filepath.Join(r.root, ".swim.log"))
	contains(t, ".swim.log", string(hist), "secret-refused  swim 1", "PROD_DB  (PROD_DB is guarded by "+gsFlag)
	noValue(t, r)
}

func TestGuardedSecretReleased(t *testing.T) {
	r := guardedRepo(t, "")
	guardedLane(t, r, 1, gsApproved+`
run "inline" bash -c 'echo inline=$(swim secret get PROD_DB)'`)
	// Lane 2 has no guard: -y approves lane 1's, never lane 2's.
	guardedLane(t, r, 2, `DB=$(swim secret get PROD_DB) || stop "refused"
run "use" bash -c "echo db=$DB"`)
	out, code := r.swim("run", "1", "2", "-y", "--plain")
	if code == 0 {
		t.Fatalf("lane 2 should have stopped:\n%s", out)
	}
	contains(t, "agent1.log", r.log(1), "APPROVED  use the prod db", "PASS  use", "db=***(PROD_DB)", "inline=***(PROD_DB)")
	contains(t, "agent2.log", r.log(2), "WARN  secret PROD_DB refused")
	contains(t, "output", out, "has no passphrase") // the throwaway key

	rel := secretEntries(t, r, "secret-released")
	if len(rel) != 2 { // the get, and the inline one in the step
		t.Fatalf("secret-released entries: %+v", rel)
	}
	var appr string
	for _, l := range chainLinks(t, r) {
		if l.Type == "approve" && l.Lane == 1 && l.Guard == gsFlag {
			appr = l.Hash
		}
	}
	l := rel[0]
	if l.Secret != "PROD_DB" || l.Lane != 1 || l.Guard != gsFlag || l.Method != "auto" || len(l.Approvals) != 1 || l.Approvals[0] != appr || l.Manifest == "" {
		t.Errorf("released entry: %+v (approve entry %s)", l.Entry, appr)
	}
	hist, _ := os.ReadFile(filepath.Join(r.root, ".swim.log"))
	contains(t, ".swim.log", string(hist), "secret-released  swim 1", "PROD_DB (guarded by "+gsFlag+", approved by auto)")
	contains(t, "verify", r.mustSwim("verify"), "consistent  yes")
	contains(t, "chain", r.mustSwim("history"), "secret PROD_DB released to swim 1 (guard "+gsFlag+", approved by auto)", "secret PROD_DB refused to swim 2")
	noValue(t, r)

	// From swim run's own environment, too.
	c := r.cmd(bin, "run", "1", "--plain")
	c.Env = append(c.Env, gsFlag+"=1")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%s=1 swim run: %v\n%s", gsFlag, err, out)
	}
	contains(t, "agent1.log", r.log(1), "db=***(PROD_DB)")
	if rel := secretEntries(t, r, "secret-released"); rel[len(rel)-1].Method != "flag" {
		t.Errorf("released by the flag: %+v", rel[len(rel)-1].Entry)
	}
}

// release: step hands the secret out once.
func TestGuardedSecretReleaseStep(t *testing.T) {
	r := guardedRepo(t, ", release: step")
	guardedLane(t, r, 1, gsApproved+`
AGAIN=$(swim secret get PROD_DB) || stop "second get refused"`)
	r.swim("run", "1", "-y", "--plain")
	contains(t, "agent1.log", r.log(1), "PASS  use", "WARN  secret PROD_DB refused (PROD_DB is released for one step (release: step), and this lane already got it)", "STOP  second get refused")
}

// A signed approval and a queue entry's approve: release it too.
func TestGuardedSecretSignedAndQueued(t *testing.T) {
	r := guardedRepo(t, "")
	keys := people(t, r, "", "zach")
	guardedLane(t, r, 1, gsApproved)
	r.mustSwim("approve", "1", "--guards", gsFlag, "--key", keys["zach"])
	c := r.cmd(bin, "run", "1", "--plain")
	c.SysProcAttr = noTTY()
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("signed: %v\n%s", err, out)
	}
	contains(t, "signed", r.log(1), "PASS  use", "db=***(PROD_DB)")
	if rel := secretEntries(t, r, "secret-released"); len(rel) != 1 || rel[0].Method != "signature" {
		t.Fatalf("signed release: %+v", rel)
	}

	q := guardedRepo(t, "")
	guardedLane(t, q, 1, gsApproved)
	q.mustSwim("queue", "add", "1", "--approve", gsFlag)
	if out, err := q.cmd(bin, "watch", "--once").CombinedOutput(); err != nil {
		t.Fatalf("watch: %v\n%s", err, out)
	}
	contains(t, "queued", q.log(1), "PASS  use", "db=***(PROD_DB)")
	if rel := secretEntries(t, q, "secret-released"); len(rel) != 1 || rel[0].Method != "queue" {
		t.Fatalf("queued release: %+v", rel)
	}
	noValue(t, r)
	noValue(t, q)
}

// In CI the secret comes from SWIM_SECRET_<NAME>: a guarded one never
// reaches the lane's environment, only the broker (once approved).
func TestGuardedSecretFromCIEnv(t *testing.T) {
	r := newRepo(t, "")
	r.write("swim.yml", "secrets:\n  PROD_DB: { guard: "+gsFlag+" }\n", 0o644)
	r.env = append(r.env, "SWIM_SECRET_PROD_DB="+gsValue, "SWIM_SECRET_OTHER=other-ci-value")
	guardedLane(t, r, 1, `run "env" bash -c 'echo prod=${SWIM_SECRET_PROD_DB:-unset} other=${SWIM_SECRET_OTHER:+set}'
`+gsApproved)
	out, code := r.swim("run", "1", "-y", "--plain")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	contains(t, "agent1.log", r.log(1), "prod=unset other=set", "db=***(prod_db)") // stand-ins are named in lower case
	noValue(t, r)
}

// Outside a run the operator may get it (recorded); a lane run with bash,
// which has no broker to approve its guard, may not.
func TestGuardedSecretOutsideRun(t *testing.T) {
	r := guardedRepo(t, "")
	if got := r.mustSwim("secret", "get", "PROD_DB"); got != gsValue {
		t.Fatalf("get at the operator's shell: %q", got)
	}
	hist, _ := os.ReadFile(filepath.Join(r.root, ".swim.log"))
	contains(t, ".swim.log", string(hist), "PROD_DB (repo scope, guarded by "+gsFlag+"; got outside a run by")
	contains(t, "ls", r.mustSwim("secret", "ls"), "guarded by "+gsFlag+" (swim.yml)")

	guardedLane(t, r, 1, `DB=$(swim secret get PROD_DB) || stop "refused"`)
	c := r.cmd("bash", "lane.1.sh")
	c.SysProcAttr = noTTY()
	out, _ := c.CombinedOutput()
	contains(t, "bash lane", string(out), "refused: PROD_DB is guarded by "+gsFlag+": a lane gets it only from its swim run")
	contains(t, "agent1.log", r.log(1), "WARN  secret PROD_DB refused", "STOP  refused")
}

// An SDK lane gets the same refusal, as its language's error.
func TestGuardedSecretSDKRefusal(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	r := guardedRepo(t, "")
	r.env = append(r.env, "PYTHONPATH="+filepath.Join(repoRoot(), "sdk", "python"))
	r.write("lane.1.py", `#!/usr/bin/env python3
# Round: python guarded secret
import swim
with swim.round() as r:
    try:
        swim.secret.get("PROD_DB")
        r.run("got it", ["echo", "released"])
    except swim.SwimError as e:
        r.run("refused", ["echo", "error: " + str(e)])
`, 0o755)
	r.swim("run", "1", "--plain")
	contains(t, "agent1.log", r.log(1), "error: secret PROD_DB: refused: PROD_DB is guarded by "+gsFlag+", which isn't approved in this run")
	if strings.Contains(r.log(1), "released") {
		t.Errorf("released to an SDK lane without a guard:\n%s", r.log(1))
	}
}
