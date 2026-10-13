package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// handlerScript writes a syntax 3 lane.N.sh with header extras, handler
// functions and a body.
func handlerScript(t *testing.T, r *repo, n int, header, handlers, body string) {
	t.Helper()
	s := fmt.Sprintf("#!/usr/bin/env bash\n# swim: syntax 3\n# Round: handlers %d\n%s_swim_lib=$(\"${SWIM_BIN:-swim}\" lib) || exit 1; eval \"$_swim_lib\"\nlane_init %d\n%s\n%s\nsummary\n", n, header, n, handlers, body)
	r.write(fmt.Sprintf("lane.%d.sh", n), s, 0o755)
}

// Every handler records its outcome; the env tells it how the round ended.
const recordingHandlers = `on_success() { run "on_success ran" bash -c 'echo "$SWIM_OUTCOME" > outcome.txt'; }
on_failure() { run "on_failure ran" bash -c 'echo "$SWIM_OUTCOME|$SWIM_STOP_REASON|$SWIM_FAILED_STEP|$SWIM_FAILED_STAGE" > outcome.txt'; }
on_exit() { run "on_exit ran" true; }`

func readFile(t *testing.T, r *repo, name string) string {
	t.Helper()
	data, _ := os.ReadFile(filepath.Join(r.root, name))
	return strings.TrimSpace(string(data))
}

// Each way a round can end calls the right handlers in order, with the
// right SWIM_OUTCOME; handler results never change the round's.
func TestHandlerExitPaths(t *testing.T) {
	for _, tc := range []struct {
		name, header, body, outcome string
		handler                     string // which of on_success / on_failure ran
		end                         string
	}{
		{"pass", "", `run "ok" true`, "pass", "on_success", "== END PASS"},
		{"soft failure", "", `stage check
run "probe" false
run "more" true`, "fail||probe|check", "on_failure", "== END FAIL"},
		{"gate", "", `gate "must" false`, "stopped|gate failed: must|must|setup", "on_failure", "== END FAIL"},
		{"stop", "", `stop "no go"`, "stopped|no go||", "on_failure", "== END FAIL"},
		{"hard failure", "# Fail: hard\n", `run "first" false
run "never" true`, "stopped|hard failure: first|first|setup", "on_failure", "== END FAIL"},
		{"bash error exit", "", `exit 3`, "fail|||", "on_failure", "== END FAIL"},
		{"timeout", "# Timeout: 1s\n", `run "slow" sleep 5`, "timeout|timeout: the round's Timeout (1s) ran out|slow|setup", "on_failure", "== END FAIL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRepo(t, "")
			handlerScript(t, r, 1, tc.header, recordingHandlers, tc.body)
			r.swim("run", "1", "--plain")
			log := r.log(1)
			if got := readFile(t, r, "outcome.txt"); got != tc.outcome {
				t.Errorf("outcome %q, want %q\n%s", got, tc.outcome, log)
			}
			contains(t, "log", log, "-- handler "+tc.handler, "PASS  "+tc.handler+" ran", "-- handler on_exit", "PASS  on_exit ran",
				tc.end, "handlers: "+tc.handler+" PASS | on_exit PASS")
			if strings.Index(log, "-- handler on_exit") < strings.Index(log, "-- handler "+tc.handler) {
				t.Error("on_exit ran before " + tc.handler)
			}
			if tc.name == "hard failure" && strings.Contains(log, "never") {
				t.Error("a hard round carried on after a failed step")
			}
			if l := r.status().Get(1); len(l.Handlers) != 2 {
				t.Errorf("status handlers %v", l.Handlers)
			}
		})
	}
}

// Handlers run soft: a failed gate or stop inside one doesn't stop it, the
// round's result is unchanged, and a failed rollback exits 89.
func TestHandlersRunSoft(t *testing.T) {
	r := newRepo(t, "")
	handlerScript(t, r, 1, "", `on_failure() {
  reached change || return 0
  run "rollback 1" false
  gate "rollback gate" false
  stop "rollback stop"
  run "rollback 2" true
}`, `stage check
run "check" true
stage change
gate "change" false`)
	r.swim("run", "1", "--plain")
	log := r.log(1)
	contains(t, "log", log, "FAIL  rollback 1", "STOP  gate failed: rollback gate", "STOP  rollback stop", "PASS  rollback 2",
		"== END FAIL  pass=1 fail=1 skip=0 drift=0  exit=89", "handlers: on_failure FAIL",
		"failed: FAIL  change (exit 1); STOP  gate failed: change")
	if l := r.status().Get(1); l.ExitCode == nil || *l.ExitCode != 89 || l.State != "failed" {
		t.Errorf("status: %+v", l)
	}
	chain, _ := os.ReadFile(filepath.Join(r.root, ".swim", "chain.jsonl"))
	contains(t, "chain", string(chain), `"handlers":{"on_failure":"FAIL"}`)

	// reached: a failure before the change stage skips the rollback.
	handlerScript(t, r, 1, "", `on_failure() {
  reached change || return 0
  run "rollback" true
}`, `stage check
gate "precheck" false`)
	r.swim("run", "1", "--plain")
	log = r.log(1)
	if last := log[strings.LastIndex(log, "== ROUND"):]; strings.Contains(last, "PASS  rollback") || !strings.Contains(last, "exit=1 ") {
		t.Errorf("rolled back before any change:\n%s", log)
	}
}

// The handlers share a budget that starts when the round ends: past it, the
// running step is stopped and the rest skipped.
func TestHandlerTimeout(t *testing.T) {
	r := newRepo(t, "")
	handlerScript(t, r, 1, "# Timeout: 1s\n# Handler-Timeout: 2s\n", `on_failure() {
  run "rollback" sleep 1.5
  run "slow cleanup" sleep 10
  run "never" true
}`, `run "slow" sleep 5`)
	start := time.Now()
	r.swim("run", "1", "--plain")
	if el := time.Since(start); el > 8*time.Second {
		t.Errorf("took %s", el)
	}
	log := r.log(1)
	contains(t, "log", log, "PASS  rollback", "FAIL  slow cleanup (timeout", "SKIP  never (not run: the handlers' budget")
}

// The first Ctrl-C stops the round at the step boundary and runs its
// handlers, whose steps start despite the stop request.
func TestHandlersAfterInterrupt(t *testing.T) {
	r := newRepo(t, "")
	handlerScript(t, r, 1, "", recordingHandlers, `run "first" sleep 1
run "second" true`)
	l := r.launch(t, nil)
	r.waitStep(t, 1, "first")
	l.signal(syscall.SIGINT)
	l.wait(t, 15*time.Second)
	log := r.log(1)
	contains(t, "log", log, "STOP  interrupted by operator (after: first)", "PASS  on_failure ran", "PASS  on_exit ran", "== END INTERRUPTED")
	if got := readFile(t, r, "outcome.txt"); !strings.HasPrefix(got, "interrupted|") {
		t.Errorf("outcome %q", got)
	}
}

// Lint: handlers need syntax 3, near-miss names are errors, and an
// on_failure with a guard should check reached. An older swim refuses a
// newer script syntax; plan lists handlers and their guards.
func TestHandlerLintAndPlan(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "syntax 2 with a handler", `on_failure() { run "x" true; }
run "y" true`)
	contains(t, "lint", must(r.swim("lint", "1")), "uses outcome handlers (on_failure) but declares syntax 2", "[handler]")
	r.script(1, "typo", `on_fail() { run "x" true; }
run "y" true`)
	contains(t, "lint", must(r.swim("lint", "1")), "function on_fail is not an outcome handler", "[handler]")
	handlerScript(t, r, 1, "", `on_failure() {
  if guard ALLOW_UNDO "undo it"; then run "undo" true; fi
}`, `run "y" true`)
	out := must(r.swim("lint", "1"))
	contains(t, "lint", out, "on_failure uses guard ALLOW_UNDO but never checks `reached`", "[handler-reached]")
	contains(t, "plan", r.mustSwim("plan", "1"), "handler on_failure  (guards: ALLOW_UNDO)")
	contains(t, "plan --yaml", r.mustSwim("plan", "1", "--yaml"), "name: on_failure", "guards: [ALLOW_UNDO]")

	r.write("lane.1.sh", "#!/usr/bin/env bash\n# swim: syntax 4\n# Round: future\n", 0o755)
	if out, code := r.swim("run", "1", "--plain"); code == 0 || !strings.Contains(out, "reads up to syntax 3") {
		t.Errorf("syntax 4: exit %d\n%s", code, out)
	}
}

// A lane in another language names its handlers as commands; in them swim
// gate and swim stop end nothing, and Fail: hard ends the round at a
// failed swim run (--soft opts out).
func TestWrappedLaneHandlers(t *testing.T) {
	r := newRepo(t, "")
	r.write("lane.1.sh", `#!/usr/bin/env bash
# swim: syntax 3
# Round: wrapped handlers
# Fail: hard
# On-Failure: swim run "rollback" -- sh -c 'echo "$SWIM_OUTCOME" > outcome.txt'; swim gate "rb gate" -- false; swim run "rb after" -- true
# On-Exit: echo cleanup-output
swim run --soft "soft" -- false
swim run "breaks" -- false
touch carried-on
`, 0o755)
	r.swim("run", "1", "--plain")
	log := r.log(1)
	contains(t, "log", log, "FAIL  soft (exit 1)", "STOP  hard failure: breaks", "-- handler on_failure", "PASS  rollback",
		"STOP  gate failed: rb gate", "PASS  rb after", "-- handler on_exit", "PASS  on_exit: echo cleanup-output", "| cleanup-output",
		"exit=89", "handlers: on_failure FAIL | on_exit PASS")
	if got := readFile(t, r, "outcome.txt"); got != "stopped" {
		t.Errorf("outcome %q", got)
	}
	if _, err := os.Stat(filepath.Join(r.root, "carried-on")); err == nil {
		t.Error("the program carried on after a hard failure")
	}
}
