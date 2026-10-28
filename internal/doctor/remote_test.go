package doctor

import (
	"os"
	"path/filepath"
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

// A require: rule with evidence: true is warned about: nothing can
// approve its guards.
func TestCheckEvidenceRules(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "lookup.yml"), []byte("require:\n  - guards: [ORDERS_*]\n    evidence: true\n  - count: 1\n"), 0o644)
	got := checkEvidenceRules(&Options{Root: root})
	if len(got) != 1 || got[0].Code != "evidence-rule" || !strings.Contains(got[0].Message, "a require: rule for ORDERS_* says evidence: true") {
		t.Fatalf("findings: %+v", got)
	}
	if got := checkEvidenceRules(&Options{Root: t.TempDir()}); len(got) != 0 {
		t.Errorf("no lookup.yml: %+v", got)
	}
}
