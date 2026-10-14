package launcher

import (
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
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
