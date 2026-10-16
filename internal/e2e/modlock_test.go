package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// .swim.lock pins the built-in modules the lanes use (swim modules lock):
// a swim whose built-in differs is refused with the fix, which way it
// differs deciding the fix; swim modules check and swim plan say so first.
func TestModuleLockPinsBuiltins(t *testing.T) {
	r := newRepo(t, "")
	fakeBin(t, r, "aws", `echo '{"Account": "123456789012"}'`+"\n")
	r.modScript(1, "target check", `# Modules: target@1
mod target aws --account 123456789012`)
	r.mustSwim("run", "1", "--plain") // writes the lock (syntaxes only)

	out, code := r.swim("modules", "check")
	if code != 0 {
		t.Fatalf("check before lock: %d %s", code, out)
	}
	contains(t, "check unpinned", out, "target", "lock -", "this swim 1.0.0", "asked target@1", "not pinned: swim modules lock pins it")
	contains(t, "modules lock", r.mustSwim("modules", "lock"), ".swim.lock: target 1.0.0 (new)")
	lockPath := filepath.Join(r.root, ".swim.lock")
	lock, _ := os.ReadFile(lockPath)
	contains(t, "lock file", string(lock), "modules:", "builtin:", "target:", "version: 1.0.0")
	contains(t, "modules lock again", r.mustSwim("modules", "lock"), "already pins")
	contains(t, "check pinned", r.mustSwim("modules", "check"), "lock 1.0.0", "this swim 1.0.0")

	pin := func(ver string) {
		data := regexp.MustCompile(`(?s)(target:\s*\n\s*version: )\S+(\s*\n\s*sha256: )\S+`).ReplaceAll(lock, []byte("${1}"+ver+"${2}"+strings.Repeat("0", 64)))
		os.WriteFile(lockPath, data, 0o644)
	}

	// The lock pins a newer target than this swim has: update swim.
	pin("1.5.0")
	out, code = r.swim("modules", "check")
	if code != 1 {
		t.Errorf("check with a newer pin: exit %d", code)
	}
	const newer = ".swim.lock pins built-in target 1.5.0, and this swim's is 1.0.0: update swim"
	contains(t, "check newer", out, newer)
	contains(t, "plan", r.mustSwim("plan", "1"), "blocked: "+newer)
	r.swim("run", "1", "--plain")
	if l := r.status().Get(1); l.State != "failed" || !strings.Contains(l.Reason, newer) {
		t.Errorf("run with a newer pin: %s %q", l.State, l.Reason)
	}
	contains(t, "log", r.log(1), "STOP  not started: blocked: "+newer)
	if out, err := r.cmd("bash", "lane.1.sh").CombinedOutput(); err == nil || !strings.Contains(string(out), newer) {
		t.Errorf("direct run with a newer pin: %v %s", err, out)
	}

	// An older one: this swim can move the repo to its own.
	pin("0.9.0")
	contains(t, "check older", must(r.swim("modules", "check")), "pins built-in target 0.9.0, and this swim's is 1.0.0: swim modules lock moves the repo to target 1.0.0")
	contains(t, "relock", r.mustSwim("modules", "lock"), ".swim.lock: target 0.9.0 -> 1.0.0")
	contains(t, "run after relock", must(r.swim("run", "1", "--plain")), "")
	if l := r.status().Get(1); l.State != "passed" {
		t.Errorf("run after relock: %s %q\n%s", l.State, l.Reason, r.log(1))
	}

	// A minimum the built-in doesn't meet.
	r.modScript(2, "needs a newer target", `# Modules: target@1.4
mod target aws --account 123456789012`)
	contains(t, "plan minimum", r.mustSwim("plan", "2"), "blocked: module target@1.4: this swim's built-in target is 1.0.0; the lane needs 1.4 or later within 1 (update swim, or ask for less)")
}
