package lookup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
		"users: {a: {keys: [t k1]}}\nstanding: {max: forever}",
	} {
		if _, err := Load(write(t, bad)); err == nil {
			t.Errorf("accepted:\n%s", bad)
		}
	}
}

// A rule's canonical form ignores glob order and spelled-out defaults, and
// changes with anything it requires.
func TestRuleCanonical(t *testing.T) {
	a := Rule{Guards: []string{"B_*", "A_*"}, Group: "ops", Count: 0}
	b := Rule{Guards: []string{"A_*", "B_*"}, Group: "ops", Count: 1}
	if a.Canonical() != b.Canonical() {
		t.Errorf("same rule: %q vs %q", a.Canonical(), b.Canonical())
	}
	for _, other := range []Rule{
		{Guards: []string{"A_*", "B_*"}, Group: "ops", Count: 2},
		{Guards: []string{"A_*", "B_*"}, Group: "admin"},
		{Guards: []string{"A_*", "B_*"}, Group: "ops", Evidence: true},
		{Guards: []string{"A_*"}, Group: "ops"},
	} {
		if other.Canonical() == a.Canonical() {
			t.Errorf("%+v reads like %+v", other, a)
		}
	}
	rules := []Rule{{Guards: []string{"FIN_*"}, Count: 2}, {Group: "ops"}}
	if MatchRule(rules, "FIN_X").Count != 2 || MatchRule(rules, "OTHER").Group != "ops" || MatchRule(nil, "X").Count != 1 {
		t.Error("MatchRule")
	}
	if RulesText(rules) == RulesText(rules[1:]) || RulesText(nil) != "" {
		t.Error("RulesText")
	}
}

func TestStandingMax(t *testing.T) {
	l, err := Load(write(t, "users: {a: {keys: [t k1]}}\nstanding: {max: 90d}\n"))
	if err != nil || l.MaxStanding != 90*24*time.Hour {
		t.Fatalf("max: %v %v", l.MaxStanding, err)
	}
	for in, want := range map[string]time.Duration{"30d": 30 * 24 * time.Hour, "36h": 36 * time.Hour, "90m": 90 * time.Minute} {
		if d, err := ParseLifetime(in); err != nil || d != want {
			t.Errorf("%s: %v %v", in, d, err)
		}
	}
	for _, bad := range []string{"", "0d", "-1d", "xd", "soon", "0s"} {
		if _, err := ParseLifetime(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
