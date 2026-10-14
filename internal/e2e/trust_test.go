package e2e

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/approval"
)

// Claims in docs/trust-model.md that no other test covers.

// A guard whose approval socket can't be reached declines: the step is a
// dry run, never approved by default.
func TestGuardDeclinesOnSocketError(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "guarded", `if guard ALLOW_X "change x"; then run "x changed" true; fi`)
	c := r.cmd("bash", "lane.1.sh")
	c.Env = append(c.Env, approval.EnvVar+"="+filepath.Join(t.TempDir(), "no-such-socket"))
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("lane: %v\n%s", err, out)
	}
	contains(t, "agent1.log", r.log(1), "SKIP  change x (dry run: not approved: approval: ")
	if strings.Contains(r.log(1), "PASS  x changed") {
		t.Error("the guarded step ran without an approval")
	}
}
