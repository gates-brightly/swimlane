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
