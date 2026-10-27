package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/queue"
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
	set(4, func(l *status.Lane) { l.State, l.WaitingOn = status.Waiting, []int{1} })
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
}

// q in swim watch adds a lane to the next batch and s submits them as one
// batch, as swim queue add 1 2 does: one run, so After: between them holds.
// q again takes a lane out; x clears; nothing is queued before s.
func TestWatchStagesOneBatch(t *testing.T) {
	root := t.TempDir()
	for n := 1; n <= 3; n++ {
		os.WriteFile(filepath.Join(root, "lane."+string(rune('0'+n))+".sh"),
			[]byte("#!/usr/bin/env bash\n# swim: syntax 4\n# Round: r"+string(rune('0'+n))+"\n"), 0o755)
	}
	w := &watchView{root: root, cfg: &config.Config{Root: root, Settings: config.Settings{Lanes: 3}}}
	queued := func() []*queue.Entry {
		es, err := queue.List(root, false)
		if err != nil {
			t.Fatal(err)
		}
		return es
	}

	w.act("lane:1", 'q')
	w.act("lane:3", 'q')
	w.act("lane:2", 'q')
	w.act("lane:3", 'q') // out again
	if got := joinNums(w.staged); got != "1, 2" {
		t.Fatalf("staged %q", got)
	}
	if len(queued()) != 0 {
		t.Fatal("q queued something before s")
	}
	rows := map[string]string{}
	for _, r := range w.rows() {
		rows[r.ID] = r.Text
	}
	if !strings.Contains(rows["submit"], "submit swim 1, 2 as one batch") ||
		!strings.Contains(rows["lane:1"], "[+ next batch]") || strings.Contains(rows["lane:3"], "[+ next batch]") {
		t.Fatalf("rows: %v", rows)
	}

	if msg := w.act("lane:3", 's'); !strings.HasPrefix(msg, "queued swim 1,2 as #1") {
		t.Fatalf("submit: %s", msg)
	}
	es := queued()
	if len(es) != 1 || len(es[0].Lanes) != 2 || es[0].Lanes[0].Lane != 1 || es[0].Lanes[1].Lane != 2 {
		t.Fatalf("want one batch of swim 1, 2: %+v", es)
	}
	if len(w.staged) != 0 {
		t.Errorf("still staged after submit: %v", w.staged)
	}
	if msg := w.act("lane:1", 's'); !strings.Contains(msg, "no batch to submit") {
		t.Errorf("empty submit: %s", msg)
	}

	// Enter on the submit row submits too; x clears without queueing.
	w.act("lane:3", 'q')
	if msg := w.act("submit", display.PickEnter); !strings.HasPrefix(msg, "queued swim 3 as #2") {
		t.Errorf("enter on submit: %s", msg)
	}
	w.act("lane:1", 'q')
	if msg := w.act("lane:1", 'x'); !strings.Contains(msg, "cleared the next batch (swim 1)") || len(w.staged) != 0 || len(queued()) != 2 {
		t.Errorf("x: %s, staged %v, %d queued", msg, w.staged, len(queued()))
	}
}
