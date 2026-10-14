package secrets

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newKey makes a throwaway key (never the developer's own).
func newKey(t *testing.T, passphrase string) *Key {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not installed")
	}
	p := filepath.Join(t.TempDir(), "id_ed25519")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", passphrase, "-f", p).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v %s", err, out)
	}
	k, err := LoadKey(p)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func noEnv(string) string { return "" }

func TestSetGetScopes(t *testing.T) {
	k := newKey(t, "")
	path := filepath.Join(t.TempDir(), "secrets.yml")
	f, _ := Load(path)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.Set(k, "", "token", "all-scope-value"))
	must(f.Set(k, "", "shared", "from-all-scope"))
	must(f.Set(k, "/repo/a", "token", "repo-a-value"))
	must(f.Save(path))

	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("store mode %v, want 0600", fi.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	for _, plain := range []string{"all-scope-value", "repo-a-value"} {
		if strings.Contains(string(data), plain) {
			t.Fatalf("the store holds %q in the clear:\n%s", plain, data)
		}
	}

	f, err := Load(path)
	must(err)
	id, err := k.Identity(nil)
	must(err)
	a := Open(f, "/repo/a", id, noEnv)
	if s, err := a.Get("token"); err != nil || s.Value != "repo-a-value" || s.Scope != ScopeRepo {
		t.Errorf("repo a token: %+v %v", s, err)
	}
	if s, err := a.Get("shared"); err != nil || s.Value != "from-all-scope" || s.Scope != ScopeAll {
		t.Errorf("repo a shared: %+v %v", s, err)
	}
	b := Open(f, "/repo/b", id, noEnv)
	if s, _ := b.Get("token"); s.Value != "all-scope-value" {
		t.Errorf("repo b sees %q, want the all scope's", s.Value)
	}
	if _, err := b.Get("missing"); err == nil || !strings.Contains(err.Error(), "swim secret set missing") {
		t.Errorf("missing: %v", err)
	}
	if got := len(a.All()); got != 2 {
		t.Errorf("repo a resolves %d secrets, want 2", got)
	}

	ls := f.List("/repo/a")
	if len(ls) != 3 || ls[0].Name != "shared" || ls[1].Scope != ScopeRepo || !ls[2].Shadowed {
		t.Errorf("list: %+v", ls)
	}
	if !f.Has("/repo/b", "token") || f.Has("/repo/b", "nope") || f.Empty("/repo/b") {
		t.Error("Has/Empty")
	}
	if !f.Remove("/repo/a", "token") || f.Remove("/repo/a", "token") {
		t.Error("remove")
	}
	if _, ok := f.Repos["/repo/a"]; ok {
		t.Error("an empty repo scope was kept")
	}
	if err := f.Set(k, "", "bad-name", "x"); err == nil {
		t.Error("accepted a bad name")
	}
}

func TestOtherKeyAndEnv(t *testing.T) {
	mine, theirs := newKey(t, ""), newKey(t, "")
	f := &File{Schema: Schema}
	f.Set(theirs, "", "theirs", "their-value-123")
	id, _ := mine.Identity(nil)
	v := Open(f, "", id, noEnv)
	if _, err := v.Get("theirs"); err == nil || !strings.Contains(err.Error(), theirs.Fingerprint) {
		t.Errorf("another key's secret: %v", err)
	}
	// SWIM_SECRET_<NAME> wins, with or without a key (CI).
	env := func(k string) string {
		if k == "SWIM_SECRET_THEIRS" {
			return "from-ci-env"
		}
		return ""
	}
	for _, v := range []*Vault{Open(f, "", id, env), Open(f, "", nil, env)} {
		if s, err := v.Get("theirs"); err != nil || s.Value != "from-ci-env" || s.Scope != "env" {
			t.Errorf("env stand-in: %+v %v", s, err)
		}
	}
}

func TestPassphraseKey(t *testing.T) {
	k := newKey(t, "correct horse")
	f := &File{Schema: Schema}
	if err := f.Set(k, "", "x", "protected-value"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Identity(nil); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("no prompt: %v", err)
	}
	id, err := k.Identity(func() ([]byte, error) { return []byte("correct horse"), nil })
	if err != nil {
		t.Fatal(err)
	}
	if s, err := Open(f, "", id, noEnv).Get("x"); err != nil || s.Value != "protected-value" {
		t.Errorf("get: %+v %v", s, err)
	}
	bad, _ := k.Identity(func() ([]byte, error) { return []byte("wrong"), nil })
	if _, err := Open(f, "", bad, noEnv).Get("x"); err == nil {
		t.Error("decrypted with the wrong passphrase")
	}
}

func TestExpand(t *testing.T) {
	get := func(n string) (string, error) {
		if n == "db_url" {
			return "postgres://u:p@h/db", nil
		}
		return "", errors.New("no secret " + n)
	}
	got, err := Expand([]string{"psql", "_swim.db_url", "-c", "x=_swim.db_url;"}, get)
	if err != nil || got[1] != "postgres://u:p@h/db" || got[3] != "x=postgres://u:p@h/db;" {
		t.Errorf("expand: %q %v", got, err)
	}
	if _, err := Expand([]string{"echo", "_swim.nope"}, get); err == nil {
		t.Error("an unknown secret expanded")
	}
	if got, _ := Expand([]string{"no placeholders"}, get); got[0] != "no placeholders" {
		t.Errorf("untouched: %q", got)
	}
}
