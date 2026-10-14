package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/status"
)

// The hub sees what -y approved: each lane's unprompted guards, and a
// worker that approves every guard (swim watch -y).
func TestHubSnapshotUnprompted(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "lane.1.sh"), []byte("#!/usr/bin/env bash\n# Round: deploy\nrun \"x\" true\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	status.Update(root, 1, 1, func(l *status.Lane) {
		l.State = status.Passed
		l.Unprompted("DEPLOY", "swim run -y")
		l.Unprompted("SEED", "")
	})
	watchYes = true
	defer func() { watchYes = false }()
	s := hubSnapshot(root, &config.Config{Lanes: 1}, "w", "idle", time.Second)
	if s.AutoApprove != "swim watch -y" {
		t.Errorf("auto_approve %q", s.AutoApprove)
	}
	if len(s.Lanes) != 1 {
		t.Fatalf("lanes: %+v", s.Lanes)
	}
	l := s.Lanes[0]
	if strings.Join(l.AutoApproved, " ") != "DEPLOY" || l.AutoVia != "swim run -y" || strings.Join(l.FlagApproved, " ") != "SEED" {
		t.Errorf("lane: %+v", l)
	}
}
