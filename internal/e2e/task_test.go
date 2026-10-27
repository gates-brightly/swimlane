package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A lane named by # Task: is found by its name everywhere a lane number
// or job id goes: swim new NAME writes it in the lowest free lane, and run,
// approve, log, status and history take the name. The lane number stays
// its identity: signatures and the chain read as for any lane.
func TestNamedTask(t *testing.T) {
	r := newRepo(t, "")
	keys := people(t, r, "", "alice")
	r.script(1, "something else", `run "x" true`)

	out := r.mustSwim("new", "renew-certs", "Renew the certs")
	contains(t, "new", out, "wrote lane.2.sh (task renew-certs, swim 2)", "swim run renew-certs")
	data, _ := os.ReadFile(filepath.Join(r.root, "lane.2.sh"))
	contains(t, "header", string(data), "# Task:    renew-certs")
	r.script(2, "Renew the certs", "# Task: renew-certs\n"+`stage change
if guard CERT_ALLOW "renew"; then run "renew" true; fi`)

	contains(t, "dry run", r.mustDryRun("run", "renew-certs", "--plain", "--no-prompt"), "SKIP  renew")
	r.mustSwim("approve", "renew-certs", "--guards", "CERT_ALLOW", "--key", keys["alice"])
	contains(t, "approved", r.mustSwim("renew-certs", "--plain", "--no-prompt"), "APPROVED  renew")
	contains(t, "log", r.mustSwim("log", "renew-certs"), "APPROVED  renew")
	contains(t, "status", r.mustSwim("status"), "[renew-certs]")
	contains(t, "history", r.mustSwim("history", "renew-certs", "--oneline"), "round swim 2 passed")
	contains(t, "verify", r.mustSwim("verify", "--levels", "--allow-unsigned"), "consistent  yes")
	// The approval is the lane's, as any is.
	files, _ := filepath.Glob(filepath.Join(r.root, "approvals", "lane.2.*.signature"))
	if len(files) != 1 {
		t.Errorf("approvals: %v", files)
	}

	// Names swim can't use are refused, by swim new and by lint.
	for name, want := range map[string]string{"status": "is a swim command", "abc123": "nor a task name"} {
		if out, code := r.swim("new", name, "x"); code == 0 || !strings.Contains(out, want) {
			t.Errorf("swim new %s: exit %d\n%s", name, code, out)
		}
	}
	if out, code := r.swim("new", "renew-certs", "again"); code == 0 || !strings.Contains(out, "is lane 2 already") {
		t.Errorf("swim new renew-certs again: exit %d\n%s", code, out)
	}
	r.script(3, "a twin", "# Task: renew-certs\n"+`run "x" true`)
	out, _ = r.swim("lint")
	if !regexp.MustCompile(`# Task: renew-certs is swim \d's name too`).MatchString(out) {
		t.Errorf("lint, two lanes one name:\n%s", out)
	}
	if out, code := r.swim("run", "renew-certs", "--plain"); code == 0 || !strings.Contains(out, "ambiguous") {
		t.Errorf("run an ambiguous name: exit %d\n%s", code, out)
	}
	r.script(3, "a command", "# Task: status\n"+`run "x" true`)
	contains(t, "lint command", must(r.swim("lint", "3")), "# Task: status is a swim command")
	if out, code := r.swim("run", "nope", "--plain"); code == 0 || !strings.Contains(out, "tasks: renew-certs") {
		t.Errorf("an unknown name: exit %d\n%s", code, out)
	}
}

// # After: takes a task name as it takes a lane number.
func TestTaskAfter(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "build", "# Task: build\n"+`run "build" true`)
	r.script(2, "ship", "# After: build\n"+`run "ship" true`)
	contains(t, "lint", must(r.swim("lint")), "no problems found")
	// Lane 2 waits on the lane named build, as on swim 1.
	contains(t, "alone", must(r.swim("run", "2", "--plain")), "swim 1's pending round has not passed")
	contains(t, "both", r.mustSwim("run", "build", "2", "--plain"), "swim 1", "swim 2", "PASS")
	r.script(2, "ship", "# After: nope\n"+`run "ship" true`)
	contains(t, "lint unknown", must(r.swim("lint", "2")), "no lane is task nope")
}
