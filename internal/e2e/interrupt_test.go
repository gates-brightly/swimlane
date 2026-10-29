package e2e

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/step"
	"github.com/gates-brightly/swimlane/internal/testwait"
)

// launched is a `swim all` started in its own process group, like a
// terminal's foreground job, so a signal to the group is what the terminal
// sends on Ctrl-C.
type launched struct {
	c    *exec.Cmd
	out  *syncBuffer
	done chan error
}

// syncBuffer is a command's output, read while it writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	n, err := b.buf.Write(p)
	b.mu.Unlock()
	output.Notify() // a wait on what a command printed checks again
	return n, err
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (r *repo) launch(t *testing.T, stdin io.Reader, args ...string) *launched {
	t.Helper()
	return start(t, r.cmd(bin, append([]string{"all", "--plain"}, args...)...), stdin)
}

// start starts c in its own process group, its output in a syncBuffer.
func start(t *testing.T, c *exec.Cmd, stdin io.Reader) *launched {
	t.Helper()
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out := &syncBuffer{}
	c.Stdout, c.Stderr, c.Stdin = out, out, stdin
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	l := &launched{c: c, out: out, done: make(chan error, 1)}
	go func() { l.done <- c.Wait() }()
	t.Cleanup(func() { syscall.Kill(-c.Process.Pid, syscall.SIGKILL) })
	return l
}

// signal sends sig to swim's process group, as the terminal does.
func (l *launched) signal(sig syscall.Signal) { syscall.Kill(-l.c.Process.Pid, sig) }

// saw waits for the command to print want: it has handled what printed it.
func (l *launched) saw(t *testing.T, want string) {
	t.Helper()
	if !testwait.UntilOK(&output, func() bool { return strings.Contains(l.out.String(), want) }, waitDeadline) {
		t.Fatalf("never printed %q:\n%s", want, l.out.String())
	}
}

// wait is the exit code; one still running after waitDeadline fails the
// test. A test that times the exit checks that itself.
func (l *launched) wait(t *testing.T) int {
	t.Helper()
	select {
	case err := <-l.done:
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		return 0
	case <-time.After(waitDeadline):
		t.Fatalf("didn't finish within %s:\n%s", waitDeadline, l.out.String())
	}
	return -1
}

// waitStep waits until lane n runs step label and the step's command has
// started (its process group is recorded). A step that must set something
// up first (a trap) says so with a file: waitFile.
func (r *repo) waitStep(t *testing.T, n int, label string) {
	t.Helper()
	pgid := filepath.Join(r.root, ".swim", "logs", fmt.Sprintf("agent%d.log", n)+step.GroupSuffix)
	waitFor(t, func() bool {
		if laneOf(r, n).CurrentStep != label {
			return false
		}
		_, err := os.Stat(pgid)
		return err == nil
	})
}

// waitFile waits for the file name in r's root: a step wrote it.
func (r *repo) waitFile(t *testing.T, name string) {
	t.Helper()
	waitFor(t, func() bool { _, err := os.Stat(filepath.Join(r.root, name)); return err == nil })
}

// noStray fails if a process whose command line holds marker is still
// there after a few seconds (scaled): a group just killed takes a moment.
func noStray(t *testing.T, marker string) {
	t.Helper()
	out, _ := exec.Command("pgrep", "-f", marker).Output()
	deadline := time.Now().Add(scaled(5 * time.Second))
	var left []string
	for _, f := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(f)
		if err == nil && !testwait.Exited(pid, time.Until(deadline)) {
			left = append(left, f)
		}
	}
	if len(left) > 0 {
		t.Errorf("processes matching %q survived: %s", marker, strings.Join(left, " "))
	}
}

func TestGracefulInterrupt(t *testing.T) {
	r := newRepo(t, "")
	// Step "a" notes any SIGINT it gets; it must get none. Both steps run
	// until both lanes have the stop request.
	r.script(1, "graceful", `run "a" bash -c 'trap "echo got > $SWIM_ROOT/int1.txt" INT; touch $SWIM_ROOT/trap1; rf="$SWIM_ROOT/go"; [ -p "$rf" ] || mkfifo "$rf" 2>/dev/null; cat "$rf" >/dev/null; true'
run "b" true`)
	r.script(2, "after 1", `run "x" true`)
	r.script(3, "other", `run "c" `+hold("go")+`
run "d" true`)
	withHeader(t, r, 2, "# After: 1")
	l := r.launch(t, nil)
	r.waitStep(t, 1, "a")
	r.waitFile(t, "trap1")
	r.waitStep(t, 3, "c")
	l.signal(syscall.SIGINT)
	r.waitFile(t, ".swim/lane1.stop")
	r.waitFile(t, ".swim/lane3.stop")
	r.release("go")
	code := l.wait(t)
	out := l.out.String()
	if code == 0 {
		t.Fatalf("an interrupted run exits non-zero:\n%s", out)
	}
	contains(t, "output", out, "swim: stopping after current steps (2 running). Ctrl-C again to force quit.", "stopping  a (", "stopping  c (")
	log1 := r.log(1)
	contains(t, "agent1.log", log1, "  PASS  a", "  STOP  interrupted by operator (after: a)", "== END INTERRUPTED", "exit=130")
	if strings.Contains(log1, "  PASS  b") {
		t.Error("step b started after the stop request")
	}
	if _, err := os.Stat(filepath.Join(r.root, "int1.txt")); err == nil {
		t.Error("the running step got SIGINT on the first Ctrl-C")
	}
	contains(t, "agent3.log", r.log(3), "  PASS  c", "STOP  interrupted by operator (after: c)")
	if l := r.status().Get(1); l.State != status.Interrupted || *l.ExitCode != 130 {
		t.Errorf("swim 1: %+v", l)
	}
	if l := r.status().Get(2); l.State != status.Skipped || l.Reason != "swim 1 interrupted" {
		t.Errorf("swim 2: %+v", l)
	}
	for _, n := range []int{1, 3} {
		if _, err := os.Stat(filepath.Join(r.root, ".swim", "lane"+string(rune('0'+n))+".stop")); err == nil {
			t.Errorf("stop file for swim %d left behind", n)
		}
	}
}

func TestForceQuit(t *testing.T) {
	r := newRepo(t, "")
	r.mustSwim("config", "interrupt_grace", "600ms")
	// A terraform-like tool: cleans up and exits on its first SIGINT, and
	// counts how many it got.
	// Its cleanup lasts until the test has seen swim's force quit, so a
	// second SIGINT would be counted.
	r.script(1, "tf-like", `run "apply" bash -c 'c=0; trap "c=\$((c+1)); echo \$c > $SWIM_ROOT/ints1.txt; rf=$SWIM_ROOT/tf-done; [ -p \$rf ] || mkfifo \$rf; cat \$rf >/dev/null; exit 1" INT; touch $SWIM_ROOT/trap1; `+fifoCat("31.1")+` & wait'
run "after" true`)
	// Ignores its SIGINT for longer than the grace (for good): killed.
	r.script(2, "stubborn", `run "hang" bash -c 'trap "echo got-int; `+fifoCat("never")+`; exit 3" INT; echo started; touch $SWIM_ROOT/trap2; `+fifoCat("31.2")+` & wait; wait'`)
	l := r.launch(t, nil)
	r.waitStep(t, 1, "apply")
	r.waitStep(t, 2, "hang")
	r.waitFile(t, "trap1")
	r.waitFile(t, "trap2")
	l.signal(syscall.SIGINT)
	l.saw(t, "swim: stopping after current steps")
	// A forwarded Ctrl-C would have been sent before swim said so.
	if data, _ := os.ReadFile(filepath.Join(r.root, "ints1.txt")); len(data) > 0 {
		t.Fatalf("the first Ctrl-C reached the step: %s", data)
	}
	start := time.Now()
	l.signal(syscall.SIGINT)
	l.saw(t, "swim: force quitting")
	waitFor(t, func() bool { data, _ := os.ReadFile(filepath.Join(r.root, "ints1.txt")); return len(data) > 0 })
	r.release("tf-done")
	l.wait(t)
	if took := time.Since(start); took > scaled(3*time.Second) {
		t.Errorf("force quit took %s (grace 600ms)", took)
	}
	out := l.out.String()
	contains(t, "output", out, "swim: force quitting: interrupting 2 lanes, killing in 600ms")
	if data, _ := os.ReadFile(filepath.Join(r.root, "ints1.txt")); strings.TrimSpace(string(data)) != "1" {
		t.Errorf("the tool should get exactly one SIGINT, got %q", data)
	}
	log2 := r.log(2)
	contains(t, "agent2.log", log2, "| got-int", "== END INTERRUPTED", "exit=137")
	for _, n := range []int{1, 2} {
		if st := r.status().Get(n).State; st != status.Interrupted {
			t.Errorf("swim %d = %s", n, st)
		}
	}
	noStray(t, `blocked-31\.[12]`) // this test's own steps: tests run in parallel
}

func TestKillOnThirdCtrlC(t *testing.T) {
	r := newRepo(t, "")
	// It says so once its trap is set (see TestForceQuitKillsStepGroup).
	r.script(1, "stubborn", `run "hang" bash -c 'trap "`+fifoCat("never")+`" INT; echo hang-armed; `+fifoCat("31.3")+` & wait; wait'`)
	r.script(2, "other", `run "x" `+blocked("31.4")+``)
	l := r.launch(t, nil)
	r.waitStep(t, 1, "hang")
	r.waitStep(t, 2, "x")
	l.saw(t, "[1] hang-armed")
	l.signal(syscall.SIGINT)
	l.saw(t, "swim: stopping after current steps")
	l.signal(syscall.SIGINT)
	l.saw(t, "swim: force quitting")
	// Lane 2's step dies of the forwarded SIGINT; lane 1's ignores it. Wait
	// for the launcher to see lane 2's process end ("[2] INTERRUPTED"), not
	// for its status: the lane writes that just before it exits.
	l.saw(t, "[2] INTERRUPTED (")
	start := time.Now()
	l.signal(syscall.SIGINT)
	l.wait(t)
	if took := time.Since(start); took > scaled(2*time.Second) {
		t.Errorf("third Ctrl-C took %s", took)
	}
	contains(t, "output", l.out.String(), "swim: killing 1 lanes now.", "swim summary")
	noStray(t, `blocked-31\.[34]`) // this test's own steps: tests run in parallel
}

// A step stopped by its time limit takes what it started with it: the
// whole process group gets SIGTERM.
func TestStepTimeoutLeavesNoSurvivors(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "forks", `run --timeout 500ms "forks" bash -c '`+fifoCat("33.1")+` & `+fifoCat("33.2")+` & wait'
run "after" true`)
	start := time.Now()
	r.swim("run", "1", "--plain")
	if took := time.Since(start); took > scaled(5*time.Second) {
		t.Errorf("the step limit took %s to stop the step", took)
	}
	contains(t, "agent1.log", r.log(1), "  FAIL  forks (timeout: step limit 500ms)", "  PASS  after")
	noStray(t, `blocked-33\.[12]`) // this test's own steps: tests run in parallel
	if _, err := os.Stat(filepath.Join(r.root, ".swim", "logs", "agent1.log.partial.pgid")); err == nil {
		t.Error("the step's process group record was left behind")
	}
}

// Force quit kills a step's process group too: it isn't in the lane's
// group, and its swim step, killed with the lane, can't stop it.
func TestForceQuitKillsStepGroup(t *testing.T) {
	r := newRepo(t, "")
	r.mustSwim("config", "interrupt_grace", "400ms")
	// The step and its child ignore SIGINT: only the kill stops them.
	// It says so once its trap is set: a SIGINT before that kills bash
	// outright (seen on a loaded macOS runner), and there'd be nothing to kill.
	r.script(1, "deaf", `run "deaf" bash -c 'trap "" INT; echo deaf-armed; `+fifoCat("33.3")+` & wait; wait'`)
	r.script(2, "other", `run "x" `+blocked("33.4")+``)
	l := r.launch(t, nil, "--interrupt", "immediate")
	r.waitStep(t, 1, "deaf")
	r.waitStep(t, 2, "x")
	l.saw(t, "[1] deaf-armed")
	l.signal(syscall.SIGINT)
	l.wait(t)
	contains(t, "output", l.out.String(), "swim: force quitting", "swim: killing 1 lanes now.")
	noStray(t, `blocked-33\.[34]`) // this test's own steps: tests run in parallel
	if _, err := os.Stat(filepath.Join(r.root, ".swim", "logs", "agent1.log.partial.pgid")); err == nil {
		t.Error("the killed step's process group record was left behind")
	}
}

func TestTermEscalatesAfterGrace(t *testing.T) {
	r := newRepo(t, "")
	r.mustSwim("config", "term_grace", "500ms")
	r.script(1, "long", `run "long" `+blocked("31.5")+`
run "never" true`)
	r.script(2, "long too", `run "long" `+blocked("31.6")+``)
	l := r.launch(t, nil)
	r.waitStep(t, 1, "long")
	r.waitStep(t, 2, "long")
	start := time.Now()
	l.signal(syscall.SIGTERM)
	l.wait(t)
	took := time.Since(start)
	if took < 400*time.Millisecond || took > scaled(3*time.Second) {
		t.Errorf("SIGTERM should force after term_grace (500ms); took %s", took)
	}
	contains(t, "output", l.out.String(), "forcing in 500ms (term_grace)", "swim: force quitting")
	if strings.Contains(r.log(1), "PASS  never") || r.status().Get(1).State != status.Interrupted {
		t.Errorf("swim 1 after SIGTERM: %+v", r.status().Get(1))
	}
	noStray(t, `blocked-31\.[56]`) // this test's own steps: tests run in parallel
}

func TestHangupForcesAtOnce(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "long", `run "long" `+blocked("31.7")+``)
	r.script(2, "long too", `run "long" `+blocked("31.8")+``)
	l := r.launch(t, nil)
	r.waitStep(t, 1, "long")
	r.waitStep(t, 2, "long")
	start := time.Now()
	l.signal(syscall.SIGHUP)
	l.wait(t)
	if took := time.Since(start); took > scaled(2*time.Second) {
		t.Errorf("SIGHUP should force at once; took %s", took)
	}
	contains(t, "output", l.out.String(), "swim: force quitting")
	noStray(t, `blocked-31\.[78]`) // this test's own steps: tests run in parallel
}

func TestImmediateInterruptMode(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "long", `run "long" `+blocked("31.9")+``)
	r.script(2, "long too", `run "long" `+blocked("32.1")+``)
	l := r.launch(t, nil, "--interrupt", "immediate")
	r.waitStep(t, 1, "long")
	r.waitStep(t, 2, "long")
	l.signal(syscall.SIGINT)
	l.wait(t)
	contains(t, "output", l.out.String(), "swim: force quitting")
	contains(t, "agent1.log", r.log(1), "FAIL  long (exit 130, interrupted)")
	noStray(t, `blocked-3(1\.9|2\.1)`) // this test's own steps
}

func TestStopDuringRetryWait(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "backoff", `run --retry 3 --backoff 20s "fails" false
run "never" true`)
	r.script(2, "other", `run "short" true`)
	l := r.launch(t, nil)
	waitFor(t, func() bool {
		data, _ := os.ReadFile(filepath.Join(r.root, ".swim", "logs", "agent1.log.partial"))
		return strings.Contains(string(data), "retry in")
	})
	start := time.Now()
	l.signal(syscall.SIGINT)
	l.wait(t)
	if took := time.Since(start); took > scaled(3*time.Second) {
		t.Errorf("a stop during the backoff took %s", took)
	}
	log := r.log(1)
	contains(t, "agent1.log", log, "-- stop requested (operator interrupt): no retry", "STOP  interrupted by operator (after: fails)", "== END INTERRUPTED")
	if strings.Contains(log, "attempt 2/4") || strings.Contains(log, "never") {
		t.Errorf("retried or continued after the stop:\n%s", log)
	}
}

func TestStopCancelsConfirm(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "asks", `run "before" true
confirm "really?"
run "after" true`)
	pr, pw, err := os.Pipe() // stdin that never answers
	if err != nil {
		t.Fatal(err)
	}
	defer pw.Close()
	defer pr.Close()
	l := start(t, r.cmd(bin, "run", "1", "--plain"), pr)
	// The prompt has no newline, so it isn't relayed: wait for the step
	// before it. Whether bash is at the read yet or not, the stop request
	// ends the round there: confirm reads in slices and checks for it after
	// each.
	waitFor(t, func() bool { return strings.Contains(r.log(1), "PASS  before") })
	l.signal(syscall.SIGINT)
	l.wait(t)
	log := r.log(1)
	contains(t, "agent1.log", log, "STOP  interrupted by operator (after: before)", "== END INTERRUPTED")
	if strings.Contains(log, "PASS  after") {
		t.Error("the round continued past confirm")
	}
}

func TestInterruptOneLane(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "stop me", `run "a" `+hold("go")+`
run "b" true`)
	r.script(2, "keep going", `run "x" `+hold("go")+`
run "y" true`)
	l := r.launch(t, nil)
	r.waitStep(t, 1, "a")
	r.waitStep(t, 2, "x")
	contains(t, "swim interrupt", r.mustSwim("interrupt", "1"), "swim 1: stopping after a")
	r.release("go")
	l.wait(t)
	contains(t, "agent1.log", r.log(1), "PASS  a", "STOP  interrupted by operator (after: a)")
	contains(t, "agent2.log", r.log(2), "PASS  y")
	if st := r.status().Get(1).State; st != status.Interrupted {
		t.Errorf("swim 1 = %s", st)
	}
	if st := r.status().Get(2).State; st != status.Passed {
		t.Errorf("swim 2 = %s", st)
	}
	if out, code := r.swim("interrupt", "2"); code == 0 || !strings.Contains(out, "isn't running") {
		t.Errorf("interrupting an idle lane: %d %s", code, out)
	}
}
