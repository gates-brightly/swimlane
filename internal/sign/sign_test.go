package sign

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func testKey(t *testing.T, name string) *Key {
	t.Helper()
	if !Available() {
		t.Skip("ssh-keygen not installed")
	}
	p := filepath.Join(t.TempDir(), name)
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", name, "-f", p).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	k, err := LoadKey(p)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSignVerify(t *testing.T) {
	a, b := testKey(t, "alice"), testKey(t, "bob")
	data := []byte("swim approval\nscript-sha256: abc\n")
	sig, err := Sign(a, data)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(a.Public, data, sig); err != nil {
		t.Fatalf("good signature rejected: %v", err)
	}
	if err := Verify(b.Public, data, sig); err == nil {
		t.Error("signature accepted for the wrong key")
	}
	if err := Verify(a.Public, []byte("tampered"), sig); err == nil {
		t.Error("signature accepted for changed data")
	}
	// Signing with the public key path works too (ssh-agent, or the
	// private key beside it).
	k2, _ := LoadKey(a.Path + ".pub")
	if k2.Public != a.Public || Fingerprint(a.Public) == "" {
		t.Errorf("pub path / fingerprint: %+v %q", k2, Fingerprint(a.Public))
	}
}
