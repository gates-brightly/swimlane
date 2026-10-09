package e2e

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/status"
)

// launched is a `swim all` started in its own process group, like a
// terminal's foreground job, so a signal to the group is what the terminal
// sends on Ctrl-C.
type launched struct {
	c    *exec.Cmd
	out  *bytes.Buffer
	done chan error
}

func (r *repo) launch(t *testing.T, stdin io.Reader, args ...string) *launched {
	t.Helper()
	c := r.cmd(bin, append([]string{"all", "--plain"}, args...)...)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out bytes.Buffer
	c.Stdout, c.Stderr, c.Stdin = &out, &out, stdin
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	l := &launched{c: c, out: &out, done: make(chan error, 1)}
	go func() { l.done <- c.Wait() }()
	t.Cleanup(func() { syscall.Kill(-c.Process.Pid, syscall.SIGKILL) })
	return l
}

// signal sends sig to swim's process group, as the terminal does.
func (l *launched) signal(sig syscall.Signal) { syscall.Kill(-l.c.Process.Pid, sig) }

func (l *launched) wait(t *testing.T, within time.Duration) int {
	t.Helper()
	select {
	case err := <-l.done:
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		return 0
	case <-time.After(within):
		t.Fatalf("swim didn't finish within %s:\n%s", within, l.out.String())
	}
	return -1
}

func (r *repo) waitStep(t *testing.T, n int, label string) {
	t.Helper()
	waitFor(t, func() bool { return r.status().Get(n).CurrentStep == label })
	time.Sleep(150 * time.Millisecond) // the step command is running
}

// noStray fails if a process whose command line holds marker survives.
func noStray(t *testing.T, marker string) {
	t.Helper()
	time.Sleep(200 * time.Millisecond)
	if out, err := exec.Command("pgrep", "-f", marker).Output(); err == nil && len(bytes.TrimSpace(out)) > 0 {
		t.Errorf("processes matching %q survived: %s", marker, out)
	}
}

func TestGracefulInterrupt(t *testing.T) {
	r := newRepo(t, "")
	// Step "a" notes any SIGINT it gets; it must get none.
	r.script(1, "graceful", `run "a" bash -c 'trap "echo got > int1.txt" INT; sleep 1.5 & wait; true'
run "b" true`)
	r.script(2, "after 1", `run "x" true`)
	r.script(3, "other", `run "c" sleep 1.2
run "d" true`)
	withHeader(t, r, 2, "# After: 1")
	l := r.launch(t, nil)
	r.waitStep(t, 1, "a")
	r.waitStep(t, 3, "c")
	l.signal(syscall.SIGINT)
	code := l.wait(t, 10*time.Second)
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
	r.script(1, "tf-like", `run "apply" bash -c 'c=0; trap "c=\$((c+1)); echo \$c > ints1.txt; sleep 0.2; exit 1" INT; sleep 31.1 & wait'
run "after" true`)
	// Ignores its SIGINT for longer than the grace: killed.
	r.script(2, "stubborn", `run "hang" bash -c 'trap "echo got-int; sleep 5; exit 3" INT; echo started; sleep 31.2 & wait; wait'`)
	l := r.launch(t, nil)
	r.waitStep(t, 1, "apply")
	r.waitStep(t, 2, "hang")
	l.signal(syscall.SIGINT)
	time.Sleep(300 * time.Millisecond)
	if data, _ := os.ReadFile(filepath.Join(r.root, "ints1.txt")); len(data) > 0 {
		t.Fatalf("the first Ctrl-C reached the step: %s", data)
	}
	start := time.Now()
	l.signal(syscall.SIGINT)
	l.wait(t, 5*time.Second)
	if took := time.Since(start); took > 3*time.Second {
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
	noStray(t, "sleep 31.")
}

func TestKillOnThirdCtrlC(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "stubborn", `run "hang" bash -c 'trap "sleep 20" INT; sleep 31.3 & wait; wait'`)
	r.script(2, "other", `run "x" sleep 31.4`)
	l := r.launch(t, nil)
	r.waitStep(t, 1, "hang")
	r.waitStep(t, 2, "x")
	l.signal(syscall.SIGINT)
	time.Sleep(200 * time.Millisecond)
	l.signal(syscall.SIGINT)
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	l.signal(syscall.SIGINT)
	l.wait(t, 4*time.Second)
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("third Ctrl-C took %s", took)
	}
	contains(t, "output", l.out.String(), "swim: killing 1 lanes now.", "swim summary")
	noStray(t, "sleep 31.")
}

func TestTermEscalatesAfterGrace(t *testing.T) {
	r := newRepo(t, "")
	r.mustSwim("config", "term_grace", "500ms")
	r.script(1, "long", `run "long" sleep 31.5
run "never" true`)
	r.script(2, "long too", `run "long" sleep 31.6`)
	l := r.launch(t, nil)
	r.waitStep(t, 1, "long")
	r.waitStep(t, 2, "long")
	start := time.Now()
	l.signal(syscall.SIGTERM)
	l.wait(t, 5*time.Second)
	took := time.Since(start)
	if took < 400*time.Millisecond || took > 3*time.Second {
		t.Errorf("SIGTERM should force after term_grace (500ms); took %s", took)
	}
	contains(t, "output", l.out.String(), "forcing in 500ms (term_grace)", "swim: force quitting")
	if strings.Contains(r.log(1), "PASS  never") || r.status().Get(1).State != status.Interrupted {
		t.Errorf("swim 1 after SIGTERM: %+v", r.status().Get(1))
	}
	noStray(t, "sleep 31.")
}

func TestHangupForcesAtOnce(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "long", `run "long" sleep 31.7`)
	r.script(2, "long too", `run "long" sleep 31.8`)
	l := r.launch(t, nil)
	r.waitStep(t, 1, "long")
	r.waitStep(t, 2, "long")
	start := time.Now()
	l.signal(syscall.SIGHUP)
	l.wait(t, 5*time.Second)
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("SIGHUP should force at once; took %s", took)
	}
	contains(t, "output", l.out.String(), "swim: force quitting")
	noStray(t, "sleep 31.")
}

func TestImmediateInterruptMode(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "long", `run "long" sleep 31.9`)
	r.script(2, "long too", `run "long" sleep 32.1`)
	l := r.launch(t, nil, "--interrupt", "immediate")
	r.waitStep(t, 1, "long")
	r.waitStep(t, 2, "long")
	l.signal(syscall.SIGINT)
	l.wait(t, 5*time.Second)
	contains(t, "output", l.out.String(), "swim: force quitting")
	contains(t, "agent1.log", r.log(1), "FAIL  long (exit 130, interrupted)")
	noStray(t, "sleep 3")
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
	l.wait(t, 5*time.Second)
	if took := time.Since(start); took > 3*time.Second {
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
	c := r.cmd(bin, "run", "1", "--plain")
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out bytes.Buffer
	c.Stdout, c.Stderr, c.Stdin = &out, &out, pr
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	l := &launched{c: c, out: &out, done: make(chan error, 1)}
	go func() { l.done <- c.Wait() }()
	// The prompt has no newline, so it isn't relayed yet: wait for the step
	// before it, then for bash to reach the read.
	waitFor(t, func() bool { return strings.Contains(r.log(1), "PASS  before") })
	time.Sleep(300 * time.Millisecond)
	l.signal(syscall.SIGINT)
	l.wait(t, 5*time.Second)
	log := r.log(1)
	contains(t, "agent1.log", log, "STOP  interrupted by operator (after: before)", "== END INTERRUPTED")
	if strings.Contains(log, "PASS  after") {
		t.Error("the round continued past confirm")
	}
}

func TestInterruptOneLane(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "stop me", `run "a" sleep 1
run "b" true`)
	r.script(2, "keep going", `run "x" sleep 1
run "y" true`)
	l := r.launch(t, nil)
	r.waitStep(t, 1, "a")
	r.waitStep(t, 2, "x")
	contains(t, "swim interrupt", r.mustSwim("interrupt", "1"), "swim 1: stopping after a")
	l.wait(t, 10*time.Second)
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
