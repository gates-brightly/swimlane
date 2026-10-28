package lookup

import (
	"crypto/sha256"
	"fmt"
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
	// The signed form, golden: a rule without evidence: true reads (and so
	// hashes) as it always did; one with it still adds ";evidence".
	if got := a.Canonical(); got != "guards=A_*,B_*;group=ops;count=1" {
		t.Errorf("canonical %q", got)
	}
	if got := (Rule{Guards: []string{"A_*"}, Evidence: true}).Canonical(); got != "guards=A_*;group=;count=1;evidence" {
		t.Errorf("canonical with evidence %q", got)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(RulesText([]Rule{a, {Group: "ops", Count: 2}})))); got != "9806b87012931bbaa88129668ed4b379d24e8d37c9f347b5525d585087284235" {
		t.Errorf("rules hash %s", got)
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

// A rule with evidence: true approves nothing now: EvidenceWhy names the
// guards it covers, and only those.
func TestEvidenceWhy(t *testing.T) {
	l, err := Load(write(t, "require:\n  - guards: [ORDERS_*]\n    evidence: true\n  - count: 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := "lookup.yml rule for ORDERS_APPLY requires evidence-bound approvals, which swim no longer has: remove `evidence: true`"
	if got := l.EvidenceWhy("ORDERS_APPLY"); got != want {
		t.Errorf("ORDERS_APPLY: %q", got)
	}
	if got := l.EvidenceWhy("FIN_DROP"); got != "" {
		t.Errorf("FIN_DROP: %q", got)
	}
	if (*Lookup)(nil).EvidenceWhy("X") != "" {
		t.Error("nil lookup")
	}
}

// AddKey creates lookup.yml, adds keys to new and existing users, keeps
// comments, and refuses a key another user has.
func TestAddKey(t *testing.T) {
	root := t.TempDir()
	a, b := "ssh-ed25519 AAAAa zach@laptop", "ssh-ed25519 AAAAb zach@yubikey"
	if created, err := AddKey(root, "zach", a); err != nil || !created {
		t.Fatalf("create: %v %v", created, err)
	}
	path := filepath.Join(root, FileName)
	data, _ := os.ReadFile(path)
	os.WriteFile(path, append([]byte("# ours\n"), data...), 0o644)
	if _, err := AddKey(root, "zach", b); err != nil {
		t.Fatal(err)
	}
	if _, err := AddKey(root, "alice", a); err == nil {
		t.Error("gave zach's key to alice")
	}
	l, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if who, _ := l.Who(b); who != "zach" || len(l.Users["zach"].Keys) != 2 {
		t.Errorf("users = %+v", l.Users)
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "# ours") {
		t.Errorf("comment lost:\n%s", data)
	}
}
