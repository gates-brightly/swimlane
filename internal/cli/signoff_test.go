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

// A signoff verifies only with its signature intact and a key someone is
// named for; a statement changed after signing, or signed by a stranger,
// doesn't.
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

	if err := verifySignoff(root, good); err == nil || !strings.Contains(err.Error(), "neither lookup.yml nor trust.yml") {
		t.Errorf("a stranger's key: %v", err)
	}
	lookupYML := "users:\n  zach:\n    keys: [" + sign.KeyOnly(key.Public) + "]\n"
	if err := os.WriteFile(filepath.Join(root, "lookup.yml"), []byte(lookupYML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifySignoff(root, good); err != nil {
		t.Errorf("a named key: %v", err)
	}
	if err := verifySignoff(root, tampered); err == nil || !strings.Contains(err.Error(), "bad signature") {
		t.Errorf("a changed statement: %v", err)
	}
}
