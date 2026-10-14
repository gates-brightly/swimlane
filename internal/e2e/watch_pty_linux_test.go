package e2e

import (
	"os"
	"strings"
	"testing"
)

// swim watch on a terminal shows the jobs and the queue: ↑ ↓ pick one, r
// runs it now, q queues it (and the worker then runs it), Ctrl-C quits.
func TestWatchPickRunAndQueue(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "first job", `run "one" echo one-ran`)
	r.script(2, "second job", `run "two" echo two-ran`)
	r.write("lane.3.sh", "#!/usr/bin/env bash\n# being written: no Round: line yet\n", 0o755) // in use, nothing pending
	tr := r.startTUI(nil, "watch")
	s := tr.waitFor("▸ swim 1")
	contains(t, "watch screen", s, "pending", "Jobs", "first job", "second job", "Queue", "r run now", "q queue")

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

	// ↑ then q: swim 1 is queued, and the idle worker takes it.
	tr.send("\x1b[A")
	tr.waitFor("▸ swim 1")
	tr.send("q")
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

// A batch the worker takes runs in the live view, whose keys move between
// its lanes but never answer for it; the view closes when the batch ends,
// even with a lane open, and the watch screen comes back.
func TestWatchBatchLiveViewKeys(t *testing.T) {
	r := newRepo(t, "")
	release := tuiLanes(r, 2)
	r.mustSwim("queue", "add", "1", "2")
	tr := r.startTUI(nil, "watch")
	tr.waitFor("all lanes · following")
	tr.send("\x1b[C")
	tr.waitFor("swim 1 · following · .swim/logs/agent1.log")
	tr.send("y")
	tr.waitFor("pre-answers aren't available")
	os.WriteFile(release, nil, 0o644)
	tr.waitFor("#1 passed")
	s := tr.waitFor("▸ swim 1")
	contains(t, "watch screen after the batch", s, "Jobs", "Queue")
	tr.send("\x03")
	if code, _ := tr.wait(); code != 0 {
		t.Errorf("exit %d after ctrl-c", code)
	}
}

// --no-tui keeps the plain worker.
func TestWatchNoTUI(t *testing.T) {
	r := newRepo(t, "")
	tr := r.startTUI(nil, "watch", "--once", "--no-tui")
	code, out := tr.wait()
	if code != 0 || strings.Contains(out, "\x1b[?1049h") {
		t.Errorf("exit %d, screen used: %v\n%s", code, strings.Contains(out, "\x1b[?1049h"), out)
	}
}
