package runrec

import (
	"testing"
	"time"
)

func TestWriteLoadList(t *testing.T) {
	root := t.TempDir()
	start := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	exit := 0
	for i, run := range []string{"r-20261014T010000Z-aaaa", "r-20261014T020000Z-bbbb"} {
		r := &Run{Run: run, Started: start.Add(time.Duration(i) * time.Second), Finished: start.Add(time.Duration(i+5) * time.Second),
			Lanes: []Lane{{Lane: 1, State: "passed", Exit: &exit, Start: Since(start, start.Add(time.Second))}}}
		if err := Write(root, r); err != nil {
			t.Fatal(err)
		}
	}
	got, ok, err := Load(root, "r-20261014T020000Z-bbbb")
	if err != nil || !ok || len(got.Lanes) != 1 || *got.Lanes[0].Start != 1 || got.Schema == "" {
		t.Fatalf("load: %+v %v %v", got, ok, err)
	}
	if _, ok, _ := Load(root, "r-missing"); ok {
		t.Error("a missing run loaded")
	}
	all, err := List(root)
	if err != nil || len(all) != 2 || all[0].Run > all[1].Run {
		t.Errorf("list (oldest first): %v %v", all, err)
	}
	if Since(start, time.Time{}) != nil {
		t.Error("Since of a zero time should be nil")
	}
}

// A record holds ended lanes only, and a lane's result doesn't change in
// a rewrite of the same run.
func TestRunRecordTransitions(t *testing.T) {
	start := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	ok := &Run{Run: "r", Started: start, Lanes: []Lane{{Lane: 1, State: "passed"}}}
	if err := Transition(nil, ok); err != nil {
		t.Errorf("a first record: %v", err)
	}
	if err := Transition(nil, &Run{Lanes: []Lane{{Lane: 1, State: "running"}}}); err == nil {
		t.Error("a running lane in a record")
	}
	if err := Transition(ok, &Run{Run: "r", Started: start, Lanes: []Lane{{Lane: 1, State: "failed"}}}); err == nil {
		t.Error("a result changed in place")
	}
	if err := Transition(ok, &Run{Run: "r", Started: start.Add(time.Hour), Lanes: []Lane{{Lane: 1, State: "failed"}}}); err != nil {
		t.Errorf("another run under the same id: %v", err)
	}
	defer func() {
		if recover() == nil {
			t.Error("an illegal write didn't panic in a test")
		}
	}()
	Write(t.TempDir(), &Run{Run: "r", Lanes: []Lane{{Lane: 1, State: "waiting"}}})
}
