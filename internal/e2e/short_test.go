package e2e

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// shortLane writes lane n in the short form: #!/usr/bin/env swim, its
// header and its steps. swim run loads the library and calls lane_init.
func shortLane(t *testing.T, r *repo, n int, src string) {
	t.Helper()
	r.write("lane."+strconv.Itoa(n)+".sh", "#!/usr/bin/env swim\n# swim: syntax 4\n"+src, 0o755)
}

// A short-form lane runs as an explicit one does: steps and stages logged,
// a guard dry-runs until approved, FLAG=1 approves it, and the round ends
// without summary.
func TestShortFormLane(t *testing.T) {
	r := newRepo(t)
	shortLane(t, r, 1, `# Round: Say hello
# Guards: HELLO_ALLOW_WRITE  write hello.txt
stage check
step "say hello" echo "hello from swim"
stage change
if guard HELLO_ALLOW_WRITE "write hello.txt"; then
  step "write it" bash -c 'echo hi > "$SWIM_ROOT/hello.txt"'
fi
`)
	contains(t, "lint", r.mustSwim("lint", "1"), "no problems found")

	r.mustDryRun("run", "1", "--plain", "--no-prompt")
	contains(t, "dry run", r.log(1), "PASS  say hello", "SKIP  write hello.txt (dry run", "== END SKIP")
	if _, err := os.Stat(filepath.Join(r.root, "hello.txt")); err == nil {
		t.Fatal("the dry run wrote hello.txt")
	}

	r.mustSwimEnv([]string{"HELLO_ALLOW_WRITE=1"}, "run", "1", "--plain")
	contains(t, "approved", r.log(1), "APPROVED  write hello.txt (HELLO_ALLOW_WRITE=1", "PASS  write it", "== END PASS")
	if got, _ := os.ReadFile(filepath.Join(r.root, "hello.txt")); string(got) != "hi\n" {
		t.Errorf("hello.txt = %q", got)
	}
	runs, _ := filepath.Glob(filepath.Join(r.root, ".swim", "runs", "*", "1.manifest.yml"))
	if len(runs) == 0 {
		t.Fatal("no staged manifest")
	}
	if m, _ := os.ReadFile(runs[0]); !strings.Contains(string(m), "runner: bash (short form)") {
		t.Errorf("manifest doesn't pin the short form's runner:\n%s", m)
	}
}

// A short-form lane whose last step fails ends with that failure alone:
// the prelude's own source of the script isn't a command outside a step.
// A command outside a step that fails is still recorded.
func TestShortFormLastStepFails(t *testing.T) {
	r := newRepo(t)
	shortLane(t, r, 1, "# Round: fails last\nstage check\nstep \"fails\" false\n")
	if _, code := r.swim("run", "1", "--plain", "--no-prompt"); code == 0 {
		t.Fatal("the lane passed")
	}
	if strings.Contains(r.log(1), "outside a step") {
		t.Errorf("the failed last step also warned outside a step:\n%s", r.log(1))
	}
	shortLane(t, r, 2, "# Round: stray\nstage check\ncd /nowhere-at-all\nstep \"ok\" true\n")
	r.swim("run", "2", "--plain", "--no-prompt")
	contains(t, "stray command", r.log(2), "WARN  outside a step: cd /nowhere-at-all")
}

// ./lane.N.sh runs the lane through its #! line, as swim run N does, from
// any directory; a failed gate ends the round, as in an explicit lane.
func TestShortFormShebang(t *testing.T) {
	r := newRepo(t)
	shortLane(t, r, 2, `# Round: Gate
stage check
gate "app.conf is there" test -f "$SWIM_ROOT/app.conf"
step "never reached" echo reached
`)
	c := r.cmd(filepath.Join(r.root, "lane.2.sh"), "--plain")
	c.Dir = t.TempDir()
	out, err := c.CombinedOutput()
	if err == nil {
		t.Errorf("a failed gate passed:\n%s", out)
	}
	log := r.log(2)
	contains(t, "gate", log, "FAIL  app.conf is there", "== END FAIL")
	if strings.Contains(log, "PASS  never reached") {
		t.Errorf("the round went on after its gate failed:\n%s", log)
	}

	// Queued and run by a swimmer, like any lane.
	r.write("app.conf", "x\n", 0o644)
	r.mustSwim("queue", "2")
	r.swim("watch", "--once")
	contains(t, "queued", r.log(2), "PASS  app.conf is there", "PASS  never reached", "== END PASS")
}
