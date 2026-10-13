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

func TestRemoteApproval(t *testing.T) {
	if !sign.Available() {
		t.Skip("ssh-keygen not installed")
	}
	root := t.TempDir()
	zach, alice, mallory := newKey(t, root, "zach"), newKey(t, root, "alice"), newKey(t, root, "mallory")
	os.WriteFile(filepath.Join(root, lookup.FileName), []byte(`users:
  zach: {keys: ["`+zach.Public+`"]}
  alice: {keys: ["`+alice.Public+`"]}
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
	f := &File{Path: filepath.Join(root, "lane.3.sh.signature"), Script: "lane.3.sh", SHA256: "abc123", Lane: 3, Job: "j", Approves: []string{"FIN_ALLOW_DELETE", "OTHER"}}
	f.Sign(zach, "zach")
	now := time.Now()
	if d := f.Decide(l, "FIN_ALLOW_DELETE", now); d.OK || !strings.Contains(d.Why, "needs 2 signature(s) from platform-admin, has 1") {
		t.Fatalf("one of two: %+v", d)
	}
	if d := f.Decide(l, "OTHER", now); !d.OK || strings.Join(d.Users, ",") != "zach" {
		t.Fatalf("default rule, one known signer: %+v", d)
	}
	f.Sign(mallory, "mallory") // valid signature, unknown key: doesn't count
	if d := f.Decide(l, "FIN_ALLOW_DELETE", now); d.OK || !strings.Contains(d.Why, "1 signer(s) not in lookup.yml") {
		t.Fatalf("unknown signer: %+v", d)
	}
	f.Sign(alice, "alice")
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	g, err := Load(f.Path)
	if err != nil {
		t.Fatal(err)
	}
	if d := g.Decide(l, "FIN_ALLOW_DELETE", now); !d.OK || strings.Join(d.Users, ",") != "alice,zach" {
		t.Fatalf("two platform-admins: %+v", d)
	}
	if d := g.Decide(l, "NOT_LISTED", now); d.OK {
		t.Error("approved a guard it doesn't name")
	}
	// Tampering: change what it approves, and every signature fails.
	g.Approves = append(g.Approves, "SNEAKY")
	if v, bad := g.Verify(l); len(v) != 0 || len(bad) != 3 {
		t.Errorf("edited statement: %d verified, %d bad", len(v), len(bad))
	}
	// Expiry.
	g, _ = Load(f.Path)
	g.Expires = now.Add(-time.Minute).UTC().Format(time.RFC3339)
	if d := g.Decide(l, "OTHER", now); d.OK || !strings.Contains(d.Why, "expired") {
		t.Errorf("expired: %+v", d)
	}
	if got := ForScript(root, "abc123"); len(got) != 1 {
		t.Errorf("ForScript: %d", len(got))
	}
}
