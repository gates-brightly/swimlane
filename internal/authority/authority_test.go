package authority

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/sigfile"
	"github.com/gates-brightly/swimlane/internal/sign"
)

// A guard whose lookup.yml rule says evidence: true can't be approved:
// Unapprovable says why, and Signed counts nothing for it. Other guards,
// and a lookup.yml that doesn't load (Signed says that), aren't affected.
func TestEvidenceRuleUnapprovable(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, lookup.FileName), []byte("require:\n  - guards: [ORDERS_*]\n    evidence: true\n"), 0o644)
	s := Take(root, []string{"ORDERS_APPLY=1", "OTHER=1"})
	why := s.Unapprovable("ORDERS_APPLY")
	if !strings.Contains(why, "lookup.yml rule for ORDERS_APPLY requires evidence-bound approvals, which swim no longer has: remove `evidence: true`") {
		t.Fatalf("unapprovable: %q", why)
	}
	if r := s.Signed(Ask{Root: root, Lane: 1, Flag: "ORDERS_APPLY", Script: "abc"}, time.Now()); r.OK || r.Why != why {
		t.Errorf("signed: %+v", r)
	}
	if s.Unapprovable("OTHER") != "" {
		t.Error("OTHER is unapprovable")
	}
	bad := &Snapshot{LookupErr: errors.New("lookup.yml: broken")}
	if bad.Unapprovable("ORDERS_APPLY") != "" || bad.Signed(Ask{Flag: "ORDERS_APPLY"}, time.Now()).Why != "lookup.yml: broken" {
		t.Error("a lookup.yml that doesn't load")
	}
}

// Guard decides as swim run does as it starts: a flag set in the
// environment approves; a two-signature rule is met by two signatures,
// not one; an evidence: true rule approves nothing, the flag included;
// approvals: flags never asks.
func TestGuard(t *testing.T) {
	if !sign.Available() {
		t.Skip("ssh-keygen not installed")
	}
	root := t.TempDir()
	keys := map[string]*sign.Key{}
	var users strings.Builder
	for _, name := range []string{"alice", "bob"} {
		p := filepath.Join(t.TempDir(), name)
		if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", name, "-f", p).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		k, err := sign.LoadKey(p)
		if err != nil {
			t.Fatal(err)
		}
		keys[name] = k
		fmt.Fprintf(&users, "  %s:\n    keys: [%q]\n", name, k.Public)
	}
	os.WriteFile(filepath.Join(root, lookup.FileName), []byte("users:\n"+users.String()+
		"groups:\n  dba: [alice, bob]\nrequire:\n  - guards: [DROP_*]\n    group: dba\n    count: 2\n  - guards: [PLAN_*]\n    evidence: true\n"), 0o644)
	sha := strings.Repeat("a", 64)
	f := &sigfile.File{Kind: sigfile.Script, Script: "lane.1.sh", SHA256: sha, Lane: 1, Approves: []string{"DROP_OK"},
		Path: sigfile.ScriptPath(root, 1, sha, "")}
	sign1 := func(name string) {
		if err := f.Add(keys[name], sigfile.Approve, name); err != nil {
			t.Fatal(err)
		}
		if err := f.Save(); err != nil {
			t.Fatal(err)
		}
	}
	ask := Ask{Root: root, Lane: 1, Flag: "DROP_OK", Desc: "drop table old_orders", Script: sha}
	now := time.Now()

	sign1("alice")
	g := Take(root, nil).Guard(ask, true, now)
	if g.Approved || !g.Asks || g.Why != "approval needs 2 signature(s) from dba, has 1" {
		t.Errorf("one of two: %+v", g)
	}
	if got := g.Text(); got != "not approved: approval needs 2 signature(s) from dba, has 1; swim run will ask (a dry run if no one answers)" {
		t.Errorf("one of two text: %q", got)
	}
	if g := Take(root, nil).Guard(ask, false, now); g.Asks || !strings.HasSuffix(g.Text(), "; a dry run (approvals: flags never ask)") {
		t.Errorf("approvals: flags: %+v %q", g, g.Text())
	}
	if g := Take(root, []string{"DROP_OK=1"}).Guard(ask, true, now); !g.Approved || g.Text() != "approved: DROP_OK=1" {
		t.Errorf("flag: %+v %q", g, g.Text())
	}

	sign1("bob")
	if g := Take(root, nil).Guard(ask, true, now); !g.Approved || g.Text() != "approved by alice (dba), bob (dba) (signed)" {
		t.Errorf("two of two: %+v %q", g, g.Text())
	}

	plan := Ask{Root: root, Lane: 1, Flag: "PLAN_APPLY", Script: sha}
	g = Take(root, []string{"PLAN_APPLY=1"}).Guard(plan, true, now)
	if g.Approved || !g.Never || g.Asks || !strings.HasPrefix(g.Text(), "never approved: lookup.yml rule for PLAN_APPLY requires evidence-bound approvals") {
		t.Errorf("evidence rule: %+v %q", g, g.Text())
	}
}
