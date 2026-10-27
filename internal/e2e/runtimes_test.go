package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Lane scripts in any language: lane.N.<ext> (or a binary lane.N) runs
// inside swim's wrapper; in-lane steps are swim commands.

func (r *repo) write(name, content string, mode os.FileMode) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.root, name), []byte(content), mode); err != nil {
		r.t.Fatal(err)
	}
}

// fakeRunner puts a command named name on the repo's PATH (in front of a
// minimal system PATH, so no real tsx/node/bun is found) that announces
// itself (and runs nothing).
func fakeRunner(t *testing.T, r *repo, name string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	os.WriteFile(path, []byte("#!/bin/sh\necho \""+name+" runs $(basename \"$1\")\"\n"), 0o755)
	r.env = append(r.env, "PATH="+filepath.Dir(bin)+":"+dir+":/usr/bin:/bin")
	return path
}

// A lane that never loads the bash library runs inside the wrapper: its
// swim run/gate/guard/stage calls are steps, and a failed gate ends the
// program even though it carries on.
func TestWrappedLaneInLaneCommands(t *testing.T) {
	r := newRepo(t, "")
	r.write("lane.1.sh", `#!/usr/bin/env bash
# Round: plain bash, swim commands
swim stage check
swim run "first" -- echo one
if swim guard ALLOW_X "do x"; then swim run "x" -- echo did-x; fi
swim drift "config differs"
swim gate "must pass" -- false
touch "$SWIM_ROOT/after-gate"
`, 0o755)
	r.env = append(r.env, "ALLOW_X=1")
	out, code := r.swim("run", "1", "--plain")
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(r.root, "after-gate")); err == nil {
		t.Error("the program kept running after its gate failed")
	}
	log := r.log(1)
	contains(t, "log", log, "script: lane.1.sh", "-- stage check", "PASS  first", "APPROVED  do x (ALLOW_X=1)", "PASS  x",
		"DRIFT  config differs", "FAIL  must pass (exit 1)", "STOP  gate failed: must pass", "== END FAIL")
	if l := r.status().Get(1); l.State != "failed" || l.Pass != 2 || l.Fail != 1 || l.Drift != 1 || l.Script != "lane.1.sh" {
		t.Errorf("status: %+v", l)
	}
	// Outside a lane, the in-lane commands say how to use them.
	out, code = r.swim("gate", "x", "--", "true")
	if code == 0 || !strings.Contains(out, "inside a lane's round") {
		t.Errorf("gate outside a lane: exit %d\n%s", code, out)
	}
}

// Once a gate fails no later step runs, even in a program that outlives
// its stop: this one ignores SIGTERM, so it reaches its next swim run
// before the wrapper's SIGKILL, every time. That step is refused, not run,
// and the log shows the gate's FAIL and STOP and nothing after.
func TestWrappedLaneNoStepAfterEnd(t *testing.T) {
	long(t)
	r := newRepo(t, "")
	r.write("lane.1.sh", `#!/usr/bin/env bash
# Round: ignores its stop
trap '' TERM
swim run "first" -- true
swim gate "must pass" -- false
swim run "never" -- touch "$SWIM_ROOT/ran-never"
touch "$SWIM_ROOT/after-refusal"
`, 0o755)
	out, code := r.swim("run", "1", "--plain")
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(r.root, "ran-never")); err == nil {
		t.Error("a step ran after the round's gate failed")
	}
	if _, err := os.Stat(filepath.Join(r.root, "after-refusal")); err == nil {
		t.Error("the refused step returned to the program before it was stopped")
	}
	contains(t, "output", out, `swim run: the round has ended; "never" not run`)
	log := r.log(1)
	contains(t, "log", log, "PASS  first", "FAIL  must pass (exit 1)", "STOP  gate failed: must pass", "== END FAIL")
	if strings.Contains(log, "never") {
		t.Errorf("the refused step is in the log:\n%s", log)
	}
}

// A program that never calls swim run is one step: its whole output, with
// secrets masked, passed or failed by its exit code.
func TestWrappedLaneWholeRun(t *testing.T) {
	r := newRepo(t, "")
	r.write("lane.2.sh", "#!/usr/bin/env bash\n# Round: no swim calls\necho \"hello $SWIM_LANE\"\necho \"token=$DEPLOY_TOKEN\"\nexit 3\n", 0o755)
	r.env = append(r.env, "DEPLOY_TOKEN=s3cr3t-value-123")
	out, code := r.swim("run", "2", "--plain")
	if code != 1 || strings.Contains(out, "migrated") {
		t.Fatalf("exit %d, want 1 (and a plain program is never migrated):\n%s", code, out)
	}
	log := r.log(2)
	contains(t, "log", log, "FAIL  lane.2.sh (exit 3)", "$ bash ./lane.2.sh", "| hello 2", "| token=***", "== END FAIL")
	if strings.Contains(log, "s3cr3t-value-123") {
		t.Errorf("secret in the log:\n%s", log)
	}
}

// A compiled lane has no header: its sidecar lane.N.yml holds the round
// and the rest.
func TestBinaryLaneWithSidecar(t *testing.T) {
	r := newRepo(t, "")
	// A program built here, not a copy of a system one: macOS kills a copied
	// system binary at launch (exit 137); Go signs what it builds.
	src := filepath.Join(t.TempDir(), "main.go")
	os.WriteFile(src, []byte("package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"compiled lane ran\") }\n"), 0o644)
	build := exec.Command("go", "build", "-o", filepath.Join(r.root, "lane.3"), src)
	build.Env = append(os.Environ(), "GOFLAGS=-buildvcs=false")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the lane: %v %s", err, out)
	}
	out, _ := r.swim("status")
	contains(t, "status before sidecar", out, "swim 3")
	r.write("lane.3.yml", "round: compiled lane\nguards:\n  SHIP_IT: ship the build (2026-10-13)\ntimeout: 5m\nlocks: [deploy]\n", 0o644)
	r.mustSwim("run", "3", "--plain")
	contains(t, "log", r.log(3), "compiled lane", "script: lane.3", "timeout: 5m", "guards: SHIP_IT", "PASS  lane.3")
	// A sidecar key swim doesn't know is an error, not ignored.
	r.write("lane.3.yml", "round: x\nrounds: typo\n", 0o644)
	out, code := r.swim("run", "3", "--plain")
	if code == 0 || !strings.Contains(out, "lane.3.yml") {
		t.Errorf("bad sidecar: exit %d\n%s", code, out)
	}
}

// Without a #! line or a runner, a lane fails at start with the exact
// fix; swim.yml's runners: (command names) covers everyone; a #! line
// needs nothing.
func TestRunnerResolution(t *testing.T) {
	r := newRepo(t, "")
	fakeRunner(t, r, "fakets")
	r.write("lane.1.ts", "// Round: typescript lane\nconsole.log('hi')\n", 0o644)
	out, code := r.swim("run", "1", "--plain")
	if code == 0 {
		t.Fatalf("ran without a runner:\n%s", out)
	}
	contains(t, "no runner", out, "lane.1.ts: no runner for .ts lanes", "none of tsx, ts-node, bun, deno is on PATH",
		"#!/usr/bin/env tsx", "swim config runners.ts", "runners:", "ts: tsx")

	r.write("swim.yml", "runners:\n  ts: fakets\n", 0o644)
	out = r.mustSwim("run", "1", "--plain")
	contains(t, "swim.yml runner", out, "fakets runs lane.1.ts")
	contains(t, "log", r.log(1), "PASS  lane.1.ts", "$ fakets ./lane.1.ts")

	// The operator's own runner (swim config runners.EXT) wins over swim.yml.
	other := fakeRunner(t, r, "otherts")
	r.mustSwim("config", "runners.ts", other)
	if got := strings.TrimSpace(r.mustSwim("config", "runners.ts")); got != other {
		t.Errorf("config runners.ts = %q, want %q", got, other)
	}
	contains(t, "personal runner", r.mustSwim("run", "1", "--plain"), "otherts runs lane.1.ts")

	// A #! line wins, with no runner configured for its extension.
	r.write("lane.2.py", "#!/bin/sh\n# Round: shebang lane\necho \"from sh: $0\"\n", 0o644)
	contains(t, "shebang", r.mustSwim("run", "2", "--plain"), "from sh:")
}

// A lane has exactly one script.
func TestTwoScriptsConflict(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "bash version", `run "x" true`)
	r.write("lane.1.js", "// Round: js version\n", 0o644)
	out, code := r.swim("run", "1", "--plain")
	if code == 0 || !strings.Contains(out, "swim 1 has 2 scripts (lane.1.sh, lane.1.js)") {
		t.Errorf("exit %d\n%s", code, out)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// The first Ctrl-C stops a wrapped lane at its next step, as it does a
// bash lane: the running step finishes, the next is refused, the round
// ends interrupted, and the program doesn't carry on.
func TestWrappedLaneGracefulStop(t *testing.T) {
	r := newRepo(t, "")
	r.write("lane.1.sh", `#!/usr/bin/env bash
# Round: interrupt me
for i in 1 2 3; do swim run "step $i" -- `+hold("go")+`; done
touch "$SWIM_ROOT/carried-on"
`, 0o755)
	l := r.launch(t, nil)
	r.waitStep(t, 1, "step 1")
	l.signal(syscall.SIGINT)
	waitFor(t, func() bool { _, err := os.Stat(filepath.Join(r.root, ".swim", "lane1.stop")); return err == nil })
	r.release("go")
	l.wait(t)
	log := r.log(1)
	contains(t, "log", log, "PASS  step 1", "STOP  interrupted by operator (after: step 1)", "== END INTERRUPTED")
	if strings.Contains(log, "step 2 ") {
		t.Errorf("step 2 ran:\n%s", log)
	}
	if _, err := os.Stat(filepath.Join(r.root, "carried-on")); err == nil {
		t.Error("the program carried on after the stop")
	}
}

// The git shim stops a wrapped lane too: git push from anywhere in the
// program is refused and the round ends.
func TestWrappedLaneGitShim(t *testing.T) {
	r := newRepo(t, "")
	// (Spelled so swim run's scan of the script doesn't refuse it first.)
	r.write("lane.1.sh", "#!/usr/bin/env bash\n# Round: pushes\nsub=push\ngit $sub origin main\nsleep 2\ntouch \"$SWIM_ROOT/carried-on\"\n", 0o755)
	out, code := r.swim("run", "1", "--plain")
	if code == 0 {
		t.Fatalf("exit 0:\n%s", out)
	}
	contains(t, "log", r.log(1), "BLOCKED  git push", "STOP  blocked command: git push/commit/pull")
	if _, err := os.Stat(filepath.Join(r.root, "carried-on")); err == nil {
		t.Error("the program carried on after git push was blocked")
	}
}
