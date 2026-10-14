package sigfile

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	f := &File{Kind: Script, Path: ScriptPath(root, 3, "abc123abc123abc123"), Script: "lane.3.sh", SHA256: "abc123abc123abc123", Lane: 3, Approves: []string{"FIN_ALLOW_DELETE", "OTHER"}}
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
	script := &File{Kind: Script, SHA256: "def456def456def456", Lane: 1, Approves: []string{"FIN_ALLOW_DROP"}, Path: ScriptPath(root, 1, "def456def456def456")}
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
	if !strings.HasSuffix(EvidencePath("/r", 1, base.SHA256, ev.Evidence, "ENV=prod,SVC=a"), "/lane.1.aaaaaaaaaaaa.eeeeeeeee.ENV-prod.SVC-a.signature") {
		t.Errorf("evidence path %s", EvidencePath("/r", 1, base.SHA256, ev.Evidence, "ENV=prod,SVC=a"))
	}
}
