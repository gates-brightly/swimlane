package step

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// alive reports whether pid is a running process (a zombie, waiting for
// whoever adopted it to reap it, counts as gone).
func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false // ps lists nothing: gone
	}
	return !strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}

// waitGone fails unless pid is gone within a few seconds.
func waitGone(t *testing.T, pid int, what string) {
	t.Helper()
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(25 * time.Millisecond) {
		if !alive(pid) {
			return
		}
	}
	syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("%s (pid %d) survived", what, pid)
}

// readPid waits for the step to write a pid to file p.
func readPid(t *testing.T, p string) int {
	t.Helper()
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		data, _ := os.ReadFile(p)
		if n, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && n > 0 {
			return n
		}
	}
	t.Fatalf("no pid in %s", p)
	return 0
}

func TestStepTimeoutKillsItsChildren(t *testing.T) {
	cases := []struct{ name, script string }{
		// The background sleep gets the group's SIGTERM.
		{"forked child", `sleep 999 & echo $! > p; wait`},
		// The step ignores SIGTERM: SIGKILL after the grace.
		{"step ignores TERM", `trap "" TERM; sleep 999 & echo $! > p; wait`},
		// The step dies on SIGTERM but its child ignores it: the group is
		// killed after the grace even though the step's own process is gone.
		{"child ignores TERM", `bash -c 'trap "" TERM; echo $$ > p; sleep 999; :' & wait`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o, _, _ := newOpts(t)
			dir := filepath.Dir(o.LogPath)
			o.Args = []string{"bash", "-c", "cd " + Quote([]string{dir}) + " || exit; " + c.script}
			o.StepTimeout, o.StepTimeoutText = 300*time.Millisecond, "300ms"
			o.KillGrace = 300 * time.Millisecond
			start := time.Now()
			res, err := Run(o)
			if err != nil {
				t.Fatal(err)
			}
			if !res.TimedOut || res.ExitCode != 124 {
				t.Fatalf("result %+v", res)
			}
			if took := time.Since(start); took > 3*time.Second {
				t.Errorf("took %s (limit 300ms, grace 300ms)", took)
			}
			waitGone(t, readPid(t, filepath.Join(dir, "p")), "the step's child")
			if _, err := os.Stat(GroupPath(o.LogPath)); err == nil {
				t.Error("the group record was left behind")
			}
		})
	}
}

// A step that ends on its own leaves what it started running (a server it
// backgrounds on purpose): only a stopped step's group is killed.
func TestStepThatSucceedsKeepsItsChildren(t *testing.T) {
	o, _, _ := newOpts(t)
	dir := filepath.Dir(o.LogPath)
	o.Args = []string{"bash", "-c", "cd " + Quote([]string{dir}) + " || exit; sleep 999 >/dev/null 2>&1 & echo $! > p"}
	res, err := Run(o)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	pid := readPid(t, filepath.Join(dir, "p"))
	defer syscall.Kill(pid, syscall.SIGKILL)
	if !alive(pid) {
		t.Fatal("a successful step's background process was killed")
	}
	RecoverSpool(o.LogPath) // the next step starts: nothing recorded to kill
	time.Sleep(50 * time.Millisecond)
	if !alive(pid) {
		t.Fatal("the next step killed a finished step's background process")
	}
}

func TestKillRecorded(t *testing.T) {
	start := func(t *testing.T) *exec.Cmd {
		c := exec.Command("sleep", "999")
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		go c.Wait()
		t.Cleanup(func() { c.Process.Kill() })
		return c
	}
	log := filepath.Join(t.TempDir(), "agent1.log")

	// swim step was killed: RecoverSpool kills the group it recorded.
	c := start(t)
	recordGroup(log, c.Process.Pid)
	RecoverSpool(log)
	waitGone(t, c.Process.Pid, "the recorded group")
	if _, err := os.Stat(GroupPath(log)); err == nil {
		t.Error("the record was not removed")
	}

	// A record whose start time doesn't match is another process's pid.
	c = start(t)
	os.WriteFile(GroupPath(log), []byte(fmt.Sprintf("%d 12345\n", c.Process.Pid)), 0o644)
	if KillRecorded(log) {
		t.Error("killed a group whose start time doesn't match")
	}
	time.Sleep(50 * time.Millisecond)
	if !alive(c.Process.Pid) {
		t.Error("a process with another start time was killed")
	}
	if KillRecorded(log) {
		t.Error("no record: nothing to kill")
	}
}
