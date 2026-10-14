package trust

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/sign"
)

func newKey(t *testing.T, name string) *sign.Key {
	t.Helper()
	if !sign.Available() {
		t.Skip("ssh-keygen not installed")
	}
	p := filepath.Join(t.TempDir(), name)
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", name, "-f", p).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	k, err := sign.LoadKey(p)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSetIgnoresOrderAndComments(t *testing.T) {
	a := []string{"ssh-ed25519 BBBB b@x", "ssh-ed25519 AAAA a@x", "ssh-ed25519 AAAA again"}
	b := []string{"ssh-ed25519 AAAA", "ssh-ed25519 BBBB other"}
	if SHA(a) != SHA(b) {
		t.Errorf("same keys, different hash: %v %v", Set(a), Set(b))
	}
	if SHA(a) == SHA([]string{"ssh-ed25519 AAAA"}) {
		t.Error("a key dropped, same hash")
	}
}

// A trust.yml edit needs a change signed by a pinned key: an unsigned one
// keeps the pinned keys, a signed one moves the pin along.
func TestTrustPinTransitions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	alice, bob, mallory := newKey(t, "alice"), newKey(t, "bob"), newKey(t, "mallory")
	f := &File{Keys: []string{alice.Public}}
	if err := f.Save(root); err != nil {
		t.Fatal(err)
	}
	f, _ = Load(root)
	pin, _ := LoadPin(root)
	if r := Check(root, f, pin); r.Status != TOFU || !r.Trusted(alice.Public) {
		t.Fatalf("first use: %+v", r)
	}
	if _, err := SavePin(root, f.Keys, nil); err != nil {
		t.Fatal(err)
	}
	pin, _ = LoadPin(root)
	if r := Check(root, f, pin); r.Status != Match {
		t.Fatalf("pinned: %+v", r)
	}

	// Unsigned: mallory added by editing the file.
	edited := &File{Keys: []string{alice.Public, mallory.Public}}
	edited.Save(root)
	r := Check(root, edited, pin)
	if r.Status != Unsigned || r.Trusted(mallory.Public) || !r.Trusted(alice.Public) {
		t.Fatalf("unsigned edit: %+v", r)
	}

	// Signed by a key the pinned set doesn't trust: refused, and a forged
	// record doesn't count either.
	if err := Sign(root, f.Keys, edited.Keys, mallory); err == nil {
		t.Fatal("a change signed by an untrusted key was recorded")
	}
	forged := Transition{Prev: pin.SHA, SHA: edited.SHA(), Keys: Set(edited.Keys), Signer: mallory.Public}
	forged.Sig, _ = sign.Sign(mallory, forged.payload())
	os.WriteFile(filepath.Join(root, SigName), mustYAML(t, []Transition{forged}), 0o644)
	if r := Check(root, edited, pin); r.Status != Unsigned {
		t.Fatalf("forged change: %+v", r)
	}
	os.Remove(filepath.Join(root, SigName))

	// Signed by alice: bob joins.
	next := &File{Keys: []string{alice.Public, bob.Public}}
	if err := Sign(root, f.Keys, next.Keys, alice); err != nil {
		t.Fatal(err)
	}
	next.Save(root)
	r = Check(root, next, pin)
	if r.Status != Advanced || r.Steps != 1 || !r.Trusted(bob.Public) {
		t.Fatalf("signed change: %+v", r)
	}
	// An explicit pin with only the hash walks too (the first change
	// recorded the pinned set's keys).
	if r := Check(root, next, &Pin{SHA: pin.SHA}); r.Status != Advanced {
		t.Fatalf("hash-only pin: %+v", r)
	}

	// Then bob removes alice: two steps from the pin, and alice still
	// verifies what she signed before.
	last := &File{Keys: []string{bob.Public}}
	if err := Sign(root, next.Keys, last.Keys, bob); err != nil {
		t.Fatal(err)
	}
	last.Save(root)
	r = Check(root, last, pin)
	if r.Status != Advanced || r.Steps != 2 || !r.Trusted(alice.Public) {
		t.Fatalf("two changes: %+v", r)
	}
	if _, err := SavePin(root, last.Keys, r.KeyList()); err != nil {
		t.Fatal(err)
	}
	pin, _ = LoadPin(root)
	if r := Check(root, last, pin); r.Status != Match || !r.Trusted(alice.Public) || r.Trusted(mallory.Public) {
		t.Fatalf("moved pin: %+v (retired %v)", r, pin.Retired)
	}
}

func mustYAML(t *testing.T, ts []Transition) []byte {
	t.Helper()
	data, err := yaml.Marshal(ts)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
