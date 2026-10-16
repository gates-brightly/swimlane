package e2e

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// matrixLane writes lane n as a matrix round over SVC.
func matrixLane(t *testing.T, r *repo, n int, header, body string) {
	t.Helper()
	r.script(n, "Cut over $SVC", body)
	for _, l := range strings.Split(strings.TrimSpace(header), "\n") {
		withHeader(t, r, n, l)
	}
}

// One script, a cell per value: each with its own log, status and job;
// Matrix-Parallel caps cells at once; the lane passes only if every cell
// does; --failed and 3[cell] rerun cells; After: 1[orders] waits on one.
func TestMatrixRounds(t *testing.T) {
	r := newRepo(t, "")
	r.write("services.txt", "orders\nbilling\nusers\nsearch\n", 0o644)
	matrixLane(t, r, 1, "# Matrix: SVC=@services.txt\n# Matrix-Parallel: 2\n# Locks: tf/$SVC", `run "plan $SVC" bash -c '
mkdir -p .running && touch .running/$SVC
ls .running | wc -l >> concurrency.txt
sleep 0.3
rm .running/$SVC
[ "$SVC" != billing ] || [ -e fixed ]'`)
	r.script(2, "after orders", `run "follows" true`)
	withHeader(t, r, 2, "# After: 1[orders]")

	out, code := r.swim("run", "--plain")
	if code == 0 {
		t.Fatalf("passed with a failing cell:\n%s", out)
	}
	contains(t, "summary", out, "cells", "1[billing] FAILED  FAIL  plan billing (exit 1)", "swim log '1[billing]'")
	for _, c := range []string{"orders", "billing", "users", "search"} {
		if _, err := os.Stat(filepath.Join(r.root, ".swim", "logs", "agent1."+c+".log")); err != nil {
			t.Errorf("no log for cell %s", c)
		}
	}
	contains(t, "cell log", readFile(t, r, ".swim/logs/agent1.users.log"), "Cut over users", "PASS  plan users")
	maxSeen := 0
	for _, l := range strings.Fields(readFile(t, r, "concurrency.txt")) {
		k, _ := strconv.Atoi(l)
		maxSeen = max(maxSeen, k)
	}
	if maxSeen > 2 {
		t.Errorf("%d cells at once, Matrix-Parallel is 2", maxSeen)
	}
	st := r.status().Get(1)
	if st.State != "failed" || len(st.Cells) != 4 {
		t.Fatalf("lane status: %+v", st)
	}
	if b := st.FindCell("billing"); b == nil || b.State != "failed" || !strings.HasSuffix(b.Job, "/billing") || !strings.HasPrefix(b.Job, st.Job) {
		t.Errorf("billing cell: %+v (lane job %s)", b, st.Job)
	}
	contains(t, "lane 2 ran (waits only on 1[orders])", r.log(2), "PASS  follows")
	contains(t, "swim status", r.mustSwim("status"), "cells: 3/4 passed · failed: billing")
	contains(t, "swim log 1", r.mustSwim("log", "1"), "1[billing]", "failed", "swim log '1[orders]'")
	contains(t, "swim log cell", r.mustSwim("log", "1[billing]"), "FAIL  plan billing")
	contains(t, "plan", r.mustSwim("plan", "1"), "matrix: 4 cells (billing, orders, search, users), 2 at once")

	// --failed reruns only billing.
	os.WriteFile(filepath.Join(r.root, "fixed"), nil, 0o644)
	os.Remove(filepath.Join(r.root, "concurrency.txt"))
	r.mustSwim("run", "1", "--failed", "--plain")
	if got := strings.Fields(readFile(t, r, "concurrency.txt")); len(got) != 1 {
		t.Errorf("--failed ran %d cells, want 1", len(got))
	}
	if st := r.status().Get(1); st.State != "passed" || st.CellsText() != "4/4 passed" {
		t.Errorf("after --failed: %s %s", st.State, st.CellsText())
	}
	// One cell by name.
	os.Remove(filepath.Join(r.root, "concurrency.txt"))
	r.mustSwim("run", "1[search]", "--plain")
	if got := strings.Fields(readFile(t, r, "concurrency.txt")); len(got) != 1 {
		t.Errorf("1[search] ran %d cells", len(got))
	}
	if _, code := r.swim("run", "1[nope]", "--plain"); code == 0 {
		t.Error("an unknown cell ran")
	}
}

// A failed matrix round says which cells failed and why: the end of the
// run and swim log N list only the cells that didn't pass, each with its
// first failed step and the line its command printed.
func TestMatrixFailuresSummary(t *testing.T) {
	r := newRepo(t, "")
	matrixLane(t, r, 1, "# Matrix: SVC=orders,billing,users,search", `run "fetch $SVC" true
run "deploy $SVC" bash -c 'case $SVC in billing|search) echo "deploy: no capacity in $SVC"; echo second; exit 4 ;; esac'
run "after $SVC" true`)
	out, code := r.swim("run", "1", "--plain")
	if code == 0 {
		t.Fatalf("passed with failing cells:\n%s", out)
	}
	summary := out[strings.LastIndex(out, "swim summary"):]
	for _, view := range []struct{ name, text string }{
		{"run summary", summary},
		{"swim log 1", r.mustSwim("log", "1")},
		{"swim log 1 --failures", r.mustSwim("log", "1", "--failures")},
	} {
		contains(t, view.name, view.text,
			"1[billing] FAILED  FAIL  deploy billing (exit 4)", "| deploy: no capacity in billing",
			"1[search] FAILED  FAIL  deploy search (exit 4)", "| deploy: no capacity in search")
		if strings.Contains(view.text, "| second") {
			t.Errorf("%s: more than the first line:\n%s", view.name, view.text)
		}
	}
	only := r.mustSwim("log", "1", "--failures")
	contains(t, "--failures", only, "2 of 4 cells didn't pass")
	for _, c := range []string{"orders", "users"} {
		if strings.Contains(only, c) || strings.Contains(summary, "1["+c+"]") {
			t.Errorf("a passing cell (%s) is listed:\n%s\n%s", c, summary, only)
		}
	}
	if out, code := r.swim("log", "1[orders]", "--failures"); code != 2 {
		t.Errorf("--failures on one cell: exit %d\n%s", code, out)
	}
}

// Matrix-Fail: stop skips the cells not started after a failure; a cross
// product with an exclusion; editing the value file voids a queued pin.
func TestMatrixFailStopAndPins(t *testing.T) {
	r := newRepo(t, "")
	matrixLane(t, r, 1, "# Matrix: SVC=a,b,c\n# Matrix-Fail: stop", `run "work $SVC" bash -c '[ "$SVC" != a ]'`)
	r.swim("run", "1", "--plain")
	st := r.status().Get(1)
	if st.FindCell("a").State != "failed" || st.FindCell("b").State != "skipped" || st.FindCell("c").State != "skipped" {
		t.Errorf("stop: %s", st.CellsText())
	}

	r.script(2, "x $ENV $SVC", `run "w" true`)
	withHeader(t, r, 2, "# Matrix: ENV=staging,prod")
	withHeader(t, r, 2, "# Matrix: SVC=orders,search")
	withHeader(t, r, 2, "# Matrix-Exclude: ENV=prod,SVC=search")
	r.mustSwim("run", "2", "--plain")
	if st := r.status().Get(2); len(st.Cells) != 3 || st.FindCell("ENV=prod,SVC=search") != nil {
		t.Errorf("cross product: %s", st.CellsText())
	}

	r.write("svc.txt", "one\ntwo\n", 0o644)
	matrixLane(t, r, 3, "# Matrix: SVC=@svc.txt", `run "w" true`)
	r.mustSwim("queue", "add", "3")
	r.write("svc.txt", "one\ntwo\nthree\n", 0o644)
	out, _ := r.cmd(bin, "watch", "--once").CombinedOutput()
	contains(t, "queued pin", string(out), "changed since it was queued")
}

// A signed approval with --cell covers that cell only; one without covers
// every cell. Each cell asks for itself, naming its cell.
func TestMatrixApprovalPerCell(t *testing.T) {
	r := newRepo(t, "")
	keys := people(t, r, "", "alice")
	matrixLane(t, r, 1, "# Matrix: SVC=orders,billing", `if guard ALLOW_DROP "drop $SVC tables"; then run "dropped $SVC" true; fi`)
	r.mustSwim("approve", "1", "--guards", "ALLOW_DROP", "--cell", "orders", "--key", keys["alice"])
	contains(t, "approvals", r.mustSwim("approve"), "(cell orders only)")
	r.mustSwim("run", "1", "--plain", "--no-prompt")
	contains(t, "orders", readFile(t, r, ".swim/logs/agent1.orders.log"), "APPROVED  [orders] drop orders tables (approved by alice", "PASS  dropped orders")
	billing := readFile(t, r, ".swim/logs/agent1.billing.log")
	contains(t, "billing", billing, "SKIP  [billing] drop billing tables (dry run")
	if strings.Contains(billing, "PASS  dropped") {
		t.Error("a --cell orders approval approved billing")
	}
	// An approval without --cell covers every cell.
	r.mustSwim("approve", "1", "--guards", "ALLOW_DROP", "--key", keys["alice"])
	r.mustSwim("run", "1", "--plain", "--no-prompt")
	contains(t, "billing, whole-round approval", readFile(t, r, ".swim/logs/agent1.billing.log"), "PASS  dropped billing")
}

// swim plan --check checks every cell (read-only); the first Ctrl-C stops
// every running cell at its step boundary and skips cells not started.
func TestMatrixCheckAndInterrupt(t *testing.T) {
	r := newRepo(t, "")
	matrixLane(t, r, 1, "# Matrix: SVC=a,b,c", `stage check
run "probe $SVC" touch probed.$SVC
stage change
run "change $SVC" touch changed.$SVC`)
	r.mustSwim("plan", "--check", "1")
	for _, c := range []string{"a", "b", "c"} {
		if _, err := os.Stat(filepath.Join(r.root, "probed."+c)); err != nil {
			t.Errorf("cell %s wasn't checked", c)
		}
		if _, err := os.Stat(filepath.Join(r.root, "changed."+c)); err == nil {
			t.Errorf("cell %s's change ran in a check", c)
		}
		contains(t, "cell check log", readFile(t, r, ".swim/logs/agent1."+c+".check.log"), "CHECKED  stopped before stage change")
	}
	if st := r.status().Get(1); st.State == "passed" {
		t.Error("a check made the matrix lane passed")
	}

	matrixLane(t, r, 2, "# Matrix: SVC=x,y,z\n# Matrix-Parallel: 2", `run "first $SVC" sleep 1
run "second $SVC" touch second.$SVC`)
	l := r.launch(t, nil)
	waitFor(t, func() bool {
		st := r.status().Get(2)
		return st != nil && st.FindCell("x") != nil && st.FindCell("x").CurrentStep == "first x"
	})
	time.Sleep(150 * time.Millisecond)
	l.signal(syscall.SIGINT)
	l.wait(t, 20*time.Second)
	st := r.status().Get(2)
	if st.FindCell("z").State != "skipped" {
		t.Errorf("cell z (not started) should be skipped: %s", st.CellsText())
	}
	for _, c := range []string{"x", "y"} {
		if _, err := os.Stat(filepath.Join(r.root, "second."+c)); err == nil {
			t.Errorf("cell %s ran its next step after the stop", c)
		}
		contains(t, "cell "+c, readFile(t, r, ".swim/logs/agent2."+c+".log"), "STOP  interrupted by operator (after: first "+c+")")
	}
}
