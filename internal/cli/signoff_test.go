package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/sign"
)

// A signoff verifies only with its signature intact and by this machine's
// signing key; a statement changed after signing, or signed by another
// key, doesn't.
func TestVerifySignoff(t *testing.T) {
	if !sign.Available() {
		t.Skip("ssh-keygen not installed")
	}
	root := t.TempDir()
	keyPath := filepath.Join(t.TempDir(), "id")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", keyPath).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	key, err := sign.LoadKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	stmt := signoffVersion + "\ncommit " + strings.Repeat("a", 40) + "\ntree " + strings.Repeat("b", 40) +
		"\nlane 1\ntask checks\ncontext swim/checks\nscript abc\nresult passed\nsigner " + sign.KeyOnly(key.Public) + "\n"
	sig, err := sign.Sign(key, []byte(stmt))
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, s Signoff) string {
		b, _ := json.Marshal(s)
		p := filepath.Join(root, name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := write("good.json", Signoff{Statement: stmt, Signature: sig})
	tampered := write("tampered.json", Signoff{Statement: strings.Replace(stmt, "lane 1", "lane 2", 1), Signature: sig})

	if err := verifySignoff(root, "", good); err == nil || !strings.Contains(err.Error(), "not this machine's signing key") {
		t.Errorf("no signing key here: %v", err)
	}
	other := filepath.Join(t.TempDir(), "other")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", other).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	otherKey, err := sign.LoadKey(other)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifySignoff(root, otherKey.Public, good); err == nil || !strings.Contains(err.Error(), "not this machine's signing key") {
		t.Errorf("another key: %v", err)
	}
	if err := verifySignoff(root, key.Public, good); err != nil {
		t.Errorf("this machine's key: %v", err)
	}
	if err := verifySignoff(root, key.Public, tampered); err == nil || !strings.Contains(err.Error(), "bad signature") {
		t.Errorf("a changed statement: %v", err)
	}
}
