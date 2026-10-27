package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/queue"
	"github.com/gates-brightly/swimlane/internal/status"
)

func guardedLanes(t *testing.T, r *repo) {
	r.script(1, "drop", `if guard ALLOW_DROP "drop table t"; then run "dropped" true; fi`)
	r.script(2, "other", `if guard ALLOW_OTHER "other change"; then run "other changed" true; fi`)
	withHeader(t, r, 1, "# Guards:  ALLOW_DROP drop table t")
	withHeader(t, r, 2, "# Guards:  ALLOW_OTHER other change")
}

func doneEntries(t *testing.T, r *repo) []queue.Entry {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(r.root, ".swim", "queue", "done", "*.yml"))
	var out []queue.Entry
	for _, f := range files {
		data, _ := os.ReadFile(f)
		var e queue.Entry
		if err := yaml.Unmarshal(data, &e); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func TestQueueAndWatchOnce(t *testing.T) {
	r := newRepo(t, "")
	guardedLanes(t, r)
	if out, code := r.swim("queue", "add", "--approve", "ALLOW_TYPO"); code == 0 || !strings.Contains(out, "no lane in this batch has a guard ALLOW_TYPO") {
		t.Fatalf("typo approval accepted: %d %s", code, out)
	}
	out := r.mustSwim("queue", "add", "--approve", "ALLOW_DROP", "--note", "batch one")
	contains(t, "queue add", out, "queued q-", "(#1): 2 lanes", "approvals: ALLOW_DROP", "not approved (dry runs; a queued batch never asks): ALLOW_OTHER")
	contains(t, "queue list", r.mustSwim("queue"), "#1", "queued", "swim 1,2", "approve ALLOW_DROP", "batch one")

	// The watcher's own ALLOW_OTHER=1 must not approve the batch: only the
	// queue entry says what may change.
	c := r.cmd(bin, "watch", "--once")
	c.Env = append(c.Env, "ALLOW_OTHER=1", "SWIM_USER=zach")
	wout, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("watch: %v\n%s", err, wout)
	}
	contains(t, "watch", string(wout), "running q-", "passed; consumed to .swim/queue/done/0001-q-")
	contains(t, "agent1.log", r.log(1), "APPROVED  drop table t (ALLOW_DROP=1, approved in queue q-", "PASS  dropped")
	contains(t, "agent2.log", r.log(2), "SKIP  other change (dry run: set ALLOW_OTHER=1 to approve)")
	if strings.Contains(r.log(2), "PASS  other changed") {
		t.Error("the watcher's environment approved a guard the batch didn't")
	}
	done := doneEntries(t, r)
	if len(done) != 1 || done[0].Result == nil || done[0].Result.Result != "passed" || len(done[0].Result.Lanes) != 2 || done[0].Worker == nil {
		t.Fatalf("consumed entry: %+v", done)
	}
	contains(t, "queue list", r.mustSwim("queue"), "done: passed")
	hist, _ := os.ReadFile(filepath.Join(r.root, ".swim.log"))
	contains(t, ".swim.log", string(hist), "queue ", "approve=ALLOW_DROP  batch one", "queue-run", "queue-done")

	// A lane rewritten after it was queued is skipped, not run.
	r.script(1, "drop v2", `run "v2" true`)
	withHeader(t, r, 1, "# Job: 11111111-2222-4333-8444-555555555555")
	r.mustSwim("queue", "add", "1")
	r.script(1, "drop v3", `run "v3" true`)
	withHeader(t, r, 1, "# Job: 99999999-2222-4333-8444-555555555555")
	if out, code := r.swim("watch", "--once"); code == 0 {
		t.Errorf("a batch with a skipped lane should not pass:\n%s", out)
	}
	if l := r.status().Get(1); l.State != status.Skipped || !strings.Contains(l.Reason, "changed since it was queued") {
		t.Errorf("rewritten lane: %+v", l)
	}
	// Nothing queued: --once exits at once.
	start := time.Now()
	r.mustSwim("watch", "--once")
	if time.Since(start) > 3*time.Second {
		t.Error("watch --once waited on an empty queue")
	}
}

// A queued batch waits only for its own lanes: one that another swim run
// has waits until that run ends, one whose lanes are free starts now.
func TestWatchWaitsForBusyLanes(t *testing.T) {
	long(t)
	r := newRepo(t, "")
	r.script(1, "long", `run "long" `+hold("go"))
	r.script(2, "queued", `run "q" true`)
	busy := r.cmd(bin, "run", "1", "--plain")
	if err := busy.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return laneOf(r, 1).CurrentStep == "long" })
	r.mustSwim("queue", "add", "1")
	r.mustSwim("queue", "add", "2")
	w := r.cmd(bin, "watch", "--interval", "100ms")
	var out bytes.Buffer
	w.Stdout, w.Stderr = &out, &out
	w.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Process.Kill()
	// Swim 2's batch doesn't wait for swim run's swim 1.
	waitFor(t, func() bool { return len(doneEntries(t, r)) == 1 })
	if r.status().Get(1).CurrentStep != "long" {
		t.Errorf("swim 2's batch waited for swim run 1 to end:\n%s", out.String())
	}
	r.release("go")
	busy.Wait()
	waitFor(t, func() bool { return len(doneEntries(t, r)) == 2 })
	syscall.Kill(-w.Process.Pid, syscall.SIGINT) // the watch stops between batches
	if err := w.Wait(); err != nil {
		t.Errorf("watch after Ctrl-C: %v\n%s", err, out.String())
	}
	// It says it's waiting: with nothing running ("1 queued; waiting for
	// their lanes to be free") or beside swim 2's batch ("1 queued,
	// waiting for their lanes or a free slot"), as timing has it.
	contains(t, "watch", out.String(), "1 queued", "waiting for their lanes")
	// Swim 1's batch started only once swim run's round had ended.
	if n := strings.Count(r.log(1), "== ROUND "); n != 2 {
		t.Errorf("swim 1 ran %d rounds, want swim run's and the batch's", n)
	}
}

func TestWatchWorkersShareTheQueue(t *testing.T) {
	r := newRepo(t, "")
	for n := 1; n <= 3; n++ {
		r.script(n, "q", `run "work" sleep 0.3`)
	}
	for n := 1; n <= 3; n++ {
		r.mustSwim("queue", "add", string(rune('0'+n)))
	}
	var wg sync.WaitGroup
	outs := make([]string, 2)
	for i := range outs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out, _ := r.cmd(bin, "watch", "--once", "--interval", "100ms").CombinedOutput()
			outs[i] = string(out)
		}(i)
	}
	wg.Wait()
	done := doneEntries(t, r)
	if len(done) != 3 {
		t.Fatalf("consumed %d of 3 entries\n%s\n%s", len(done), outs[0], outs[1])
	}
	runs := map[string]bool{}
	for _, e := range done {
		if e.Result.Result != "passed" || runs[e.Result.Run] {
			t.Errorf("entry %s: %+v", e.ID, e.Result)
		}
		runs[e.Result.Run] = true
	}
	for n := 1; n <= 3; n++ {
		if c := strings.Count(r.log(n), "== ROUND "); c != 1 {
			t.Errorf("swim %d ran %d times", n, c)
		}
	}
}

func TestQueueYesOrphanRemove(t *testing.T) {
	r := newRepo(t, "")
	guardedLanes(t, r)
	// Approvals are credited to whoever queued the batch, not the worker.
	add := r.cmd(bin, "queue", "add", "-y")
	add.Env = append(add.Env, "SWIM_USER=zach")
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	c := r.cmd(bin, "watch", "--once")
	c.Env = append(c.Env, "SWIM_USER=worker")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	contains(t, "agent2.log", r.log(2), "APPROVED  other change (approved by queue q-", " -y (zach))", "PASS  other changed")

	// An entry claimed by a worker that died is orphaned, then requeued.
	r.mustSwim("queue", "add", "1")
	qdir := filepath.Join(r.root, ".swim", "queue")
	files, _ := filepath.Glob(filepath.Join(qdir, "*.queue"))
	running := strings.TrimSuffix(files[0], ".queue") + ".running"
	data, _ := os.ReadFile(files[0])
	data = append(data, []byte("worker:\n  host: "+hostname()+"\n  pid: 999999\n  user: x\n  claimed: now\n")...)
	os.WriteFile(running, data, 0o644)
	os.Remove(files[0])
	contains(t, "orphaned", r.mustSwim("queue"), "orphaned: its swimmer is gone")
	contains(t, "requeue", r.mustSwim("queue", "requeue", "2"), "requeued", "(was orphaned) as #3")
	contains(t, "rm", r.mustSwim("queue", "rm", "3"), "removed")
	if out := r.mustSwim("queue"); strings.Contains(out, "queued") {
		t.Errorf("still queued after rm:\n%s", out)
	}
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

var _ = exec.Command

func TestWatchCtrlCDuringBatch(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "long", `run "a" `+hold("go")+`
run "b" true`)
	r.mustSwim("queue", "add", "1")
	r.mustSwim("queue", "add", "1") // waits for the first: same lane
	w := r.cmd(bin, "watch", "--interval", "100ms")
	var out bytes.Buffer
	w.Stdout, w.Stderr = &out, &out
	w.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Process.Kill()
	r.waitStep(t, 1, "a")
	syscall.Kill(-w.Process.Pid, syscall.SIGINT)
	// Step a ends once the stop is requested.
	waitFor(t, func() bool { _, err := os.Stat(lane.StopPath(r.root, 1)); return err == nil })
	r.release("go")
	done := make(chan error, 1)
	go func() { done <- w.Wait() }()
	select {
	case <-done:
	case <-time.After(waitDeadline):
		t.Fatalf("watch didn't stop:\n%s", out.String())
	}
	contains(t, "agent1.log", r.log(1), "PASS  a", "STOP  interrupted by operator (after: a)")
	entries := doneEntries(t, r)
	if len(entries) != 1 || entries[0].Result.Result != "interrupted" {
		t.Fatalf("the interrupted batch should be consumed as interrupted: %+v", entries)
	}
	// The next batch is still waiting: Ctrl-C stops the watch, not the queue.
	contains(t, "queue", r.mustSwim("queue"), "#2", "queued")
}

func TestWatchYes(t *testing.T) {
	r := newRepo(t, "")
	guardedLanes(t, r)
	add := r.cmd(bin, "queue", "add", "--approve", "ALLOW_DROP")
	add.Env = append(add.Env, "SWIM_USER=zach")
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	w := r.cmd(bin, "watch", "--once", "-y")
	w.Env = append(w.Env, "SWIM_USER=worker")
	out, err := w.CombinedOutput()
	if err != nil {
		t.Fatalf("watch -y: %v\n%s", err, out)
	}
	contains(t, "watch", string(out), "-y: every guard in every batch is approved")
	// The batch's own approval keeps its credit; the worker approves the rest.
	contains(t, "agent1.log", r.log(1), "APPROVED  drop table t (ALLOW_DROP=1, approved in queue q-", " by zach)")
	contains(t, "agent2.log", r.log(2), "APPROVED  other change (approved by swim watch -y (worker))", "PASS  other changed")
	done := doneEntries(t, r)
	if len(done) != 1 || done[0].Result.ApprovedBy != "swim watch -y (worker)" {
		t.Fatalf("consumed entry should record the worker's approval: %+v", done[0].Result)
	}
	hist, _ := os.ReadFile(filepath.Join(r.root, ".swim.log"))
	contains(t, ".swim.log", string(hist), "every other guard approved by swim watch -y (worker)")
	// swim status names what approved which guard with no one asked.
	if l := r.status().Get(2); strings.Join(l.AutoApproved, " ") != "ALLOW_OTHER" || l.AutoVia != "swim watch -y" {
		t.Errorf("status lane 2: auto_approved %v via %q", l.AutoApproved, l.AutoVia)
	}
	if l := r.status().Get(1); strings.Join(l.FlagApproved, " ") != "ALLOW_DROP" || len(l.AutoApproved) != 0 {
		t.Errorf("status lane 1: flag_approved %v auto_approved %v", l.FlagApproved, l.AutoApproved)
	}

	// Without -y the same batch would have dry-run lane 2's change.
	r.mustSwim("queue", "add", "2")
	r.mustSwim("watch", "--once")
	if !strings.Contains(r.log(2), "SKIP  other change (dry run: set ALLOW_OTHER=1 to approve)") {
		t.Errorf("without -y an unapproved guard must dry-run:\n%s", r.log(2))
	}
}

// swim watch -y approves a guard in a file the script includes, and one
// whose flag the script names at run time: both are the lane's own.
func TestWatchYesIncludedAndDynamicGuards(t *testing.T) {
	r := newRepo(t, "")
	os.MkdirAll(filepath.Join(r.root, "lib"), 0o755)
	r.write("lib/g.sh", "do_inc() { if guard ALLOW_INC \"included\"; then run \"inc ran\" true; fi; }\n", 0o644)
	r.script(1, "include", `source lib/g.sh
do_inc`)
	// A guard in an included file is the lane's: its header declares it.
	withHeader(t, r, 1, "# Includes: lib/g.sh\n# Guards: ALLOW_INC  included")
	r.script(2, "dynamic", `F=ALLOW_DYN
if guard "$F" "dynamic"; then run "dyn ran" true; fi`)
	r.mustSwim("queue", "add", "1", "2")
	w := r.cmd(bin, "watch", "--once", "-y")
	w.Env = append(w.Env, "SWIM_USER=worker")
	if out, err := w.CombinedOutput(); err != nil {
		t.Fatalf("watch -y: %v\n%s", err, out)
	}
	contains(t, "agent1.log", r.log(1), "APPROVED  included (approved by swim watch -y (worker))", "PASS  inc ran")
	contains(t, "agent2.log", r.log(2), "APPROVED  dynamic (approved by swim watch -y (worker))", "PASS  dyn ran")
}

// A guard nothing approves runs as a dry run, and says so: [DRY] in swim
// status, dry_run in status.yml, until the lane's next round.
func TestWatchDryRunMarked(t *testing.T) {
	r := newRepo(t, "")
	guardedLanes(t, r)
	r.mustSwim("queue", "add", "2")
	r.mustSwim("watch", "--once")
	if l := r.status().Get(2); strings.Join(l.DryRun, " ") != "ALLOW_OTHER" {
		t.Errorf("status lane 2 dry_run: %v", l.DryRun)
	}
	contains(t, "status", r.mustSwim("status"), "[DRY] dry run (not approved): ALLOW_OTHER")
	r.mustSwim("queue", "add", "2", "--approve", "ALLOW_OTHER")
	r.mustSwim("watch", "--once")
	if l := r.status().Get(2); len(l.DryRun) != 0 {
		t.Errorf("approved round still marked dry: %v", l.DryRun)
	}
}

func TestWaitAndQueueWait(t *testing.T) {
	long(t)
	r := newRepo(t, "")
	r.script(1, "long", `run "long" `+hold("go"))
	r.script(2, "queued", `run "q" true`)
	r.script(3, "fails", `run "f" false`)
	contains(t, "wait when idle", r.mustSwim("wait"), "swim wait: idle")

	busy := r.cmd(bin, "run", "1", "--plain")
	if err := busy.Start(); err != nil {
		t.Fatal(err)
	}
	defer busy.Process.Kill()
	waitFor(t, func() bool { return laneOf(r, 1).CurrentStep == "long" })
	if out, code := r.swim("wait", "--timeout", "300ms", "--interval", "100ms"); code != 1 || !strings.Contains(out, "still busy after 300ms: swim 1 is running") {
		t.Fatalf("wait --timeout on a busy repo: %d\n%s", code, out)
	}

	// swim queue 2 --wait: shorthand for queue add, and it waits for the
	// running lane, then its own batch.
	q := r.cmd(bin, "queue", "2", "--wait")
	qout := &syncBuffer{}
	q.Stdout, q.Stderr = qout, qout
	if err := q.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return strings.Contains(qout.String(), "swim wait: waiting: swim 1 is running") })
	r.release("go")
	w := r.cmd(bin, "watch", "--interval", "100ms")
	w.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { syscall.Kill(-w.Process.Pid, syscall.SIGINT); w.Wait() }()
	if err := q.Wait(); err != nil {
		t.Fatalf("queue --wait: %v\n%s", err, qout.String())
	}
	contains(t, "queue --wait", qout.String(), "swim wait: waiting: swim 1 is running", "swim wait: idle", "(#1): passed")
	if len(doneEntries(t, r)) != 1 || r.status().Get(1).State != status.Passed {
		t.Errorf("queue --wait returned before the work was done: %+v", r.status().Get(1))
	}

	// A failing batch: --wait exits non-zero.
	if out, code := r.swim("queue", "3", "--wait"); code != 1 || !strings.Contains(out, "(#2): failed") {
		t.Errorf("queue --wait on a failing batch: %d\n%s", code, out)
	}
}

func TestWaitNamesAQueueWithNoWorker(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "q", `run "q" true`)
	r.mustSwim("queue", "1")
	if out, code := r.swim("wait", "--timeout", "200ms", "--interval", "50ms"); code != 1 || !strings.Contains(out, "1 queued batch(es) not yet taken by a worker (swim watch)") {
		t.Errorf("wait with a queued batch and no worker: %d\n%s", code, out)
	}
}

func TestQueueWaitTimeout(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "q", `run "q" true`)
	// No worker up: --timeout gives up, exits 1, and leaves the batch queued.
	start := time.Now()
	out, code := r.swim("queue", "1", "--timeout", "300ms")
	if code != 1 || !strings.Contains(out, "still busy after 300ms: 1 queued batch(es) not yet taken by a worker") || !strings.Contains(out, "(#1) is still queued") {
		t.Fatalf("queue --timeout with no worker: %d\n%s", code, out)
	}
	if took := time.Since(start); took > scaled(5*time.Second) {
		t.Errorf("--timeout 300ms took %s", took)
	}
	contains(t, "still queued", r.mustSwim("queue"), "#1", "queued")
	// With a worker, it finishes within the limit and exits with the result.
	r.mustSwim("watch", "--once")
	r.script(2, "q2", `run "q" true`)
	q := r.cmd(bin, "queue", "2", "--wait", "--timeout", "30s")
	qout := &syncBuffer{}
	q.Stdout, q.Stderr = qout, qout
	if err := q.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return strings.Contains(qout.String(), "queued q-") })
	r.mustSwim("watch", "--once")
	if err := q.Wait(); err != nil || !strings.Contains(qout.String(), "(#2): passed") {
		t.Errorf("queue --wait --timeout with a worker: %v\n%s", err, qout.String())
	}
	if _, code := r.swim("queue", "1", "--timeout", "soon"); code != 2 {
		t.Errorf("a bad --timeout should be a usage error, got %d", code)
	}
}

// swim watch runs queued batches side by side: a batch whose lanes are
// free starts while another runs (even ahead of an older one that has to
// wait), batches sharing a lane never overlap, and --batches 1 keeps them
// one at a time.
func TestWatchBatchesSideBySide(t *testing.T) {
	long(t)
	r := newRepo(t, "")
	// work is the middle step: side by side, swim 1's waits (up to 30s)
	// for swim 2's start, so the two overlap however slow the machine.
	stamp := func(name, work string) string {
		return fmt.Sprintf(`run "%[1]s start" bash -c 'date +%%s.%%N >> "$SWIM_ROOT/%[1]s.start"'
run "%[1]s work" %[2]s
run "%[1]s end" bash -c 'date +%%s.%%N >> "$SWIM_ROOT/%[1]s.end"'`, name, work)
	}
	r.script(1, "slow one", stamp("one", `bash -c 'for i in $(seq 600); do [ -e "$SWIM_ROOT/two.start" ] && exit 0; sleep 0.05; done'`))
	r.script(2, "two", stamp("two", "sleep 0.2"))
	times := func(name string) []float64 {
		data, _ := os.ReadFile(filepath.Join(r.root, name))
		var out []float64
		for _, f := range strings.Fields(string(data)) {
			v, _ := strconv.ParseFloat(f, 64)
			out = append(out, v)
		}
		return out
	}
	reset := func() {
		for _, f := range []string{"one.start", "one.end", "two.start", "two.end"} {
			os.Remove(filepath.Join(r.root, f))
		}
	}

	// Side by side: 2 starts before 1 ends; a second batch of lane 1 waits
	// for the first.
	r.mustSwim("queue", "add", "1")
	r.mustSwim("queue", "add", "1")
	r.mustSwim("queue", "add", "2")
	out := r.mustSwim("watch", "--once")
	contains(t, "watch", out, "up to 4 at once", "started")
	one, oneEnd, two := times("one.start"), times("one.end"), times("two.start")
	if len(one) != 2 || len(oneEnd) != 2 || len(two) != 1 {
		t.Fatalf("starts %v ends %v two %v\n%s", one, oneEnd, two, out)
	}
	if two[0] >= oneEnd[0] {
		t.Errorf("batch 3 (swim 2) waited for batch 1 (swim 1): started %.1fs after it ended", two[0]-oneEnd[0])
	}
	if one[1] < oneEnd[0] {
		t.Errorf("two batches of swim 1 overlapped: the second started %.1fs before the first ended", oneEnd[0]-one[1])
	}
	if done := doneEntries(t, r); len(done) != 3 {
		t.Fatalf("consumed %d of 3", len(done))
	}

	// One at a time.
	reset()
	r.script(1, "slow one", stamp("one", "sleep 0.2"))
	r.mustSwim("queue", "add", "1")
	r.mustSwim("queue", "add", "2")
	r.mustSwim("watch", "--once", "--batches", "1")
	if oneEnd, two := times("one.end"), times("two.start"); len(oneEnd) != 1 || len(two) != 1 || two[0] < oneEnd[0] {
		t.Errorf("--batches 1: swim 2 started at %v, swim 1 ended at %v", two, oneEnd)
	}
}

// T39: a batch that approves none of its lanes' guards warns on stderr,
// naming each lane and the guards that will dry-run, and how to approve
// them; --approve or -y doesn't warn.
func TestQueueWarnsUnapprovedGuards(t *testing.T) {
	r := newRepo(t, "")
	guardedLanes(t, r)
	queueErr := func(args ...string) (string, string) {
		t.Helper()
		c := r.cmd(bin, append([]string{"queue", "add"}, args...)...)
		var out, errb strings.Builder
		c.Stdout, c.Stderr = &out, &errb
		if err := c.Run(); err != nil {
			t.Fatalf("queue add %v: %v\n%s%s", args, err, out.String(), errb.String())
		}
		return out.String(), errb.String()
	}
	_, stderr := queueErr("1", "2")
	contains(t, "warning", stderr, "approves no guards, so these dry-run", "swim 1  ALLOW_DROP", "swim 2  ALLOW_OTHER",
		"to approve: swim queue add 1 --approve ALLOW_DROP", "-y (every guard)")
	for _, args := range [][]string{{"1", "--approve", "ALLOW_DROP"}, {"1", "2", "-y"}} {
		if _, stderr := queueErr(args...); strings.Contains(stderr, "approves no guards") {
			t.Errorf("queue add %v warned:\n%s", args, stderr)
		}
	}
}
