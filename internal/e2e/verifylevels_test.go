package e2e

import (
	"os"
	"os/exec"
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

// trusted: trust.yml pinned on first use; a hand edit fails it, a change
// signed by a trusted key moves the pin along; CI pins by hash.
func TestVerifyTrustedLevel(t *testing.T) {
	r, keys := signedRepo(t)
	contains(t, "no trust.yml", r.mustSwim("verify"), "consistent  yes", "trusted     n/a", "no trust.yml")
	if out, code := r.swim("verify", "--strict"); code != 1 {
		t.Errorf("--strict with trusted n/a: exit %d\n%s", code, out)
	}

	contains(t, "keys trust", r.mustSwim("keys", "trust"), "to trust.yml (1 keys")
	// lookup.yml names alice, whose key trust.yml doesn't list.
	if out, code := r.swim("lint"); code == 0 || !strings.Contains(out, "alice's key alice isn't in trust.yml") {
		t.Errorf("lint, lookup.yml key outside trust.yml: exit %d\n%s", code, out)
	}
	contains(t, "first verify", r.mustSwim("verify"), "trusted     yes", "1 signers, all in trust.yml (pinned now, on first use)")
	contains(t, "pinned", r.mustSwim("verify"), "trusted     yes", "(pinned 20")
	contains(t, "keys", r.mustSwim("keys"), "trust.yml: 1 keys", "; pinned")

	// A hand edit (mallory's key added) is refused until pinned again.
	tf := filepath.Join(r.root, "trust.yml")
	orig, _ := os.ReadFile(tf)
	mallory := filepath.Join(t.TempDir(), "mallory")
	exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "mallory", "-f", mallory).Run()
	pub, _ := os.ReadFile(mallory + ".pub")
	os.WriteFile(tf, append(append([]byte(nil), orig...), []byte("  - "+strings.TrimSpace(string(pub))+"\n")...), 0o644)
	out, code := r.swim("verify")
	if code != 1 || !strings.Contains(out, "trusted     no") || !strings.Contains(out, "changed since it was pinned") {
		t.Fatalf("unsigned trust.yml edit: exit %d\n%s", code, out)
	}
	os.WriteFile(tf, orig, 0o644)

	// Signed by zach (trusted): alice joins, and every pin follows.
	contains(t, "signed change", r.mustSwim("keys", "trust", keys["alice"]+".pub", "--sign", "--key", keys["zach"]), "added", "signed in trust.yml.sig")
	contains(t, "after a signed change", r.mustSwim("verify"), "trusted     yes", "1 signed changes since the pin")
	contains(t, "pin moved", r.mustSwim("verify"), "trusted     yes", "(pinned 20")
	r.mustSwim("lint")

	// A change signed by a key trust.yml doesn't hold is refused.
	if out, code := r.swim("keys", "trust", mallory+".pub", "--sign", "--key", mallory); code == 0 || !strings.Contains(out, "isn't in trust.yml") {
		t.Errorf("change signed by an untrusted key: exit %d\n%s", code, out)
	}

	// CI pins by hash: the YAML has each level.
	pinOut := r.mustSwim("keys", "pin")
	i := strings.Index(pinOut, "--trust-pin ")
	full := strings.Fields(pinOut[i+len("--trust-pin "):])[0]
	y, code := r.swimEnv([]string{"SWIM_TRUST_PIN=" + full}, "verify", "--yaml")
	if code != 0 || !strings.Contains(y, "schema: swim.verify/v1") || !strings.Contains(y, `trusted: "yes"`) || !strings.Contains(y, `consistent: "yes"`) {
		t.Errorf("verify --yaml with SWIM_TRUST_PIN: exit %d\n%s", code, y)
	}
	if out, code := r.swim("verify", "--trust-pin", strings.Repeat("0", 64)); code != 1 || !strings.Contains(out, "trusted     no") {
		t.Errorf("verify against another pin: exit %d\n%s", code, out)
	}
}

// anchored: each run sends a checkpoint to a file: anchor outside the
// repo; entries removed from the chain's end, or a chain started over,
// fail the level, and --against checks an exported copy.
func TestVerifyAnchoredLevel(t *testing.T) {
	r, _ := signedRepo(t)
	contains(t, "no anchors", r.mustSwim("verify"), "anchored    n/a", "no anchors configured")
	contains(t, "doctor, no anchors", must(r.swim("doctor")), "[anchors-none]")

	dir := filepath.Join(t.TempDir(), "audit")
	r.write("swim.yml", "chain_anchors: [\"file:"+dir+"\"]\n", 0o644)
	contains(t, "no checkpoint yet", r.mustSwim("verify"), "anchored    n/a", "no checkpoint at file:"+dir)
	r.mustSwim("run", "1")
	r.mustSwim("run", "1")
	out := r.mustSwim("verify")
	contains(t, "anchored", out, "anchored    yes", "extends checkpoint seq 6", "from file:"+dir+"; 0 entries since", "anchoring proves this history existed by")
	if _, err := os.Stat(filepath.Join(dir, "checkpoints.jsonl")); err != nil {
		t.Fatal(err)
	}
	if out := must(r.swim("doctor")); strings.Contains(out, "checkpoint-unsent") || strings.Contains(out, "anchors-none") {
		t.Errorf("doctor with every checkpoint sent:\n%s", out)
	}
	contains(t, "export", r.mustSwim("chain", "export", "--to", "file:"+dir), "exported 6 new entries")

	// Remove the last two entries.
	path := filepath.Join(r.root, ".swim", "chain.jsonl")
	data, _ := os.ReadFile(path)
	lines := strings.SplitAfter(string(data), "\n")
	os.WriteFile(path, []byte(lines[0]), 0o644)
	out, code := r.swim("verify")
	if code != 1 || !strings.Contains(out, "anchored    no") || !strings.Contains(out, "chain truncated or replaced after checkpoint seq 6") {
		t.Fatalf("verify of a truncated chain: exit %d\n%s", code, out)
	}
	if out, code := r.swim("verify", "--against", "file:"+dir); code != 1 || !strings.Contains(out, "lost entries from its end") {
		t.Errorf("verify --against of a truncated chain: exit %d\n%s", code, out)
	}
	// The anchor refuses to go back, and so does export.
	if out, code := r.swim("chain", "checkpoint"); code == 0 || !strings.Contains(out, "not past the last checkpoint") {
		t.Errorf("checkpoint of a truncated chain: exit %d\n%s", code, out)
	}
	contains(t, "doctor, unsent", must(r.swim("doctor")), "[checkpoint-unsent]")
	if out, code := r.swim("chain", "export", "--to", "file:"+dir); code == 0 || !strings.Contains(out, "nothing exported") {
		t.Errorf("export of a truncated chain: exit %d\n%s", code, out)
	}

	// The repo lost and started over: a new chain, checked against the
	// exported copy.
	os.Remove(path)
	r.mustSwim("run", "1")
	if out, code := r.swim("verify", "--against", "file:"+dir); code != 1 || !strings.Contains(out, "anchored    no") || !strings.Contains(out, "was replaced") {
		t.Errorf("verify --against of a new chain: exit %d\n%s", code, out)
	}
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
	contains(t, "signed", r.mustSwim("verify"), "authorized  yes", "1 approvals, each met its lookup.yml rule")

	// FLAG=1 for a guard whose rule says signed: it runs, but it isn't
	// authorized.
	os.RemoveAll(filepath.Join(r.root, "approvals"))
	r.swimEnv([]string{"FIN_ALLOW_DROP=1"}, "run", "1")
	out, code := r.swim("verify")
	if code != 1 || !strings.Contains(out, "authorized  no") || !strings.Contains(out, "approved by flag, but lookup.yml says FIN_ALLOW_DROP needs signed approvals") {
		t.Fatalf("flag approval of a signed-only guard: exit %d\n%s", code, out)
	}
	y, _ := r.swim("verify", "--yaml")
	contains(t, "yaml", y, `authorized: "no"`, `consistent: "yes"`)

	// lookup.yml changed since: a warning, and partial.
	lk, _ := os.ReadFile(filepath.Join(r.root, "lookup.yml"))
	os.WriteFile(filepath.Join(r.root, "lookup.yml"), append(lk, []byte("# reviewed\n")...), 0o644)
	out = r.mustSwim("verify")
	contains(t, "changed lookup.yml", out, "authorized  partial", "it was approved under another")
	if out, code := r.swim("verify", "--strict"); code != 1 {
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
	out, code := r.swim("verify")
	if code != 0 || strings.Contains(out, "BROKEN") {
		t.Fatalf("verify of runs sharing an id: exit %d\n%s", code, out)
	}
	contains(t, "verify", out, "consistent  yes", "authorized  yes")
}

// swim chain checkpoint says "sent to" only for an anchor that took this
// checkpoint, not one that took the same seq before.
func TestCheckpointSendFails(t *testing.T) {
	r, _ := signedRepo(t)
	dir := filepath.Join(t.TempDir(), "audit")
	r.write("swim.yml", "chain_anchors: [\"file:"+dir+"\"]\n", 0o644)
	contains(t, "checkpoint", r.mustSwim("chain", "checkpoint"), "sent to file:"+dir)

	// The anchor is gone: a file where its directory was.
	os.RemoveAll(dir)
	os.WriteFile(dir, []byte("not a directory"), 0o644)
	out, code := r.swim("chain", "checkpoint")
	if code != 1 || strings.Contains(out, "sent to") {
		t.Fatalf("checkpoint to an anchor that's gone: exit %d\n%s", code, out)
	}
	contains(t, "doctor", must(r.swim("doctor")), "[checkpoint-unsent]", "the last send to it failed")
	if out, code := r.swim("chain", "export", "--to", "file:"+dir); code != 1 {
		t.Errorf("export to an anchor that's gone: exit %d\n%s", code, out)
	}
}
