package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// signedRepo is a repo whose runner signs as zach, with one passed round
// in the chain.
func signedRepo(t *testing.T) (*repo, map[string]string) {
	t.Helper()
	r := newRepo(t)
	keys := people(t, r, "zach")
	r.mustSwim("config", "signing", "auto")
	r.mustSwim("config", "signing_key", keys["zach"])
	r.script(1, "one", `run "ok" true`)
	r.mustSwim("run", "1")
	return r, keys
}

// swim verify --yaml says what the exit code says, and what broke.
func TestVerifyYAML(t *testing.T) {
	r, _ := signedRepo(t)
	y := r.mustSwim("verify", "--yaml")
	contains(t, "yaml", y, "schema: swim.verify/v1", "ok: true", `consistent: "yes"`, "broken: []")
	logPath := filepath.Join(r.root, ".swim", "logs", "agent1.log")
	orig, _ := os.ReadFile(logPath)
	os.WriteFile(logPath, []byte(strings.Replace(string(orig), "PASS  ok", "PASS  ko", 1)), 0o644)
	y, code := r.swim("verify", "--yaml")
	if code != 1 {
		t.Fatalf("verify --yaml of an edited log: exit %d\n%s", code, y)
	}
	contains(t, "yaml, edited log", y, "ok: false", `consistent: "no"`, "was changed after the round ended")
}

// Runs that share an id (--run-id, a CI pipeline's) each attest their own
// lanes, and the same lane may run twice under it: that isn't a break.
func TestVerifySharedRunID(t *testing.T) {
	r, _ := signedRepo(t)
	r.script(2, "two", `if guard ALLOW_B "change b"; then run "b changed" true; fi`)
	r.mustSwim("run", "1", "--plain", "--run-id", "pipeline-7")
	r.mustSwimEnv([]string{"ALLOW_B=1"}, "run", "2", "--plain", "--run-id", "pipeline-7")
	r.script(1, "one", `run "ok again" true`)
	r.mustSwim("run", "1", "--plain", "--run-id", "pipeline-7")
	out, code := r.swim("verify", "--allow-unsigned")
	if code != 0 || strings.Contains(out, "BROKEN") {
		t.Fatalf("verify of runs sharing an id: exit %d\n%s", code, out)
	}
	contains(t, "verify", out, "VERIFIED")
}
