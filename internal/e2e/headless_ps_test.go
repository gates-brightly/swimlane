package e2e

import (
	"fmt"
	"strings"
	"testing"
)

// swim ps lists the headless workers on this machine, in any directory;
// swim stop stops this directory's, swim stop --all every one.
func TestRunnersPsStop(t *testing.T) {
	long(t)
	a, _ := headlessRepo(t)
	b, _ := headlessRepo(t)
	// One machine, one user: both share a config directory.
	for _, kv := range a.env {
		if strings.HasPrefix(kv, "XDG_CONFIG_HOME=") {
			b.env = append(b.env, kv)
		}
	}
	b.mustSwim("auth", "status") // logged in under the shared config

	if out := a.mustSwim("ps"); !strings.Contains(out, "no headless swimmers running") {
		t.Errorf("ps with none:\n%s", out)
	}
	a.mustSwim("watch", "--headless", "--detach", "--interval", "200ms")
	b.mustSwim("watch", "--headless", "--detach", "--interval", "200ms")
	pa, pb := headlessPid(a), headlessPid(b)
	out := a.mustSwim("ps")
	contains(t, "ps", out, "PID", "detached", fmt.Sprint(pa), fmt.Sprint(pb), a.root, b.root, "idle")
	contains(t, "ps --yaml", a.mustSwim("ps", "--yaml"), "schema: swim.ps/v1", "mode: detached")

	// swim stop: this directory's only.
	contains(t, "stop", b.mustSwim("stop"), "stopped")
	if processAlive(pb) || !processAlive(pa) {
		t.Fatalf("stop in b: a alive %v, b alive %v", processAlive(pa), processAlive(pb))
	}
	if out := a.mustSwim("ps"); strings.Contains(out, b.root) || !strings.Contains(out, a.root) {
		t.Errorf("ps after stopping b:\n%s", out)
	}

	// --all, from anywhere.
	b.mustSwim("watch", "--headless", "--detach", "--interval", "200ms")
	pb = headlessPid(b)
	out = b.mustSwim("stop", "--all")
	contains(t, "stop --all", out, a.root+": stopped", b.root+": stopped")
	if processAlive(pa) || processAlive(pb) {
		t.Errorf("still running after stop --all")
	}
	contains(t, "stop again", a.mustSwim("stop"), "no headless swimmer running")
	if out, code := a.swim("stop", "now"); code != 2 {
		t.Errorf("stop with an argument outside a lane: exit %d\n%s", code, out)
	}
}
