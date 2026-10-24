package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func producer(t *testing.T, r *repo, n int, outputsLine, body string) {
	t.Helper()
	r.write(fmt.Sprintf("lane.%d.sh", n), fmt.Sprintf("#!/usr/bin/env bash\n# swim: syntax 2\n# Round: produce\n# Outputs: %s\n_swim_lib=$(\"${SWIM_BIN:-swim}\" lib) || exit 1; eval \"$_swim_lib\"\nlane_init %d\n%s\nsummary\n", outputsLine, n, body), 0o755)
}

// A round's declared outputs reach the lanes that depend on it: paths and
// small values, read-only, hashed into the chain on both sides.
func TestLaneOutputs(t *testing.T) {
	r := newRepo(t, "")
	producer(t, r, 1, "stats.json, pages/", `run "stats" bash -c 'echo "{\"n\":3}" > "$SWIM_OUT/stats.json"; mkdir "$SWIM_OUT/pages"; echo a > "$SWIM_OUT/pages/a"; echo junk > "$SWIM_OUT/extra"'
output VERSION v1.2.3`)
	r.script(2, "consume", `run "read" bash -c 'cat "$(swim input 1 stats.json)" > "$SWIM_ROOT/got.txt"; ls "$(swim input 1 pages)" >> "$SWIM_ROOT/got.txt"; echo "$(swim input 1 VERSION)" >> "$SWIM_ROOT/got.txt"'`)
	withHeader(t, r, 2, "# After: 1")
	// A lane in another language reads them too.
	r.write("lane.3.sh", "#!/usr/bin/env bash\n# Round: wrapped consumer\n# After: 1\nswim run \"read\" -- cat \"$(swim input 1 stats.json)\"\n", 0o755)
	r.mustSwim("run", "--plain")
	if got := readFile(t, r, "got.txt"); got != "{\"n\":3}\na\nv1.2.3" {
		t.Errorf("consumer read %q", got)
	}
	log1 := r.log(1)
	contains(t, "producer log", log1, "WARN  undeclared files in $SWIM_OUT: extra", "outputs: stats.json 8 B ", "pages 2 B ", "value:VERSION")
	contains(t, "wrapped consumer", r.log(3), "| {\"n\":3}")
	st := r.status().Get(1)
	if len(st.Outputs) != 3 || len(st.Outputs["stats.json"]) != 64 {
		t.Fatalf("status outputs %v", st.Outputs)
	}
	chain, _ := os.ReadFile(filepath.Join(r.root, ".swim", "chain.jsonl"))
	contains(t, "chain", string(chain), `"outputs":{"pages":"`, `"inputs":{"1:VERSION":"`, `"1:stats.json":"`+st.Outputs["stats.json"], `"input_rounds":{"1":"`+st.Job+" "+st.Run)
	r.mustSwim("verify", "--levels", "--allow-unsigned")
	if os.Geteuid() != 0 {
		path := filepath.Join(r.root, ".swim", "out", st.Job, st.Run, "stats.json")
		if err := os.WriteFile(path, []byte("x"), 0o644); err == nil {
			t.Error("an output is writable after its round ended")
		}
	}
}

// A promised output that's missing fails the round; reading a lane outside
// After: is a FAIL; lint catches it first; verify catches an output changed
// before it was read.
func TestLaneOutputProblems(t *testing.T) {
	r := newRepo(t, "")
	producer(t, r, 1, "report.html", `run "forgot" true`)
	out, _ := r.swim("run", "1", "--plain")
	contains(t, "missing", r.log(1), "FAIL  declared output missing: report.html (Outputs:)", "== END FAIL")
	_ = out

	producer(t, r, 1, "data.txt", `run "write" bash -c 'echo original > "$SWIM_OUT/data.txt"'`)
	r.mustSwim("run", "1", "--plain")
	r.script(2, "not a dependent", `run "peek" bash -c 'cat "$(swim input 1 data.txt)"'`)
	contains(t, "lint", must(r.swim("lint", "2")), "input 1 reads swim 1's outputs, but swim 1 isn't in After:", "[input-dep]")
	if out, code := r.swim("run", "2", "--plain"); code == 0 || !strings.Contains(out, "[input-dep]") {
		t.Errorf("swim run of a lane reading outside After: %d\n%s", code, out)
	}
	r.cmd("bash", "lane.2.sh").Run() // run directly, the read itself fails
	contains(t, "outside After:", r.log(2), "FAIL  input 1 data.txt (swim 1 is not in this round's After:")

	// Tamper with the produced file, then read it: verify proves the
	// consumer didn't get what the producer made.
	st := r.status().Get(1)
	path := filepath.Join(r.root, ".swim", "out", st.Job, st.Run, "data.txt")
	os.Chmod(path, 0o644)
	os.WriteFile(path, []byte("tampered\n"), 0o644)
	r.script(2, "dependent", `run "read" bash -c 'cat "$(swim input 1 data.txt)"'`)
	withHeader(t, r, 2, "# After: 1")
	r.mustSwim("run", "2", "--plain")
	out, code := r.swim("verify", "--levels", "--allow-unsigned")
	if code == 0 || !strings.Contains(out, "input 1:data.txt doesn't match what round "+st.Job+" "+st.Run+" produced") {
		t.Errorf("verify: exit %d\n%s", code, out)
	}
}

// A job id pins one round, even from an earlier run; doctor prunes old
// outputs that no lane's latest round uses.
func TestLaneOutputsByJobAndPrune(t *testing.T) {
	r := newRepo(t, "")
	producer(t, r, 1, "v.txt", `run "write" bash -c 'echo first > "$SWIM_OUT/v.txt"'`)
	r.mustSwim("run", "1", "--plain")
	first := r.status().Get(1).Job
	r.script(2, "pinned", `run "read" bash -c 'cat "$(swim input `+first[:8]+` v.txt)" > "$SWIM_ROOT/pinned.txt"'`)
	withHeader(t, r, 2, "# After: "+first)
	r.mustSwim("run", "2", "--plain")
	if got := readFile(t, r, "pinned.txt"); got != "first" {
		t.Errorf("pinned read %q", got)
	}

	// A new round of lane 1 (new job) makes the first one's outputs old.
	producer(t, r, 1, "v.txt", `run "write" bash -c 'echo second > "$SWIM_OUT/v.txt"'`)
	r.mustSwim("run", "1", "--plain")
	r.mustSwim("config", "outputs_keep", "1s")
	time.Sleep(1100 * time.Millisecond)
	contains(t, "doctor", must(r.swim("doctor")), "[outputs-old]")
	contains(t, "doctor --fix", must(r.swim("doctor", "--fix")), "removed 1 old round output dirs")
	if _, err := os.Stat(filepath.Join(r.root, ".swim", "out", first)); err == nil {
		t.Error("the old round's outputs are still there")
	}
	st := r.status().Get(1)
	if _, err := os.Stat(filepath.Join(r.root, ".swim", "out", st.Job, st.Run, "v.txt")); err != nil {
		t.Error("doctor --fix removed the latest round's outputs")
	}
}

// The bash library's input and output functions (not swim input inside
// bash -c): a value and a path, and a failed input recorded as FAIL.
func TestLibInputOutput(t *testing.T) {
	r := newRepo(t, "")
	producer(t, r, 1, "report.txt", `run "write" bash -c 'echo hello > "$SWIM_OUT/report.txt"'
output BUILD 42`)
	r.script(2, "consume with the library", `report=$(input 1 report.txt)
build=$(input 1 BUILD)
run "use" bash -c "cat '$report' > \"\$SWIM_ROOT/got.txt\"; echo $build >> \"\$SWIM_ROOT/got.txt\""
missing=$(input 1 nope.txt)
run "after missing" test -z "$missing"`)
	withHeader(t, r, 2, "# After: 1")
	r.swim("run", "--plain")
	if got := readFile(t, r, "got.txt"); got != "hello\n42" {
		t.Errorf("read %q", got)
	}
	contains(t, "log", r.log(2), "FAIL  input 1 nope.txt (that round declared no output nope.txt", "PASS  after missing")
}
