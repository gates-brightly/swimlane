package lookup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `users:
  zach:
    keys: [ssh-ed25519 AAAAzach zach@laptop, sk-ssh-ed25519@openssh.com AAAAyubi zach@yubikey]
  alice:
    keys: [ssh-ed25519 AAAAalice]
  bob:
    keys: [ssh-ed25519 AAAAbob]
groups:
  platform-admin: [zach, alice]
  ops: [bob, platform-admin]
require:
  - guards: ["FIN_ALLOW_*"]
    group: platform-admin
    count: 2
  - group: ops
`

func write(t *testing.T, content string) string {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, FileName), []byte(content), 0o644)
	return root
}

func TestLookup(t *testing.T) {
	l, err := Load(write(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	if u, ok := l.Who("sk-ssh-ed25519@openssh.com AAAAyubi other-comment"); !ok || u != "zach" {
		t.Errorf("who: %q %v", u, ok)
	}
	if _, ok := l.Who("ssh-ed25519 AAAAnobody"); ok {
		t.Error("unknown key matched")
	}
	if got := strings.Join(l.Members("ops"), ","); got != "alice,bob,zach" {
		t.Errorf("nested members: %s", got)
	}
	if got := l.Describe("alice"); got != "alice (ops, platform-admin)" {
		t.Errorf("describe: %s", got)
	}
	r := l.RuleFor("FIN_ALLOW_DELETE")
	if r.Group != "platform-admin" || r.Count != 2 {
		t.Fatalf("rule: %+v", r)
	}
	if ok, _ := l.Satisfied(r, []string{"zach", "bob"}); ok {
		t.Error("bob isn't platform-admin")
	}
	if ok, _ := l.Satisfied(r, []string{"zach", "alice"}); !ok {
		t.Error("two platform-admins should satisfy it")
	}
	if r := l.RuleFor("OTHER"); r.Group != "ops" || r.Count != 1 {
		t.Errorf("fallback rule: %+v", r)
	}
	empty, _ := Load(t.TempDir())
	if r := empty.RuleFor("X"); r.Count != 1 || r.Group != "" || empty.Path != "" {
		t.Errorf("no lookup.yml: %+v", r)
	}
}

func TestLookupErrors(t *testing.T) {
	for _, bad := range []string{
		"users: {a: {keys: [k1 x]}}\ngroups: {a: [a]}",
		"users: {a: {keys: [k1 x]}}\ngroups: {g: [nobody]}",
		"users: {a: {keys: [k1 x]}}\ngroups: {g: [h], h: [g]}",
		"users: {a: {keys: [t k1]}, b: {keys: [t k1]}}",
		"users: {a: {keys: [t k1]}}\nrequire: [{group: missing}]",
	} {
		if _, err := Load(write(t, bad)); err == nil {
			t.Errorf("accepted:\n%s", bad)
		}
	}
}
