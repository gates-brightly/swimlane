package doctor

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/status"
)

// A lane paused past pause_warn, and a repo-wide pause that long, are
// warned about; a short pause isn't.
func TestCheckPauseLong(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-5 * time.Hour).UTC().Format(time.RFC3339)
	recent := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	status.UpdateFile(root, 2, func(f *status.File) error {
		a := f.Get(1)
		a.State, a.PID, a.PausedAt, a.PausedBy, a.PausedAfter, a.Locks = status.Running, os.Getpid(), old, "zach", "tf plan", []string{"orders"}
		b := f.Get(2)
		b.State, b.PID, b.PausedAt = status.Running, os.Getpid(), recent
		return nil
	})
	lane.RequestRepoPause(root, lane.PauseReq{By: "zach", At: old})
	got := checkPauseLong(&Options{Root: root, Cfg: &config.Config{}})
	if len(got) != 2 {
		t.Fatalf("findings: %+v", got)
	}
	if !strings.Contains(got[0].Message, "swim 1 has been paused for 5h") || !strings.Contains(got[0].Message, "locks orders") {
		t.Errorf("lane finding: %s", got[0].Message)
	}
	if !strings.Contains(got[1].Message, "the repo has been paused") {
		t.Errorf("repo finding: %s", got[1].Message)
	}
	if got := checkPauseLong(&Options{Root: root, Cfg: &config.Config{Settings: config.Settings{PauseWarn: "6h"}}}); len(got) != 0 {
		t.Errorf("with pause_warn 6h: %+v", got)
	}
}

// A lane running on another machine for over a day is warned about; one
// started an hour ago, or on this machine, isn't.
func TestCheckRemoteRunning(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-30 * time.Hour).UTC().Format(time.RFC3339)
	recent := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	status.UpdateFile(root, 3, func(f *status.File) error {
		a := f.Get(1)
		a.State, a.PID, a.Host, a.StartedAt = status.Running, 4242, "container-1", &old
		b := f.Get(2)
		b.State, b.PID, b.Host, b.StartedAt = status.Running, 4243, "container-1", &recent
		c := f.Get(3)
		c.State, c.PID, c.StartedAt = status.Running, os.Getpid(), &old
		return nil
	})
	got := checkRemoteRunning(&Options{Root: root})
	if len(got) != 1 || !strings.Contains(got[0].Message, "swim 1 has been running on container-1 for 30h") || !strings.Contains(got[0].Fix, "rm .swim/lane1.pid") {
		t.Fatalf("findings: %+v", got)
	}
}
