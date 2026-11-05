package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// signedRepo is a repo whose runner signs as zach, with alice also in
// lookup.yml, and one passed round in the chain.
func signedRepo(t *testing.T) (*repo, map[string]string) {
	t.Helper()
	r := newRepo(t, "")
	keys := people(t, r, "", "zach", "alice")
	r.mustSwim("config", "signing", "auto")
	r.mustSwim("config", "signing_key", keys["zach"])
	r.script(1, "one", `run "ok" true`)
	r.mustSwim("run", "1")
	return r, keys
}

// authorized: an approval that didn't meet lookup.yml's rule for its guard
// fails the level, while lookup.yml is the one it was recorded under; once
// lookup.yml has changed it's a warning.
func TestVerifyAuthorizedLevel(t *testing.T) {
	r := newRepo(t, "")
	rules := "groups:\n  admins: [zach]\nrequire:\n  - guards: [FIN_ALLOW_*]\n    group: admins\n    signed: true\n"
	keys := people(t, r, rules, "zach", "alice")
	r.mustSwim("config", "signing", "auto")
	r.mustSwim("config", "signing_key", keys["zach"])
	dropLane(t, r)

	// Signed by zach (admins): authorized.
	r.mustSwim("approve", "1", "--guards", "FIN_ALLOW_DROP", "--key", keys["zach"])
	r.mustSwim("run", "1")
	contains(t, "signed", r.mustSwim("verify", "--levels", "--allow-unsigned"), "authorized  yes", "1 approval, each met its lookup.yml rule")

	// FLAG=1 for a guard whose rule says signed: it runs, but it isn't
	// authorized.
	os.RemoveAll(filepath.Join(r.root, "approvals"))
	r.swimEnv([]string{"FIN_ALLOW_DROP=1"}, "run", "1")
	out, code := r.swim("verify", "--levels", "--allow-unsigned")
	if code != 1 || !strings.Contains(out, "authorized  no") || !strings.Contains(out, "UNAUTHORIZED: entry #") || strings.Contains(out, "BROKEN") ||
		!strings.Contains(out, "approved by flag, but lookup.yml says FIN_ALLOW_DROP needs signed approvals") {
		t.Fatalf("flag approval of a signed-only guard: exit %d\n%s", code, out)
	}
	y, _ := r.swim("verify", "--levels", "--allow-unsigned", "--yaml")
	contains(t, "yaml", y, `authorized: "no"`, `consistent: "yes"`, "unauthorized:\n  - 'entry #", "broken: []")

	// lookup.yml changed since: a warning, and partial.
	lk, _ := os.ReadFile(filepath.Join(r.root, "lookup.yml"))
	os.WriteFile(filepath.Join(r.root, "lookup.yml"), append(lk, []byte("# reviewed\n")...), 0o644)
	out = r.mustSwim("verify", "--levels", "--allow-unsigned")
	contains(t, "changed lookup.yml", out, "authorized  partial", "it was approved under another")
	if out, code := r.swim("verify", "--levels", "--allow-unsigned", "--strict"); code != 1 {
		t.Errorf("--strict with authorized partial: exit %d\n%s", code, out)
	}
}

// Runs that share an id (--run-id, a CI pipeline's) each attest their own
// lanes, and the same lane may run twice under it: that isn't a break.
func TestVerifySharedRunID(t *testing.T) {
	r, _ := signedRepo(t)
	r.script(2, "two", `if guard ALLOW_B "change b"; then run "b changed" true; fi`)
	r.mustSwim("run", "1", "--plain", "--run-id", "pipeline-7")
	r.mustSwim("run", "2", "-y", "--plain", "--run-id", "pipeline-7")
	r.script(1, "one", `run "ok again" true`)
	r.mustSwim("run", "1", "--plain", "--run-id", "pipeline-7")
	out, code := r.swim("verify", "--levels", "--allow-unsigned")
	if code != 0 || strings.Contains(out, "BROKEN") {
		t.Fatalf("verify of runs sharing an id: exit %d\n%s", code, out)
	}
	contains(t, "verify", out, "consistent  yes", "authorized  yes")
}
