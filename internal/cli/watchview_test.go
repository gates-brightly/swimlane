package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/status"
)

// A lane row in swim watch says what the lane waits on: the lanes (as
// swim status says it), the lock, how many are ahead.
func TestWatchRowsSayWhatLanesWaitOn(t *testing.T) {
	root := t.TempDir()
	for n := 1; n <= 5; n++ {
		os.WriteFile(filepath.Join(root, "lane."+string(rune('0'+n))+".sh"), []byte("#!/usr/bin/env bash\n# Round: r\n"), 0o755)
	}
	pid := os.Getpid()
	set := func(n int, fn func(l *status.Lane)) {
		if err := status.Update(root, n, 5, func(l *status.Lane) { l.PID = pid; fn(l) }); err != nil {
			t.Fatal(err)
		}
	}
	set(1, func(l *status.Lane) { l.State, l.WaitingOn = status.Waiting, []int{2, 3} })
	set(2, func(l *status.Lane) { l.State, l.WaitingOn, l.Reason = status.Locked, []int{}, "waiting for lock db" })
	set(3, func(l *status.Lane) { l.State, l.WaitingOn, l.Reason = status.Queued, []int{}, "2 ahead" })
	set(4, func(l *status.Lane) {
		l.State = status.Waiting
		l.Cells = []status.Lane{{Cell: "a", State: status.Waiting, WaitingOn: []int{1}}, {Cell: "b", State: status.Waiting, WaitingOn: []int{1}}}
	})
	set(5, func(l *status.Lane) { l.State, l.WaitingOn, l.PID = status.Waiting, []int{1}, 0 }) // stale: its process is gone

	w := &watchView{root: root, cfg: &config.Config{Settings: config.Settings{Lanes: 5}}}
	row := map[string]string{}
	for _, r := range w.rows() {
		row[r.ID] = r.Text
	}
	for id, want := range map[string]string{
		"lane:1": "· " + display.WaitingText([]int{2, 3}),
		"lane:2": "· waiting for lock db",
		"lane:3": "· 2 ahead",
		"lane:4": "· waiting on swim 1",
	} {
		if !strings.HasSuffix(row[id], want) {
			t.Errorf("%s: %q, want it to end %q", id, row[id], want)
		}
	}
	if !strings.Contains(row["lane:1"], "waiting on swim 2, 3") {
		t.Errorf("lane 1: %q", row["lane:1"])
	}
	if strings.Contains(row["lane:5"], "·") || !strings.Contains(row["lane:5"], "stale") {
		t.Errorf("stale lane 5: %q", row["lane:5"])
	}
	if got := laneRowDetail(&status.Lane{Cells: []status.Lane{{State: status.Waiting, WaitingOn: []int{1}}, {State: status.Waiting, WaitingOn: []int{2}}}}, status.Waiting); got != "cells wait on different lanes" {
		t.Errorf("disagreeing cells: %q", got)
	}
}
