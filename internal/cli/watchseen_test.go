package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func writeLane(t *testing.T, root string, n int, body string) {
	t.Helper()
	path := filepath.Join(root, "lane."+string(rune('0'+n))+".sh")
	if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\n# Round: r\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// The first swim watch takes every lane as seen; a lane changed or added
// after that gets (~) until the operator acts on it, and a restarted
// watch still knows.
func TestWatchSeen(t *testing.T) {
	root := t.TempDir()
	writeLane(t, root, 1, "echo one")
	writeLane(t, root, 2, "echo two")

	s := loadWatchSeen(root)
	s.refresh([]int{1, 2})
	if s.isChanged(1) || s.isChanged(2) {
		t.Fatal("the first watch marked a lane")
	}
	if _, err := os.Stat(watchSeenPath(root)); err != nil {
		t.Fatalf("the first watch didn't record what it saw: %v", err)
	}

	writeLane(t, root, 2, "echo two, rewritten")
	writeLane(t, root, 3, "echo three")
	s = loadWatchSeen(root) // a restarted watch
	s.refresh([]int{1, 2, 3})
	if s.isChanged(1) || !s.isChanged(2) || !s.isChanged(3) {
		t.Fatalf("changed = %v, want lanes 2 (rewritten) and 3 (new)", s.changed)
	}

	s.touch(2)
	if s.isChanged(2) {
		t.Error("acting on lane 2 left its mark")
	}
	s = loadWatchSeen(root)
	s.refresh([]int{1, 2, 3})
	if s.isChanged(2) || !s.isChanged(3) {
		t.Errorf("after a restart, changed = %v, want only lane 3", s.changed)
	}

	var none *watchSeen // a view built without it marks nothing
	none.refresh([]int{1})
	none.touch(1)
	if none.isChanged(1) {
		t.Error("a nil watchSeen marked a lane")
	}
}
