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
