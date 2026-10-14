package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/logparse"
)

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// results is the round's result lines as "KIND label", without the
// (in-process) marker, for comparing languages.
func results(t *testing.T, r *repo, n int) []string {
	t.Helper()
	round, err := logparse.ParseFile(filepath.Join(r.root, ".swim", "logs", fmt.Sprintf("agent%d.log", n)))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, res := range round.Results {
		out = append(out, res.Kind+" "+res.Label)
	}
	return out
}

// The conformance round, in bash (in-lane commands) and through each SDK.
const conformanceBash = `#!/usr/bin/env bash
# Round: conformance
trap 'touch finally.sh.txt' EXIT
swim stage check
swim run "probe" -- echo probing
swim run "native check" -- echo "items: 0"
if swim guard ALLOW_X "do x"; then swim run "x" -- true; fi
swim msg "almost done"
swim stage change
swim gate "must pass" -- false
swim run "never" -- true
`

const conformancePython = `#!/usr/bin/env python3
# Round: conformance
import swim
try:
    with swim.round() as r:
        r.stage("check")
        r.run("probe", ["echo", "probing"])
        with r.step("native check") as s:
            s.log("items: 0")
        if r.guard("ALLOW_X", "do x"):
            r.run("x", ["true"])
        r.msg("almost done")
        r.stage("change")
        r.gate("must pass", ["false"])
        r.run("never", ["true"])
finally:
    open("finally.py.txt", "w").close()
`

const conformanceTS = `// Round: conformance
import { writeFileSync } from "node:fs";
import { round } from "SDK";
try {
  await round(async (r) => {
    try {
      await r.stage("check");
      await r.run("probe", ["echo", "probing"]);
      await r.step("native check", async (s) => { s.log("items: 0"); });
      if (await r.guard("ALLOW_X", "do x")) await r.run("x", ["true"]);
      await r.msg("almost done");
      await r.stage("change");
      await r.gate("must pass", ["false"]);
      await r.run("never", ["true"]);
    } finally {
      writeFileSync("finally.ts.txt", "");
    }
  });
} catch (e) { console.error(e); process.exit(1); }
`

// Every language's round produces the same log: the same results in the
// same order, the round ended at the gate, and the program unwound (its
// finally ran).
func TestSDKConformance(t *testing.T) {
	want := ""
	for _, lang := range []string{"sh", "py", "ts", "go"} {
		t.Run(lang, func(t *testing.T) {
			r := newRepo(t, "")
			r.env = append(r.env, "ALLOW_X=1", "PYTHONPATH="+filepath.Join(repoRoot(), "sdk", "python"))
			switch lang {
			case "sh":
				r.write("lane.1.sh", conformanceBash, 0o755)
			case "py":
				if _, err := exec.LookPath("python3"); err != nil {
					t.Skip("python3 not installed")
				}
				r.write("lane.1.py", conformancePython, 0o755)
			case "ts":
				if out, err := exec.Command("node", "--experimental-strip-types", "-e", "1").CombinedOutput(); err != nil {
					t.Skipf("node can't run TypeScript: %v %s", err, out)
				}
				r.write("lane.1.ts", strings.Replace(conformanceTS, "SDK", filepath.Join(repoRoot(), "sdk", "ts", "swim.ts"), 1), 0o644)
				r.write("swim.yml", "runners:\n  ts: node --experimental-strip-types --no-warnings\n", 0o644)
			case "go":
				build := exec.Command("go", "build", "-buildvcs=false", "-o", filepath.Join(r.root, "lane.1"), "./sdk/go/conformance")
				build.Dir = repoRoot()
				if out, err := build.CombinedOutput(); err != nil {
					t.Fatalf("build: %v %s", err, out)
				}
				r.write("lane.1.yml", "round: conformance\n", 0o644)
			}
			out, code := r.swim("run", "1", "--plain")
			if code == 0 {
				t.Fatalf("the round passed:\n%s", out)
			}
			got := strings.Join(results(t, r, 1), "\n")
			if want == "" {
				want = got
			} else if got != want {
				t.Errorf("%s results differ:\n got:\n%s\nwant:\n%s", lang, got, want)
			}
			contains(t, "results", got, "PASS probe", "PASS native check", "APPROVED do x", "PASS x", "FAIL must pass", "STOP gate failed: must pass")
			if strings.Contains(got, "never") {
				t.Error("the round went on after its gate failed")
			}
			if _, err := os.Stat(filepath.Join(r.root, "finally."+lang+".txt")); err != nil {
				t.Errorf("the program didn't unwind (no finally.%s.txt)", lang)
			}
			log := r.log(1)
			contains(t, "log", log, "MSG  almost done")
			if lang != "sh" {
				contains(t, "in-process marker", log, "PASS  native check (in-process)", "| items: 0")
			}
		})
	}
}

// The socket is the lane's own: mode 0600, a wrong token is refused, a
// program outside a round is told how to run it, and an SDK of another
// protocol version is refused at hello.
func TestLaneProtocolSecurity(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	r := newRepo(t, "")
	r.write("lane.1.py", `#!/usr/bin/env python3
# Round: protocol
import json, os, socket, stat
p = os.environ["SWIM_SOCKET"]
mode = stat.S_IMODE(os.stat(p).st_mode)
s = socket.socket(socket.AF_UNIX); s.connect(p); f = s.makefile("rwb")
def call(msg):
    f.write((json.dumps(msg) + "\n").encode()); f.flush(); return json.loads(f.readline())
bad = call({"id": 1, "token": "wrong", "op": "hello"})
old = call({"id": 2, "token": os.environ["SWIM_TOKEN"], "op": "hello", "args": {"protocol": "swim.lane/v9"}})
open("probe.txt", "w").write("%o|%s|%s" % (mode, bad.get("error"), old.get("error")))
`, 0o755)
	r.mustSwim("run", "1", "--plain")
	got := readFile(t, r, "probe.txt")
	if !strings.HasPrefix(got, "600|wrong token|") || !strings.Contains(got, "speaks swim.lane/v1, the SDK swim.lane/v9") {
		t.Errorf("probe: %s", got)
	}
	cmd := exec.Command("python3", "-c", "import swim; swim.round()")
	cmd.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join(repoRoot(), "sdk", "python"), "SWIM_SOCKET=")
	out, _ := cmd.CombinedOutput()
	if !strings.Contains(string(out), "run this lane with `swim run N`") {
		t.Errorf("outside a round: %s", out)
	}
}

// Once a gate fails, the lane socket refuses every later step, for an SDK
// that ignores RoundEnded (or hasn't seen it yet): nothing runs, and each
// answer says the round has ended.
func TestLaneProtocolNoStepAfterEnd(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	r := newRepo(t, "")
	r.write("lane.1.py", `#!/usr/bin/env python3
# Round: ignores RoundEnded
import json, os, socket
s = socket.socket(socket.AF_UNIX); s.connect(os.environ["SWIM_SOCKET"]); f = s.makefile("rwb")
tok = os.environ["SWIM_TOKEN"]
def call(i, op, **args):
    f.write((json.dumps({"id": i, "token": tok, "op": op, "args": args}) + "\n").encode()); f.flush()
    return json.loads(f.readline())
call(1, "hello", protocol="swim.lane/v1", sdk="test")
g = call(2, "gate", label="must pass", argv=["false"])
n = call(3, "run", label="never", argv=["touch", "ran-never"])
b = call(4, "begin_step", label="never in-process")
q = call(5, "guard", flag="ALLOW_X", action="never guarded")
open("probe.txt", "w").write(json.dumps([g, n, b, q]))
`, 0o755)
	r.env = append(r.env, "ALLOW_X=1")
	r.swim("run", "1", "--plain")
	if _, err := os.Stat(filepath.Join(r.root, "ran-never")); err == nil {
		t.Error("a step ran after the round's gate failed")
	}
	got := readFile(t, r, "probe.txt")
	for _, want := range []string{
		`"round_ended": true, "code": 1, "result": {"exit": 1, "passed": false}}`,
		`"round_ended": true, "code": 1, "result": {"started": false}}`,
		`"round_ended": true, "code": 1, "result": {"approved": false}}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("responses: missing %s in\n%s", want, got)
		}
	}
	log := r.log(1)
	contains(t, "log", log, "FAIL  must pass (exit 1)", "STOP  gate failed: must pass")
	if strings.Contains(log, "never") {
		t.Errorf("a refused step is in the log:\n%s", log)
	}
}

// An in-process gate that fails ends the round; the SDK's on_failure
// handler runs soft, in its own section.
func TestSDKHandlersAndInProcessGate(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	r := newRepo(t, "")
	r.env = append(r.env, "PYTHONPATH="+filepath.Join(repoRoot(), "sdk", "python"))
	r.write("lane.1.py", `#!/usr/bin/env python3
# swim: syntax 3
# Round: handlers
import swim
with swim.round() as r:
    @r.on_failure
    def rollback(outcome):
        r.run("rollback " + outcome["outcome"], ["true"])
        r.gate("rollback gate", ["false"])
        r.run("after rollback gate", ["true"])
    with r.step("table is empty", gate=True) as s:
        s.log("items: 3")
        s.require(False, "table still has items")
    r.run("never", ["true"])
`, 0o755)
	r.swim("run", "1", "--plain")
	log := r.log(1)
	contains(t, "log", log, "FAIL  table is empty (in-process, table still has items)", "STOP  gate failed: table is empty",
		"-- handler on_failure", "PASS  rollback stopped", "FAIL  rollback gate", "PASS  after rollback gate", "handlers: on_failure FAIL")
	if strings.Contains(log, "never") {
		t.Error("the round went on after its in-process gate failed")
	}
}

// # Includes: puts local modules in the script's hash (so an approval
// covers them); lint warns about local imports it doesn't cover, and about
// SDK lanes running commands swim doesn't see.
func TestIncludesAndSDKLint(t *testing.T) {
	r := newRepo(t, "")
	os.MkdirAll(filepath.Join(r.root, "lib"), 0o755)
	r.write("lib/orders.py", "def count(): return 0\n", 0o644)
	r.write("helpers.py", "X = 1\n", 0o644)
	r.write("lane.1.py", `#!/usr/bin/env python3
# Round: includes
# Includes: lib/*.py
import subprocess
import swim
from lib import orders
import helpers
with swim.round() as r:
    subprocess.run(["true"])
`, 0o755)
	out := must(r.swim("lint", "1"))
	contains(t, "lint", out, "imports local helpers.py, which isn't in # Includes:", "[includes]", "subprocess.run runs a command swim doesn't see", "[sdk-bypass]")
	if strings.Contains(out, "lib/orders.py, which isn't") {
		t.Errorf("a covered import was flagged:\n%s", out)
	}
	// Editing an included file changes the script's hash: the queued pin goes.
	r.mustSwim("queue", "add", "1")
	r.write("lib/orders.py", "def count(): return 1\n", 0o644)
	wout, _ := r.cmd(bin, "watch", "--once").CombinedOutput()
	contains(t, "pin", string(wout), "changed since it was queued")
}
