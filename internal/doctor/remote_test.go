package doctor

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/status"
)

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
