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

	"github.com/gates-brightly/swimlane/internal/testwait"
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

// waitGone fails unless pid is gone soon (generous: -race on a loaded
// runner is slow).
func waitGone(t *testing.T, pid int, what string) {
	t.Helper()
	if testwait.Exited(pid, testwait.Deadline) || !alive(pid) {
		return
	}
	syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("%s (pid %d) survived", what, pid)
}

// readPid waits for the step to write a pid to file p.
func readPid(t *testing.T, p string) int {
	t.Helper()
	w, err := testwait.Watch(filepath.Dir(p), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	pid := 0
	testwait.Until(t, "a pid in "+p, w, func() bool {
		data, _ := os.ReadFile(p)
		n, err := strconv.Atoi(strings.TrimSpace(string(data)))
		pid = n
		return err == nil && n > 0
	})
	return pid
}

// forever is a bash fragment that runs until it's killed: cat blocks
// opening a FIFO nobody writes (in the current directory).
const forever = `{ [ -p never ] || mkfifo never; cat never; }`

func TestStepTimeoutKillsItsChildren(t *testing.T) {
	cases := []struct{ name, script string }{
		// The background sleep gets the group's SIGTERM.
		{"forked child", forever + ` & echo $! > p; wait`},
		// The step ignores SIGTERM: SIGKILL after the grace.
		{"step ignores TERM", `trap "" TERM; ` + forever + ` & echo $! > p; wait`},
		// The step dies on SIGTERM but its child ignores it: the group is
		// killed after the grace even though the step's own process is gone.
		{"child ignores TERM", `bash -c 'trap "" TERM; echo $$ > p; ` + forever + `; :' & wait`},
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
			if took := time.Since(start); took > 3*time.Second*testwait.Slow {
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
	o.Args = []string{"bash", "-c", "cd " + Quote([]string{dir}) + " || exit; " + forever + " >/dev/null 2>&1 & echo $! > p"}
	res, err := Run(o)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	pid := readPid(t, filepath.Join(dir, "p"))
	defer syscall.Kill(pid, syscall.SIGKILL)
	if !alive(pid) {
		t.Fatal("a successful step's background process was killed")
	}
	// The next step starts: the step ended on its own, so no group is
	// recorded, and RecoverSpool has nothing to kill.
	if _, err := os.Stat(GroupPath(o.LogPath)); err == nil {
		t.Fatal("a finished step left its group recorded")
	}
	RecoverSpool(o.LogPath)
	if !alive(pid) {
		t.Fatal("the next step killed a finished step's background process")
	}
}

func TestKillRecorded(t *testing.T) {
	start := func(t *testing.T) *exec.Cmd {
		c := exec.Command("cat") // runs until killed: its stdin stays open
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if _, err := c.StdinPipe(); err != nil {
			t.Fatal(err)
		}
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
	if KillRecorded(log) { // false: it sent nothing
		t.Error("killed a group whose start time doesn't match")
	}
	if !alive(c.Process.Pid) {
		t.Error("a process with another start time was killed")
	}
	if KillRecorded(log) {
		t.Error("no record: nothing to kill")
	}
}
