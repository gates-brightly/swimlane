package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// swim watch on a terminal shows the jobs and the queue: ↑ ↓ pick one, r
// runs it now, q adds it to the next batch and s submits that batch (the
// worker then runs it), Ctrl-C quits.
func TestWatchPickRunAndQueue(t *testing.T) {
	long(t)
	r := newSerialRepo(t, "")
	r.script(1, "first job", `run "one" echo one-ran`)
	r.script(2, "second job", `run "two" echo two-ran`)
	r.write("lane.3.sh", "#!/usr/bin/env bash\n# being written: no Round: line yet\n", 0o755) // in use, nothing pending
	tr := r.startTUI(nil, "watch")
	s := tr.waitFor("▸ swim 1")
	contains(t, "watch screen", s, "pending", "Jobs", "first job", "second job", "Queue", "r run now", "q batch", "s submit")

	// ↓ then r: swim 2 runs now, and the screen comes back saying so.
	tr.send("\x1b[B")
	tr.waitFor("▸ swim 2")
	tr.send("r")
	tr.waitFor("swim 2 passed")
	if log := r.log(2); !strings.Contains(log, "PASS  two") {
		t.Fatalf("swim 2 didn't run:\n%s", log)
	}
	if log := r.log(1); log != "" {
		t.Errorf("swim 1 ran too:\n%s", log)
	}

	// ↑ then q: swim 1 is in the next batch, not queued yet; s submits
	// it, and the idle worker takes it.
	tr.send("\x1b[A")
	tr.waitFor("▸ swim 1")
	tr.send("q")
	tr.waitFor("submit swim 1 as one batch")
	if out := r.mustSwim("queue", "list", "--all"); strings.Contains(out, "#1") {
		t.Fatalf("q queued before s:\n%s", out)
	}
	tr.send("s")
	tr.waitFor("#1 passed")
	if log := r.log(1); !strings.Contains(log, "PASS  one") {
		t.Fatalf("queued swim 1 didn't run:\n%s", log)
	}
	if out := r.mustSwim("queue", "list", "--all"); !strings.Contains(out, "passed") {
		t.Errorf("the batch wasn't consumed:\n%s", out)
	}

	// A lane with no pending round says so; Ctrl-C quits.
	tr.send("\x1b[B\x1b[B")
	tr.waitFor("▸ swim 3")
	tr.send("r")
	tr.waitFor("swim 3 holds no pending round")
	tr.send("\x03")
	if code, _ := tr.wait(); code != 0 {
		t.Errorf("exit %d after ctrl-c", code)
	}
}

// A batch the worker takes runs while the watch screen stays up: the
// queue shows it running, enter on it follows its output (v: the lanes'
// verbose output), q comes back.
func TestWatchBatchOutputView(t *testing.T) {
	r := newSerialRepo(t, "")
	release := tuiLanes(r, 2)
	r.mustSwim("queue", "add", "1", "2")
	tr := r.startTUI(nil, "watch")
	tr.waitFor("(#1, swim 1,2)")
	tr.send("\x1b[B\x1b[B")
	tr.waitFor("▸ #1")
	tr.send("\r")
	tr.waitFor("swim watch · #1")
	// v: the lanes' own logs, step output included.
	tr.send("v")
	tr.waitFor("· verbose")
	tr.waitFor("lane2-hello")
	tr.send("q")
	tr.waitFor("▸ #1")
	release()
	s := tr.waitFor("#1 passed")
	contains(t, "watch screen after the batch", s, "Jobs", "Queue")
	tr.send("\x03")
	if code, _ := tr.wait(); code != 0 {
		t.Errorf("exit %d after ctrl-c", code)
	}
}

// Following a running batch, the view gets a header when it ends: the
// result, the exit code and each lane that didn't pass, with its failed
// step, above the output.
func TestWatchFollowEndHeader(t *testing.T) {
	r := newSerialRepo(t, "")
	release := tuiLanes(r, 2)
	body := `run "hold" ` + hold("release") + `
gate "the check" false`
	r.script(2, "fails after release", body)
	r.mustSwim("queue", "add", "1", "2")
	tr := r.startTUI(nil, "watch")
	tr.waitFor("(#1, swim 1,2)")
	tr.send("\x1b[B\x1b[B")
	tr.waitFor("▸ #1")
	tr.send("\r")
	s := tr.waitFor("swim watch · #1")
	if strings.Contains(s, "FAILED") || strings.Contains(s, "PASSED") {
		t.Errorf("a header while the batch runs:\n%s", s)
	}
	release()
	s = tr.waitScreen("the end header", func(s string) bool {
		return strings.Contains(s, "✗ FAILED · exit 1") && strings.Contains(s, "swim 2: FAIL  the check")
	})
	contains(t, "follow view after the end", s, "swim summary") // the output is still there, under the header
	tr.send("q")
	tr.waitFor("#1 failed")
	tr.send("\x03")
	tr.wait()
}

// Enter on a running lane's row follows its log, with no detour through
// its batch; v switches to the batch's overview.
func TestWatchLaneOutputView(t *testing.T) {
	r := newSerialRepo(t, "")
	release := tuiLanes(r, 2)
	r.mustSwim("queue", "add", "1", "2")
	tr := r.startTUI(nil, "watch")
	tr.waitFor("(#1, swim 1,2)")
	tr.send("\r")
	tr.waitFor("swim watch · swim 1 · #1")
	tr.waitFor("lane1-hello")
	tr.send("v")
	tr.waitFor("· batch")
	tr.send("q")
	tr.waitFor("▸ ")
	release()
	tr.waitFor("#1 passed")
	// Not running any more: enter says so instead of opening an empty view.
	tr.send("\r")
	tr.waitFor("swim 1 isn't running")
	tr.send("\x03")
	if code, _ := tr.wait(); code != 0 {
		t.Errorf("exit %d after ctrl-c", code)
	}
}

// l on a lane shows its log, every round, running or not; l on a batch
// shows its lanes' logs.
func TestWatchLogsKey(t *testing.T) {
	r := newSerialRepo(t, "")
	r.script(1, "short lane 1", `run "hello" echo lane1-hello`)
	r.script(2, "short lane 2", `run "hello" echo lane2-hello`)
	r.mustSwim("run", "1", "--plain")
	r.mustSwim("run", "1", "--plain") // two rounds in its log
	tr := r.startTUI(nil, "watch")
	tr.waitFor("l logs")
	tr.send("l")
	tr.waitFor("swim watch · swim 1 · .swim/logs/agent1.log")
	s := tr.waitScreen("both rounds", func(s string) bool { return strings.Count(s, "== ROUND") == 2 })
	contains(t, "l on a lane", s, "| lane1-hello")
	tr.send("v")
	s = tr.waitFor("· last round")
	if n := strings.Count(s, "== ROUND"); n != 1 {
		t.Errorf("last round shows %d rounds:\n%s", n, s)
	}
	tr.send("q")
	tr.waitFor("▸ swim 1")
	// A lane with no log yet says so.
	tr.send("\x1b[B")
	tr.waitFor("▸ swim 2")
	tr.send("l")
	tr.waitFor("swim 2 has no current log")
	// A batch (its lane busy in another run, so no swimmer claims it): its
	// lanes' logs, from their last run.
	r.script(2, "held lane 2", `run "hold" `+hold("go"))
	wait := startRun(t, r, "run", "2", "--plain")
	waitFor(t, func() bool { return laneOf(r, 2).CurrentStep == "hold" })
	r.mustSwim("queue", "add", "2")
	tr.waitFor("#1    queued")
	tr.send("\x1b[B")
	tr.waitFor("▸ #1")
	tr.send("l")
	tr.waitFor("· logs")
	tr.waitFor("[2] ")
	tr.send("q")
	tr.waitFor("▸ #1")
	tr.send("\x03")
	if code, _ := tr.wait(); code != 0 {
		t.Errorf("exit %d after ctrl-c", code)
	}
	r.release("go")
	if out, code := wait(); code != 0 {
		t.Errorf("the held run: exit %d\n%s", code, out)
	}
}

// a on a lane archives its log (swim archive N): the lane goes idle and the
// log is kept as agentN.prev-<job>.log. A lane with no log says so.
func TestWatchArchiveKey(t *testing.T) {
	r := newSerialRepo(t, "")
	r.script(1, "short lane 1", `run "hello" echo lane1-hello`)
	r.script(2, "short lane 2", `run "hello" echo lane2-hello`)
	r.mustSwim("run", "1", "--plain")
	tr := r.startTUI(nil, "watch")
	tr.waitFor("a archive")
	tr.waitFor("▸ swim 1")
	tr.send("a")
	tr.waitFor("archived .swim/logs/agent1.log -> .swim/logs/agent1.prev-")
	if _, err := os.Stat(filepath.Join(r.root, ".swim", "logs", "agent1.log")); err == nil {
		t.Error("agent1.log is still there")
	}
	tr.send("\x1b[B")
	tr.waitFor("▸ swim 2")
	tr.send("a")
	tr.waitFor("nothing archived")
	tr.send("\x03")
	if code, _ := tr.wait(); code != 0 {
		t.Errorf("exit %d after ctrl-c", code)
	}
}

// The main screen streams the running lanes' output under the queue; o
// hides it.
func TestWatchStreamsRunningOutput(t *testing.T) {
	long(t)
	r := newRepo(t, "")
	release := tuiLanes(r, 2)
	r.mustSwim("queue", "add", "1")
	r.mustSwim("queue", "add", "2")
	tr := r.startTUI(nil, "watch")
	s := tr.waitScreen("both lanes streaming", func(s string) bool {
		return strings.Contains(s, "\n  Running") && strings.Contains(s, "swim 1 · hold") && strings.Contains(s, "swim 2 · hold") &&
			strings.Contains(s, "lane1-hello") && strings.Contains(s, "lane2-hello")
	})
	contains(t, "the main screen", s, "Jobs", "Queue", "o stream")
	tr.send("o")
	tr.waitScreen("stream hidden", func(s string) bool {
		return strings.Contains(s, "running output hidden") && !strings.Contains(s, "lane1-hello")
	})
	tr.send("o")
	tr.waitFor("lane1-hello")
	release()
	tr.waitScreen("both done, nothing streaming", func(s string) bool {
		return strings.Contains(s, "#2 passed") && !strings.Contains(s, "\n  Running") && !strings.Contains(s, "lane1-hello")
	})
	tr.send("\x03")
	if code, _ := tr.wait(); code != 0 {
		t.Errorf("exit %d after ctrl-c", code)
	}
}

// --no-tui keeps the plain worker.
func TestWatchNoTUI(t *testing.T) {
	r := newSerialRepo(t, "")
	tr := r.startTUI(nil, "watch", "--once", "--no-tui")
	code, out := tr.wait()
	if code != 0 || strings.Contains(out, "\x1b[?1049h") {
		t.Errorf("exit %d, screen used: %v\n%s", code, strings.Contains(out, "\x1b[?1049h"), out)
	}
}

// swim watch has no d (trash) key: d on a lane changes nothing.
func TestWatchNoTrashKey(t *testing.T) {
	r := newSerialRepo(t, "")
	r.script(1, "keep me", `run "one" true`)
	tr := r.startTUI(nil, "watch")
	tr.waitFor("▸ swim 1")
	tr.waitFor("a archive  ^C quit")
	tr.send("d")
	tr.send("o")
	tr.waitFor("running output hidden")
	if _, err := os.Stat(filepath.Join(r.root, "lane.1.sh")); err != nil {
		t.Fatal("d removed swim 1's script")
	}
	tr.send("\x03")
	tr.wait()
}
