package launcher

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/sign"
)

// The launcher signs a lane's round with the run key, under its own facts,
// once per lane process; it signs nothing else a lane sends.
func TestLaneEntry(t *testing.T) {
	if !sign.Available() {
		t.Skip("ssh-keygen not installed")
	}
	p := filepath.Join(t.TempDir(), "runner")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", p).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	long, err := sign.LoadKey(p)
	if err != nil {
		t.Fatal(err)
	}
	rk, err := sign.NewRunKey(long, "r1", "host")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	a := &runAuthority{recorded: map[string]bool{}}
	o := &Options{Root: root, Cfg: &config.Config{Root: root}, RunID: "r1", auth: a, runKey: rk}
	c := approval.Caller{Lane: 2, Job: "deploy", Run: "r1", Manifest: "m2", Token: "t2"}

	for _, typ := range []string{"approve", "run", "grant", "secret-released"} {
		if _, err := a.laneEntry(o, c, chain.Entry{Type: typ, Guard: "F"}); err == nil {
			t.Errorf("a lane had a %s entry signed", typ)
		}
	}
	exit := 0
	if _, err := a.laneEntry(o, c, chain.Entry{Type: "round", Lane: 1, Job: "other", Run: "r0", Manifest: "m1", Result: "passed", Exit: &exit,
		Guard: "F", Method: "prompt", Approvers: []chain.Approver{{Name: "mallory"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.laneEntry(o, c, chain.Entry{Type: "round", Result: "failed"}); err == nil || !strings.Contains(err.Error(), "already recorded") {
		t.Errorf("a second round from one process: %v", err)
	}
	links, err := chain.Read(root)
	if err != nil || len(links) != 1 {
		t.Fatalf("chain: %v %+v", err, links)
	}
	l := links[0]
	if l.Lane != 2 || l.Job != "deploy" || l.Run != "r1" || l.Manifest != "m2" || l.Result != "passed" {
		t.Errorf("the launcher's facts weren't used: %+v", l.Entry)
	}
	if l.Guard != "" || l.Method != "" || len(l.Approvers) != 0 {
		t.Errorf("a round entry carried approval fields: %+v", l.Entry)
	}
	if l.Signer != rk.Public || l.SignedBy() != long.Public {
		t.Errorf("signed by %q (vouched for by %q)", l.Signer, l.SignedBy())
	}
	if problems, unsigned := chain.Check(links); len(problems) != 0 || len(unsigned) != 0 {
		t.Errorf("check: %+v %v", problems, unsigned)
	}
	if bad := chain.Trust(links, func(pub string) bool { return sign.KeyOnly(pub) == sign.KeyOnly(long.Public) }); len(bad) != 0 {
		t.Errorf("the runner key doesn't vouch for its run key: %+v", bad)
	}
}
