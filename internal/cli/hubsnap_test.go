package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/hub"
	"github.com/gates-brightly/swimlane/internal/queue"
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

// The hub sees schedules: a scheduled batch with its time, a batch it
// holds, and a recurring schedule's template.
func TestHubSnapshotSchedules(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "lane.1.sh"), []byte("#!/usr/bin/env bash\n# Round: upstream\nrun \"x\" true\n"), 0o755)
	os.WriteFile(filepath.Join(root, "lane.2.sh"), []byte("#!/usr/bin/env bash\n# Round: downstream\n# After: 1\nrun \"x\" true\n"), 0o755)
	cfg := &config.Config{Root: root, Lanes: 2}
	at := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	queue.Add(root, &queue.Entry{ID: "q-sched", By: "op", NotBefore: at, Zone: "UTC", Lanes: []queue.Lane{{Lane: 1, Job: "j1"}}})
	queue.Add(root, &queue.Entry{ID: "q-after", By: "op", Lanes: []queue.Lane{{Lane: 2, Job: "j2"}}})
	(&recurring{ID: "r-daily", Lanes: []queue.Lane{{Lane: 1}}, Clock: "06:00:00", Zone: "UTC", First: "2026-10-16", Until: "2099-01-01",
		path: filepath.Join(recurringDir(root), "r-daily.yml")}).save()
	s := hubSnapshot(root, cfg, "w", "idle", time.Second)
	byID := map[string]hub.Batch{}
	for _, b := range s.Queue {
		byID[b.ID] = b
	}
	if b := byID["q-sched"]; b.State != "scheduled" || !strings.Contains(b.At, "(UTC) · in ") {
		t.Errorf("scheduled batch: %+v", b)
	}
	if b := byID["q-after"]; b.State != "held" || b.HeldBy != "#1" {
		t.Errorf("held batch: %+v", b)
	}
	if b := byID["r-daily"]; b.State != "daily" || !strings.Contains(b.Daily, "daily at 06:00:00(UTC)") {
		t.Errorf("recurring: %+v", b)
	}
}
