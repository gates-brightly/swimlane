package e2e

import (
	"strings"
	"testing"
	"time"
)

// A lane that leaves a process behind holding its output doesn't hold up
// the run once the lane has exited.
func TestLeftoverProcessDoesntHang(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "leave a daemon", "sleep 30 &\n"+`run "after" true`)
	start := time.Now()
	contains(t, "run", r.mustSwim("run", "1", "--plain"), "PASS")
	if d := time.Since(start); d > 15*time.Second {
		t.Errorf("the run waited %v for a leftover process", d)
	}
}

// A process a lane left behind doesn't keep its # Locks: lock: the next
// lane that takes the lock doesn't wait for it.
func TestLeftoverProcessDoesntHoldLocks(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "leave a daemon", "# Locks: db\nsleep 30 &\n"+`run "after" true`)
	r.script(2, "needs the lock", "# Locks: db\n"+`run "ok" true`)
	r.mustSwim("run", "1", "--plain")
	start := time.Now()
	contains(t, "run 2", r.mustSwim("run", "2", "--plain"), "PASS")
	if d := time.Since(start); d > 15*time.Second {
		t.Errorf("lane 2 waited %v for a lock a leftover process held", d)
	}
}

// A lane waiting for a lock keeps waiting when its # Locks: line is edited
// out of the working tree meanwhile: it locks what was staged.
func TestLocksReadFromStage(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "holds db", "# Locks: db\n"+`run "hold" bash -c 'rf="$SWIM_ROOT/release"; [ -p "$rf" ] || mkfifo "$rf" 2>/dev/null; cat "$rf" >/dev/null; echo one >> "$SWIM_ROOT/order"'`)
	r.script(2, "needs db", "# Locks: db\n"+`run "use" bash -c 'echo two >> "$SWIM_ROOT/order"'`)
	one := r.cmd(bin, "run", "1", "--plain")
	if err := one.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return laneOf(r, 1).CurrentStep == "hold" })
	two := r.cmd(bin, "run", "2", "--plain") // another run: the lock holds across runs
	if err := two.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return strings.Contains(r.mustSwim("status"), "waiting for lock db") })
	r.script(2, "needs db", `run "use" bash -c 'echo two >> "$SWIM_ROOT/order"'`) // the lock edited out
	// Lane 2 waits on the locks it staged (its names are fixed by now), so
	// the edit has nothing left to change: release lane 1 at once.
	r.release("release")
	one.Wait()
	two.Wait()
	if got := readFile(t, r, "order"); got != "one\ntwo" {
		t.Errorf("order %q: lane 2 ran while lane 1 held the lock\n%s\n%s", got, r.log(1), r.log(2))
	}
}
