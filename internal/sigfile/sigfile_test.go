package sigfile

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/sign"
)

func newKey(t *testing.T, dir, name string) *sign.Key {
	p := filepath.Join(dir, name)
	exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", name, "-f", p).Run()
	k, err := sign.LoadKey(p)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func setup(t *testing.T) (string, *lookup.Lookup, map[string]*sign.Key) {
	if !sign.Available() {
		t.Skip("ssh-keygen not installed")
	}
	root := t.TempDir()
	keys := map[string]*sign.Key{}
	for _, n := range []string{"zach", "alice", "bob", "mallory"} {
		keys[n] = newKey(t, root, n)
	}
	os.WriteFile(filepath.Join(root, lookup.FileName), []byte(`users:
  zach: {keys: ["`+keys["zach"].Public+`"]}
  alice: {keys: ["`+keys["alice"].Public+`"]}
  bob: {keys: ["`+keys["bob"].Public+`"]}
groups:
  platform-admin: [zach, alice]
require:
  - guards: [FIN_ALLOW_*]
    group: platform-admin
    count: 2
`), 0o644)
	l, err := lookup.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return root, l, keys
}

func TestApproveRevokeSequence(t *testing.T) {
	root, l, k := setup(t)
	f := &File{Kind: Script, Path: ScriptPath(root, 3, "abc123abc123abc123", ""), Script: "lane.3.sh", SHA256: "abc123abc123abc123", Lane: 3, Approves: []string{"FIN_ALLOW_DELETE", "OTHER"}}
	now := time.Now()
	f.Add(k["zach"], Approve, "zach")
	if d := Decide([]*File{f}, l, "FIN_ALLOW_DELETE", now); d.OK || !strings.Contains(d.Why, "needs 2 signature(s) from platform-admin, has 1") {
		t.Fatalf("one of two: %+v", d)
	}
	if d := Decide([]*File{f}, l, "OTHER", now); !d.OK {
		t.Fatalf("default rule: %+v", d)
	}
	f.Add(k["mallory"], Approve, "mallory")
	if d := Decide([]*File{f}, l, "FIN_ALLOW_DELETE", now); d.OK || !strings.Contains(d.Why, "1 signer(s) not in lookup.yml") {
		t.Fatalf("unknown signer: %+v", d)
	}
	f.Add(k["alice"], Approve, "alice")
	if d := Decide([]*File{f}, l, "FIN_ALLOW_DELETE", now); !d.OK || strings.Join(d.Users, ",") != "alice,zach" {
		t.Fatalf("two admins: %+v", d)
	}
	// Alice revokes: no longer enough. Then approves again: enough again.
	f.Add(k["alice"], Revoke, "alice")
	if d := Decide([]*File{f}, l, "FIN_ALLOW_DELETE", now); d.OK {
		t.Fatal("a revoked approval still counted")
	}
	f.Add(k["alice"], Approve, "alice")
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	g, _ := Load(f.Path)
	if d := Decide([]*File{g}, l, "FIN_ALLOW_DELETE", now); !d.OK {
		t.Fatalf("re-approved after a revoke: %+v", d)
	}

	// The order is attested: drop the revoke, and the chain breaks there.
	g.Events = append(g.Events[:3], g.Events[4:]...)
	signers, bad := g.Standing(l)
	if len(bad) != 1 || !strings.Contains(bad[0], "event 4 breaks the chain") || len(signers) != 3 {
		t.Fatalf("removed event: signers %d, %v", len(signers), bad)
	}
	// Changing the statement invalidates every event.
	g, _ = Load(f.Path)
	g.Approves = append(g.Approves, "SNEAKY")
	if s, bad := g.Standing(l); len(s) != 0 || len(bad) == 0 {
		t.Fatalf("edited statement: %d signers, %v", len(s), bad)
	}
}

func TestStandingCombinesWithScript(t *testing.T) {
	root, l, k := setup(t)
	standing := &File{Kind: Standing, For: "bob", All: true, Path: StandingPath(root, "bob")}
	standing.Add(k["zach"], Approve, "zach")
	script := &File{Kind: Script, SHA256: "def456def456def456", Lane: 1, Approves: []string{"FIN_ALLOW_DROP"}, Path: ScriptPath(root, 1, "def456def456def456", "")}
	script.Add(k["alice"], Approve, "alice")
	now := time.Now()
	if d := Decide([]*File{standing}, l, "FIN_ALLOW_DROP", now); d.OK {
		t.Fatal("one standing approval shouldn't meet 2 of platform-admin")
	}
	if d := Decide([]*File{standing, script}, l, "FIN_ALLOW_DROP", now); !d.OK || strings.Join(d.Users, ",") != "alice,zach" {
		t.Fatalf("standing + script: %+v", d)
	}
	if !strings.Contains(standing.Statement(), "for: bob") || standing.StatementHash() == script.StatementHash() {
		t.Error("statements")
	}
}

// An approval limited to a cell, or bound to evidence, says so in its
// statement; one that isn't reads exactly as before (old signatures stay
// valid).
func TestCellAndEvidenceStatements(t *testing.T) {
	base := &File{Kind: Script, Script: "lane.1.sh", SHA256: strings.Repeat("a", 64), Lane: 1, Job: "j", Round: "r", Approves: []string{"F"}}
	plain := base.Statement()
	if strings.Contains(plain, "cells:") || strings.Contains(plain, "evidence") {
		t.Errorf("plain statement changed:\n%s", plain)
	}
	cell := *base
	cell.Cells = []string{"orders"}
	ev := *base
	ev.Evidence = strings.Repeat("e", 64)
	if !strings.Contains(cell.Statement(), "cells: orders\n") || !strings.Contains(ev.Statement(), "evidence-sha256: eeee") {
		t.Errorf("statements:\n%s\n%s", cell.Statement(), ev.Statement())
	}
	if cell.StatementHash() == base.StatementHash() || ev.StatementHash() == base.StatementHash() {
		t.Error("a limited approval hashes like the plain one")
	}
	got := ForCell([]*File{base, &cell}, "billing")
	if len(got) != 1 || got[0] != base {
		t.Errorf("ForCell(billing) kept %v", got)
	}
	if len(ForCell([]*File{base, &cell}, "orders")) != 2 || len(ForCell([]*File{&cell}, "")) != 0 {
		t.Error("ForCell")
	}
	if !strings.HasSuffix(EvidencePath("/r", 1, base.SHA256, "", ev.Evidence, "ENV=prod,SVC=a"), "/lane.1.aaaaaaaaaaaa.eeeeeeeee.ENV-prod.SVC-a.signature") {
		t.Errorf("evidence path %s", EvidencePath("/r", 1, base.SHA256, "", ev.Evidence, "ENV=prod,SVC=a"))
	}
}

// State, in each of its five states, for script and standing approvals.
func TestState(t *testing.T) {
	root, l, k := setup(t)
	now := time.Now()
	sha := strings.Repeat("a", 64)
	script := func(approves ...string) *File {
		return &File{Kind: Script, SHA256: sha, Lane: 1, Approves: approves, Path: ScriptPath(root, 1, sha, "")}
	}
	standing := func(l *lookup.Lookup, approves ...string) *File {
		f := &File{Kind: Standing, For: "bob", Approves: approves, All: len(approves) == 0, Path: StandingPath(root, "bob")}
		f.BindRules(l)
		return f
	}
	// A lookup.yml like l's, with the FIN_ALLOW_* rule loosened.
	loose, _ := lookup.Load(root)
	loose.Require = []lookup.Rule{{Guards: []string{"FIN_ALLOW_*"}, Group: "platform-admin", Count: 1}}
	// zach's key removed from lookup.yml: his signatures stop counting at once.
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, lookup.FileName), []byte("users:\n  alice: {keys: [\""+k["alice"].Public+"\"]}\n"), 0o644)
	noKeys, err := lookup.Load(other)
	if err != nil {
		t.Fatal(err)
	}

	signed := func(f *File, who ...string) *File {
		for _, w := range who {
			f.Add(k[w], Approve, w)
		}
		return f
	}
	revoked := signed(script("OTHER"), "zach")
	revoked.Add(k["zach"], Revoke, "zach")
	expired := script("OTHER")
	expired.Expires = now.Add(-time.Hour).UTC().Format(time.RFC3339)
	signed(expired, "zach")
	legacy := signed(&File{Kind: Standing, For: "bob", All: true, Path: StandingPath(root, "bob")}, "zach")

	for _, c := range []struct {
		name  string
		f     *File
		l     *lookup.Lookup
		cur   Current
		state string
		why   string
	}{
		{"active script", signed(script("OTHER"), "zach"), l, Current{Script: sha}, Active, ""},
		{"no signatures", script("OTHER"), l, Current{}, Insufficient, "no signatures"},
		{"revoked", revoked, l, Current{}, Revoked, "every signer revoked"},
		{"expired", expired, l, Current{}, Expired, "expired "},
		{"script changed", signed(script("OTHER"), "zach"), l, Current{Script: strings.Repeat("b", 64)}, Stale, "the script changed since"},
		{"one of two", signed(script("FIN_ALLOW_DROP"), "zach"), l, Current{}, Insufficient, "FIN_ALLOW_DROP: approval needs 2 signature(s) from platform-admin, has 1"},
		{"unknown signer", signed(script("OTHER"), "mallory"), l, Current{}, Insufficient, "no signer is in lookup.yml"},
		{"key removed", signed(script("OTHER"), "zach"), noKeys, Current{}, Insufficient, "no signer is in lookup.yml"},
		{"standing active", signed(standing(l, "OTHER"), "zach"), l, Current{}, Active, ""},
		// A rule changing either way stales a standing approval for that
		// guard: here loosened, so one admin would now be enough, but the
		// approval was signed under the old rule.
		{"rule loosened", signed(standing(l, "FIN_ALLOW_DROP"), "zach"), loose, Current{}, Stale, "lookup.yml's rule for FIN_ALLOW_DROP changed since"},
		{"rule tightened", signed(standing(loose, "FIN_ALLOW_DROP"), "zach"), l, Current{}, Stale, "lookup.yml's rule for FIN_ALLOW_DROP changed since"},
		{"other rule changed", signed(standing(l), "zach"), loose, Current{Flag: "OTHER"}, Active, ""},
		{"all, rules changed", signed(standing(l), "zach"), loose, Current{}, Stale, "require: rules changed since"},
		{"legacy", legacy, l, Current{}, Active, ""},
	} {
		st := c.f.State(c.l, now, c.cur)
		if st.Name != c.state || !strings.Contains(st.Why, c.why) {
			t.Errorf("%s: %s, want %s (%s)", c.name, st, c.state, c.why)
		}
	}
	if w := legacy.State(l, now, Current{}).Warnings; len(w) != 2 || !strings.Contains(w[0], "never expires") || !strings.Contains(w[1], "bound lookup.yml's rules") {
		t.Errorf("legacy warnings: %q", w)
	}
	// Collect agrees: a standing approval stale for a guard doesn't count
	// there, even where the looser rule would now be met.
	if d := Decide([]*File{signed(standing(l, "FIN_ALLOW_DROP"), "zach")}, loose, "FIN_ALLOW_DROP", now); d.OK || !strings.Contains(d.Why, "stale: lookup.yml's rule for FIN_ALLOW_DROP changed") {
		t.Errorf("loosened rule counted an old approval: %+v", d)
	}
}

// Once lookup.yml sets standing.max, a standing approval that never
// expires, or lasts longer, stops counting.
func TestStandingMax(t *testing.T) {
	root, l, k := setup(t)
	now := time.Now()
	capped := *l
	capped.Standing.Max, capped.MaxStanding = "7d", 7*24*time.Hour
	mk := func(life time.Duration) *File {
		f := &File{Kind: Standing, For: "bob", Approves: []string{"OTHER"}, Path: StandingPath(root, "bob")}
		f.BindRules(l)
		if life > 0 {
			f.Expires = now.Add(life).UTC().Format(time.RFC3339)
		}
		f.Add(k["zach"], Approve, "zach")
		return f
	}
	never, long, short := mk(0), mk(30*24*time.Hour), mk(7*24*time.Hour)
	if st := never.State(l, now, Current{}); st.Name != Active || len(st.Warnings) != 1 {
		t.Errorf("never, no max: %s %q", st, st.Warnings)
	}
	if st := never.State(&capped, now, Current{}); st.Name != Insufficient || !strings.Contains(st.Why, "never expires") {
		t.Errorf("never, max 7d: %s", st)
	}
	if st := long.State(&capped, now, Current{}); st.Name != Insufficient || !strings.Contains(st.Why, "lasts longer than lookup.yml's standing.max (7d)") {
		t.Errorf("30d, max 7d: %s", st)
	}
	if st := short.State(&capped, now, Current{}); st.Name != Active || len(st.Warnings) != 0 {
		t.Errorf("7d, max 7d: %s %q", st, st.Warnings)
	}
	if d := Decide([]*File{long}, &capped, "OTHER", now); d.OK {
		t.Error("an approval outlasting standing.max counted")
	}
}

// A standing approval bound to lookup.yml's rules says so in its
// statement; one from before reads exactly as before.
func TestStandingStatement(t *testing.T) {
	old := &File{Kind: Standing, For: "bob", All: true}
	if old.Statement() != "swim standing approval v1\nfor: bob\napproves: all\nexpires: never\n" {
		t.Errorf("legacy statement changed:\n%s", old.Statement())
	}
	bound := *old
	bound.Require = &[]lookup.Rule{}
	if !strings.Contains(bound.Statement(), "rules-sha256: ") || bound.StatementHash() == old.StatementHash() {
		t.Errorf("bound statement:\n%s", bound.Statement())
	}
	other := *old
	other.Require = &[]lookup.Rule{{Group: "ops"}}
	if other.StatementHash() == bound.StatementHash() {
		t.Error("different rules, same statement")
	}
}

// A signed answer through the hub counts only for the exact question the
// worker asked: any field changed (request, worker, lane, run, script,
// manifest, evidence, flag, decision, expiry) breaks it, as does a key
// lookup.yml doesn't list.
func TestVerifyHubAnswer(t *testing.T) {
	_, l, k := setup(t)
	q := approval.HubQuestion{Request: "q1", Worker: "w1", Lane: 3, Job: "j1", Run: "r-1", Script: "s1", Manifest: "m1", Evidence: "e1", Flag: "FIN_ALLOW_DROP", Expires: "2026-10-14T12:00:00Z"}
	st := q.Statement(approval.Approve)
	sig, err := sign.Sign(k["alice"], []byte(st))
	if err != nil {
		t.Fatal(err)
	}
	if u, err := VerifyHubAnswer(l, st, k["alice"].Public, sig); err != nil || u != "alice" {
		t.Fatalf("the signed question: %q %v", u, err)
	}
	changes := map[string]func(q *approval.HubQuestion){
		"request": func(q *approval.HubQuestion) { q.Request = "q2" }, "worker": func(q *approval.HubQuestion) { q.Worker = "w2" },
		"lane": func(q *approval.HubQuestion) { q.Lane = 4 }, "run": func(q *approval.HubQuestion) { q.Run = "r-2" },
		"script": func(q *approval.HubQuestion) { q.Script = "s2" }, "manifest": func(q *approval.HubQuestion) { q.Manifest = "m2" },
		"evidence": func(q *approval.HubQuestion) { q.Evidence = "e2" }, "flag": func(q *approval.HubQuestion) { q.Flag = "FIN_ALLOW_ALL" },
		"expires": func(q *approval.HubQuestion) { q.Expires = "2027-01-01T00:00:00Z" },
	}
	for name, change := range changes {
		q2 := q
		change(&q2)
		if _, err := VerifyHubAnswer(l, q2.Statement(approval.Approve), k["alice"].Public, sig); err == nil || !strings.Contains(err.Error(), "doesn't match this question") {
			t.Errorf("%s changed: %v", name, err)
		}
	}
	if _, err := VerifyHubAnswer(l, q.Statement(approval.Decline), k["alice"].Public, sig); err == nil {
		t.Error("an approval passed as a decline")
	}
	// Signed by a key lookup.yml doesn't list, or claimed for another key.
	msig, _ := sign.Sign(k["mallory"], []byte(st))
	if _, err := VerifyHubAnswer(l, st, k["mallory"].Public, msig); err == nil || !strings.Contains(err.Error(), "isn't in lookup.yml") {
		t.Errorf("mallory: %v", err)
	}
	if _, err := VerifyHubAnswer(l, st, k["zach"].Public, sig); err == nil {
		t.Error("alice's signature passed as zach's")
	}
	if _, err := VerifyHubAnswer(&lookup.Lookup{}, st, k["alice"].Public, sig); err == nil || !strings.Contains(err.Error(), "no lookup.yml") {
		t.Errorf("no lookup.yml: %v", err)
	}
}
