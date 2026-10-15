package launcher

import (
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/proc"
	"github.com/gates-brightly/swimlane/internal/status"
)

// ReapDead ends lanes whose process is gone and leaves live ones alone; a
// matrix lane is ended only once its swim run and every cell are gone.
// (TestReapDeadLane in internal/e2e covers a killed round with a log.)
func TestReapDead(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{Settings: config.Settings{Lanes: 4}}
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Skip(err)
	}
	dead := gone.Process.Pid
	status.UpdateFile(root, 4, func(f *status.File) error {
		f.Get(1).State, f.Get(1).PID = status.Running, dead // dead, no pid file
		f.Get(2).State, f.Get(2).PID = status.Running, os.Getpid()
		l := f.Get(3)
		l.State, l.PID = status.Running, dead // a matrix lane whose swim run died
		l.CellEntry("a").State = status.Running
		l.CellEntry("b").State = status.Waiting
		l.CellEntry("c").State = status.Passed
		m := f.Get(4)
		m.State, m.PID = status.Running, dead // its launcher died, but a cell lives
		m.CellEntry("x").State = status.Running
		return nil
	})
	if err := lane.WritePID(root, 2, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(fmt.Sprintf("%s/.swim/lane4.x.pid", root), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644)
	// A pid file whose pid was handed to another process doesn't count.
	os.WriteFile(lane.PIDFile(root, 1), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644)
	os.WriteFile(fmt.Sprintf("%s/.swim/lane1.claim", root), []byte(fmt.Sprintf("%d 1\n", os.Getpid())), 0o644)

	got := ReapDead(root, cfg, "")
	if len(got) != 3 || got[0].Lane != 1 || got[1].Lane != 3 || got[1].Cell != "a" || got[2].Lane != 3 || got[2].Cell != "" {
		t.Fatalf("reaped %+v", got)
	}
	st, _ := status.Load(root)
	if l := st.Get(1); l.State != status.Interrupted || l.PID != 0 {
		t.Errorf("lane 1: %s pid %d", l.State, l.PID)
	}
	if l := st.Get(2); l.State != status.Running {
		t.Errorf("live lane 2 reaped: %s", l.State)
	}
	l := st.Get(3)
	if l.State != status.Interrupted || l.FindCell("a").State != status.Interrupted || l.FindCell("b").State != status.Skipped || l.FindCell("c").State != status.Passed {
		t.Errorf("matrix lane 3: %s %s", l.State, l.CellsText())
	}
	if l := st.Get(4); l.State != status.Running {
		t.Errorf("matrix lane 4 with a live cell reaped: %s", l.State)
	}
	if again := ReapDead(root, cfg, ""); len(again) != 0 {
		t.Errorf("reaped again: %+v", again)
	}
}

// Two machines can share a repo (a laptop and a container mounting it). A
// lane whose process runs on the other one can't be seen from here, so
// it's never reaped as dead: not by the host status.yml names, nor by the
// one in its claim. A dead lane of this machine still is.
func TestReapDeadOtherHost(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{Settings: config.Settings{Lanes: 3}}
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Skip(err)
	}
	dead := gone.Process.Pid
	status.UpdateFile(root, 3, func(f *status.File) error {
		f.Get(1).State, f.Get(1).PID, f.Get(1).Host = status.Running, dead, "another-machine"
		f.Get(2).State, f.Get(2).PID, f.Get(2).Host = status.Running, dead, proc.Host()
		f.Get(3).State, f.Get(3).PID = status.Running, dead // an older status.yml: no host
		return nil
	})
	os.MkdirAll(root+"/.swim", 0o755)
	os.WriteFile(lane.PIDFile(root, 3), []byte(fmt.Sprintf("%d\n", dead)), 0o644)
	os.WriteFile(fmt.Sprintf("%s/.swim/lane3.claim", root), []byte(fmt.Sprintf("%d 12345 another-machine\n", dead)), 0o644)

	if pid, host, ok := lane.RunningOn(root, 3); !ok || pid != dead || host != "another-machine" {
		t.Errorf("RunningOn(3) = %d %q %v", pid, host, ok)
	}
	got := ReapDead(root, cfg, "")
	if len(got) != 1 || got[0].Lane != 2 {
		t.Fatalf("reaped %+v, want only swim 2", got)
	}
	st, _ := status.Load(root)
	for _, n := range []int{1, 3} {
		if l := st.Get(n); l.State != status.Running {
			t.Errorf("swim %d on another machine reaped: %s", n, l.State)
		}
	}
	if l := st.Get(1); !l.Alive() {
		t.Error("swim 1, on another machine, isn't counted alive")
	}
	if l := st.Get(2); l.State != status.Interrupted || l.Host != "" {
		t.Errorf("swim 2: %s host %q", l.State, l.Host)
	}
}
