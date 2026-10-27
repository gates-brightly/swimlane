package sign

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// A run key's signatures are ssh-keygen's own format, and its delegation
// holds only for the run, key and runner key it was made for.
func TestRunKey(t *testing.T) {
	by, other := testKey(t, "runner"), testKey(t, "other")
	rk, err := NewRunKey(by, "r1", "host")
	if err != nil {
		t.Fatal(err)
	}
	if rk.Path != "" || rk.RunKey == nil || rk.RunKey.By != by.Public {
		t.Fatalf("run key: %+v", rk)
	}
	data := []byte(`{"seq":1}`)
	sig, err := Sign(rk, data)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(rk.Public, data, sig); err != nil {
		t.Fatalf("ssh-keygen rejects a run key's signature: %v", err)
	}
	if err := Verify(rk.Public, []byte(`{"seq":2}`), sig); err == nil {
		t.Error("a run key's signature accepted for changed data")
	}
	if err := rk.RunKey.Check(rk.Public); err != nil {
		t.Fatalf("delegation: %v", err)
	}
	if err := rk.RunKey.Check(other.Public); err == nil {
		t.Error("a delegation accepted for another key")
	}
	d := *rk.RunKey
	d.Run = "r2"
	if err := d.Check(rk.Public); err == nil {
		t.Error("a delegation accepted for another run")
	}
	d = *rk.RunKey
	d.By = other.Public
	if err := d.Check(rk.Public); err == nil {
		t.Error("a delegation accepted from a key that didn't sign it")
	}
	// The runner key signs the statement, not something that could pass
	// for a chain entry.
	if !strings.HasPrefix(rk.RunKey.Statement(), "swim run key v1\n") {
		t.Errorf("statement: %q", rk.RunKey.Statement())
	}
}

func TestStorage(t *testing.T) {
	k := testKey(t, "plain")
	if s := Storage(k); s != InFile {
		t.Errorf("plain key: %s", s)
	}
	if out, err := exec.Command("ssh-keygen", "-q", "-p", "-N", "pw", "-P", "", "-f", k.Path).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if s := Storage(k); s != InLockedFile {
		t.Errorf("passphrase key: %s", s)
	}
	os.Remove(k.Path)
	pub, _ := LoadKey(k.Path + ".pub")
	if s := Storage(pub); s != InAgent {
		t.Errorf("no private key file: %s", s)
	}
	if s := Storage(&Key{Public: "sk-ssh-ed25519@openssh.com AAAA x"}); s != OnDevice {
		t.Errorf("security key: %s", s)
	}
}
