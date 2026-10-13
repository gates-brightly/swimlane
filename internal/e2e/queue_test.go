package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

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

func TestWatchWaitsForIdle(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "long", `run "long" sleep 1.5`)
	r.script(2, "queued", `run "q" true`)
	busy := r.cmd(bin, "run", "1", "--plain")
	if err := busy.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return r.status().Get(1).CurrentStep == "long" })
	r.mustSwim("queue", "add", "2")
	w := r.cmd(bin, "watch", "--interval", "100ms")
	var out bytes.Buffer
	w.Stdout, w.Stderr = &out, &out
	w.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Process.Kill()
	busy.Wait()
	waitFor(t, func() bool { return len(doneEntries(t, r)) == 1 })
	syscall.Kill(-w.Process.Pid, syscall.SIGINT) // the watch stops between batches
	if err := w.Wait(); err != nil {
		t.Errorf("watch after Ctrl-C: %v\n%s", err, out.String())
	}
	contains(t, "watch", out.String(), "waiting for swim to be idle: swim 1 is running")
	l1, l2 := r.status().Get(1), r.status().Get(2)
	if l2.StartedAt == nil || l1.FinishedAt == nil || *l2.StartedAt < *l1.FinishedAt {
		t.Errorf("the queued lane started before swim was idle: lane 1 finished %v, lane 2 started %v", l1.FinishedAt, l2.StartedAt)
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
	// One batch at a time: no two runs overlapped.
	hist, _ := os.ReadFile(filepath.Join(r.root, ".swim.log"))
	depth := 0
	for _, line := range strings.Split(string(hist), "\n") {
		switch {
		case strings.Contains(line, "  queue-run "):
			depth++
			if depth > 1 {
				t.Fatalf("two batches ran at once:\n%s", hist)
			}
		case strings.Contains(line, "  queue-done "):
			depth--
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
	contains(t, "orphaned", r.mustSwim("queue"), "orphaned: its worker is gone")
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
	r.script(1, "long", `run "a" sleep 1
run "b" true`)
	r.script(2, "later", `run "x" true`)
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
	waitFor(t, func() bool { return r.status().Get(1).CurrentStep == "a" })
	time.Sleep(150 * time.Millisecond)
	syscall.Kill(-w.Process.Pid, syscall.SIGINT)
	done := make(chan error, 1)
	go func() { done <- w.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
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

	// Without -y the same batch would have dry-run lane 2's change.
	r.mustSwim("queue", "add", "2")
	r.mustSwim("watch", "--once")
	if !strings.Contains(r.log(2), "SKIP  other change (dry run: set ALLOW_OTHER=1 to approve)") {
		t.Errorf("without -y an unapproved guard must dry-run:\n%s", r.log(2))
	}
}

func TestWaitAndQueueWait(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "long", `run "long" sleep 1.5`)
	r.script(2, "queued", `run "q" true`)
	r.script(3, "fails", `run "f" false`)
	contains(t, "wait when idle", r.mustSwim("wait"), "swim wait: idle")

	busy := r.cmd(bin, "run", "1", "--plain")
	if err := busy.Start(); err != nil {
		t.Fatal(err)
	}
	defer busy.Process.Kill()
	waitFor(t, func() bool { return r.status().Get(1).CurrentStep == "long" })
	if out, code := r.swim("wait", "--timeout", "300ms", "--interval", "100ms"); code != 1 || !strings.Contains(out, "still busy after 300ms: swim 1 is running") {
		t.Fatalf("wait --timeout on a busy repo: %d\n%s", code, out)
	}

	// swim queue 2 --wait: shorthand for queue add, and it waits for the
	// running lane, then its own batch.
	q := r.cmd(bin, "queue", "2", "--wait")
	var qout bytes.Buffer
	q.Stdout, q.Stderr = &qout, &qout
	if err := q.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return strings.Contains(qout.String(), "queued q-") })
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
