package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/launcher"
)

// A lane runs the bytes staged before it waited: edits to its script and
// to a file it sources, made while it waits for the lane before it, change
// nothing in the round, and the chain names the original script.
func TestManifestStagedBytesRun(t *testing.T) {
	r := newRepo(t, "")
	r.script(2, "goes first", `run "hold" bash -c 'until [ -e "$SWIM_ROOT/release" ]; do sleep 0.05; done'`)
	os.MkdirAll(filepath.Join(r.root, "lib"), 0o755)
	os.WriteFile(filepath.Join(r.root, "lib", "greet.sh"), []byte("greeting=greet-v1\n"), 0o644)
	r.script(3, "waits for 2", `source lib/greet.sh
run "say" echo "script-v1 $greeting"`)
	withHeader(t, r, 3, "# After: 2")
	data, _ := lane.ScriptBytes(r.root, 3)
	orig := chain.Hash(data)

	l := r.launch(t, nil)
	waitFor(t, func() bool { l := r.status().Get(2); return l != nil && l.CurrentStep == "hold" })
	waitFor(t, func() bool { l := r.status().Get(3); return l != nil && l.State == "waiting" })
	r.script(3, "waits for 2", `source lib/greet.sh
run "say" echo "script-v2 $greeting"`)
	withHeader(t, r, 3, "# After: 2")
	os.WriteFile(filepath.Join(r.root, "lib", "greet.sh"), []byte("greeting=greet-v2\n"), 0o644)
	os.WriteFile(filepath.Join(r.root, "release"), nil, 0o644)
	if code := l.wait(t); code != 0 {
		t.Fatalf("exit %d\n%s", code, l.out.String())
	}
	log := r.log(3)
	contains(t, "agent3.log", log, "script-v1 greet-v1", "manifest: ")
	if strings.Contains(log, "v2") {
		t.Errorf("the edits reached the waiting lane:\n%s", log)
	}
	run := r.status().Get(3).Run
	if _, err := os.Stat(filepath.Join(r.root, ".swim", "runs", run, "3", "lib", "greet.sh")); err != nil {
		t.Errorf("no staged include: %v", err)
	}
	links, _ := chain.Read(r.root)
	found := false
	for _, k := range links {
		if k.Type == "round" && k.Lane == 3 {
			found = true
			if k.Script != orig || k.Manifest == "" {
				t.Errorf("round entry names script %s manifest %q, want the original %s", k.Script, k.Manifest, orig)
			}
		}
	}
	if !found {
		t.Error("no round entry for swim 3")
	}
	contains(t, "verify", r.mustSwim("verify", "--levels", "--allow-unsigned"), "consistent  yes")
}

// An edit to the working-tree script while it runs changes nothing: bash
// reads the staged copy.
func TestManifestMidRunEdit(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "long", `run "first" echo one-v1
run "wait" bash -c 'until [ -e "$SWIM_ROOT/go1" ]; do sleep 0.05; done'
run "second" echo two-v1`)
	l := r.launch(t, nil)
	waitFor(t, func() bool { l := r.status().Get(1); return l != nil && l.CurrentStep == "wait" })
	p := filepath.Join(r.root, "lane.1.sh")
	data, _ := os.ReadFile(p)
	// In place, same length: what a running bash would read next.
	os.WriteFile(p, []byte(strings.ReplaceAll(string(data), "-v1", "-v2")), 0o755)
	os.WriteFile(filepath.Join(r.root, "go1"), nil, 0o644)
	if code := l.wait(t); code != 0 {
		t.Fatalf("exit %d\n%s", code, l.out.String())
	}
	contains(t, "agent1.log", r.log(1), "two-v1")
	if strings.Contains(r.log(1), "two-v2") {
		t.Error("bash ran the edited working-tree script")
	}
}

// A queued lane whose included file changed since it was queued is
// skipped: a file it declares (# Includes:) is part of its script's hash.
func TestManifestQueuePin(t *testing.T) {
	r := newRepo(t, "")
	os.MkdirAll(filepath.Join(r.root, "lib"), 0o755)
	os.WriteFile(filepath.Join(r.root, "lib", "x.sh"), []byte("x=1\n"), 0o644)
	r.script(1, "sources x", `source lib/x.sh
run "x" echo "x=$x"`)
	r.mustSwim("queue", "add", "1")
	os.WriteFile(filepath.Join(r.root, "lib", "x.sh"), []byte("x=2\n"), 0o644)
	r.swim("watch", "--once")
	if st := r.status().Get(1); st.State != "skipped" || !strings.Contains(st.Reason, "lane.1.sh changed since it was queued") {
		t.Errorf("swim 1 after its include changed: %s %q", st.State, st.Reason)
	}
}

// A signed approval binds the manifest: changing a sourced file, or the
// policy part of swim.yml, stales it, and swim says why.
func TestManifestApprovalBinding(t *testing.T) {
	r := newRepo(t, "")
	keys := people(t, r, "require:\n  - guards: [FIN_ALLOW_*]\n    count: 1\n", "zach")
	os.MkdirAll(filepath.Join(r.root, "lib"), 0o755)
	os.WriteFile(filepath.Join(r.root, "lib", "tables.sh"), []byte("table=old_orders\n"), 0o644)
	r.script(1, "drop old table", `source lib/tables.sh
if guard FIN_ALLOW_DROP "drop table $table"; then run "drop" true; fi`)
	withHeader(t, r, 1, "# Guards:  FIN_ALLOW_DROP drop the old table")
	contains(t, "approve", r.mustSwim("approve", "1", "--guards", "FIN_ALLOW_DROP", "--key", keys["zach"]),
		"approving manifest", "includes lib/tables.sh", "(includes)", "approved: FIN_ALLOW_DROP")
	r.mustSwim("run", "1")
	contains(t, "approved", r.log(1), "APPROVED  drop table old_orders (approved by ", "PASS  drop")

	os.WriteFile(filepath.Join(r.root, "lib", "tables.sh"), []byte("table=orders\n"), 0o644)
	r.mustDryRun("run", "1")
	// The file is declared (# Includes:), so it's part of the script's hash:
	// the approval is of another version of the script.
	contains(t, "stale include", r.log(1), "SKIP  drop table orders (dry run: ", "approves another version of lane.1.sh")
	contains(t, "approve --list", r.mustSwim("approve"), "stale: the script changed since")

	// Approve the new manifest; then change only swim.yml's policy.
	os.WriteFile(filepath.Join(r.root, "lib", "tables.sh"), []byte("table=old_orders\n"), 0o644)
	r.mustSwim("run", "1")
	if strings.Count(r.log(1), "APPROVED") != 2 {
		t.Fatalf("restoring the file should restore the approval:\n%s", r.log(1))
	}
	os.WriteFile(filepath.Join(r.root, "swim.yml"), []byte("blocked_commands: [\"terraform destroy\"]\n"), 0o644)
	r.mustDryRun("run", "1")
	contains(t, "stale policy", r.log(1), "stale: config policy changed (swim.yml: blocked_commands")
	contains(t, "verify", r.mustSwim("verify", "--levels", "--allow-unsigned"), "stale: config policy changed")
	// A key outside the policy list (max_parallel) changes nothing.
	os.WriteFile(filepath.Join(r.root, "swim.yml"), []byte("max_parallel: 3\n"), 0o644)
	r.mustSwim("run", "1")
	if strings.Count(r.log(1), "APPROVED") != 3 {
		t.Errorf("a non-policy swim.yml key staled the approval:\n%s", r.log(1))
	}
}

// The broker, not the request, says who asks: a lane that asks as another
// lane is refused (a hidden command for another lane, T10), an undeclared
// flag is refused, and the approval is recorded as the lane's own.
func TestManifestRequestsFromTheLane(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "asks as another", `if "$SWIM_BIN" _approve 5 DROP "harmless cleanup"; then run "dropped as 5" true; fi
if "$SWIM_BIN" _approve 1 DROP "harmless cleanup"; then run "dropped" true; fi
if "$SWIM_BIN" _approve 1 WIPE_ALL "routine tidy"; then run "wiped" true; fi`)
	withHeader(t, r, 1, "# Guards:  DROP drop the table")
	out, code := r.swim("run", "1", "-y")
	if code != launcher.ExitDryRun { // WIPE_ALL dry-runs
		t.Fatalf("exit %d\n%s", code, out)
	}
	contains(t, "output", out, "refused: lane 1 can't report for lane 5", "approval refused for swim 1: guard WIPE_ALL isn't declared in swim 1's script")
	if strings.Contains(r.log(1), "dropped as 5") {
		t.Error("lane 1 was approved as lane 5")
	}
	log := r.log(1)
	contains(t, "agent1.log", log, "APPROVED  harmless cleanup (approved by swim run -y", "PASS  dropped",
		"SKIP  routine tidy (dry run: not approved: guard WIPE_ALL isn't declared")
	if strings.Contains(log, "PASS  wiped") {
		t.Error("an undeclared flag was approved")
	}
	if _, err := os.Stat(filepath.Join(r.root, ".swim", "logs", "agent5.log")); err == nil {
		t.Error("the approval went to the lane the request named")
	}
	contains(t, ".swim.log", r.mustSwim("log"), "refused: guard WIPE_ALL isn't declared")
	links, _ := chain.Read(r.root)
	for _, k := range links {
		if k.Type == "approve" && k.Guard == "DROP" && (k.Lane != 1 || k.Manifest == "") {
			t.Errorf("approve entry for swim %d, manifest %q", k.Lane, k.Manifest)
		}
	}
}

// An approval is used once, by the lane it was given to: replayed through
// _mark by another lane it is refused.
func TestManifestResponseReuse(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	r := newRepo(t, "")
	// Lane 1 asks the broker itself and keeps the response's id.
	r.script(1, "gets an approval", `run "ask" python3 -c '
import json, os, socket
s = socket.socket(socket.AF_UNIX); s.connect(os.environ["SWIM_APPROVAL"])
s.sendall(json.dumps({"lane": 1, "flag": "DROP", "desc": "x", "token": os.environ["SWIM_APPROVAL_TOKEN"]}).encode() + b"\n")
resp = json.loads(s.makefile().readline())
open(os.environ["SWIM_ROOT"] + "/approval-id", "w").write(resp["id"])'
"$SWIM_BIN" _mark 1 APPROVED "drop" "approved" --request "$(cat "$SWIM_ROOT/approval-id")" --guard DROP && echo first-use-ok
"$SWIM_BIN" _mark 1 APPROVED "drop" "approved" --request "$(cat "$SWIM_ROOT/approval-id")" --guard DROP || echo second-use-refused`)
	withHeader(t, r, 1, "# Guards:  DROP drop the table")
	r.script(2, "replays it", `if "$SWIM_BIN" _mark 2 APPROVED "drop" "approved at the prompt" --request "$(cat "$SWIM_ROOT/approval-id")" --guard DROP; then run "replayed" true; fi`)
	withHeader(t, r, 2, "# Guards:  DROP drop the table")
	withHeader(t, r, 2, "# After: 1")
	out, _ := r.swim("run", "1", "2", "-y")
	contains(t, "output", out, "first-use-ok", "second-use-refused", "the approval was already used", "the approval was given to another lane")
	if strings.Contains(r.log(2), "APPROVED") || strings.Contains(r.log(2), "PASS  replayed") {
		t.Errorf("swim 2 used swim 1's approval:\n%s", r.log(2))
	}
}

// An Observe: fact that changes between approval and the guarded step
// ends the round as drift.
func TestManifestObservedDrift(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "observed", `if guard DROP "drop the table"; then run "dropped" true; fi
run "after" true`)
	withHeader(t, r, 1, `# Observe: tick = n=$(cat tick 2>/dev/null || echo 0); echo $((n+1)) > tick; echo $n`)
	out, _ := r.swim("run", "1", "-y")
	log := r.log(1)
	contains(t, "agent1.log", log, "DRIFT  drift: tick was 0 at approval, now 1", "STOP  drift: observed facts changed since approval (DROP)")
	if strings.Contains(log, "PASS  dropped") || strings.Contains(log, "PASS  after") {
		t.Errorf("the round went on after drift:\n%s\n%s", log, out)
	}
}

// An approval binds the runner the repo names for the lane: the same
// script, run by another runner, is another thing to approve (T2).
func TestManifestRunnerBinding(t *testing.T) {
	r := newRepo(t, "")
	keys := people(t, r, "require:\n  - guards: [FIN_ALLOW_*]\n    count: 1\n", "zach")
	dir := t.TempDir()
	for _, name := range []string{"pyrun", "pyrun2"} {
		os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexec bash \"$@\"\n"), 0o755)
	}
	r.env = append(r.env, "PATH="+filepath.Dir(bin)+":"+dir+":/usr/bin:/bin")
	r.write("swim.yml", "runners:\n  py: pyrun\n", 0o644)
	r.write("lane.1.py", `# Round: drop the old table
# Guards:  FIN_ALLOW_DROP drop the old table
if swim guard FIN_ALLOW_DROP "drop the old table"; then swim run "drop" -- true; fi
`, 0o644)
	r.mustSwim("approve", "1", "--guards", "FIN_ALLOW_DROP", "--key", keys["zach"])
	r.mustSwim("run", "1", "--plain")
	contains(t, "approved", r.log(1), "APPROVED  drop the old table (approved by ", "PASS  drop")

	r.write("swim.yml", "runners:\n  py: pyrun2\n", 0o644)
	r.mustDryRun("run", "1", "--plain")
	contains(t, "stale runner", r.log(1), "SKIP  drop the old table (dry run: ", "runner changed (swim.yml pyrun, now swim.yml pyrun2)")
	if strings.Count(r.log(1), "PASS  drop") != 1 {
		t.Errorf("the guarded step ran under another runner:\n%s", r.log(1))
	}
}
