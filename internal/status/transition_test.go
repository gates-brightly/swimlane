package status

import (
	"strings"
	"testing"
)

func TestTransition(t *testing.T) {
	at := func(s string) *string { return &s }
	lane := func(state, started, run string) *Lane {
		l := &Lane{State: state, Run: run}
		if started != "" {
			l.StartedAt = at(started)
		}
		return l
	}
	ok := [][2]*Lane{
		{lane(Idle, "", ""), lane(Waiting, "", "r1")},
		{lane(Waiting, "", "r1"), lane(Locked, "", "r1")},
		{lane(Locked, "", "r1"), lane(Running, "t1", "r1")},
		{lane(Queued, "", "r1"), lane(Passed, "t1", "r1")}, // exited before lane_init
		{lane(Running, "t1", "r1"), lane(Failed, "t1", "r1")},
		{lane(Running, "t1", "r1"), lane(Running, "t2", "r2")}, // a new lane_init
		{lane(Failed, "t1", "r1"), lane(Running, "t2", "r2")},
		{lane(Failed, "t1", "r1"), lane(Passed, "t2", "r2")}, // a new round
		{lane(Passed, "t1", "r1"), lane(Skipped, "t1", "r2")},
		{lane(Passed, "t1", "r1"), lane(Idle, "", "")}, // archived
	}
	for _, c := range ok {
		if err := Transition(c[0], c[1]); err != nil {
			t.Errorf("%s -> %s: %v", c[0].State, c[1].State, err)
		}
	}
	bad := [][2]*Lane{
		{lane(Running, "t1", "r1"), lane(Waiting, "t1", "r1")},
		{lane(Running, "t1", "r1"), lane(Skipped, "t1", "r1")},
		{lane(Running, "t1", "r1"), lane(Idle, "", "")},
		{lane(Queued, "", "r1"), lane(Idle, "", "")},
		{lane(Failed, "t1", "r1"), lane(Passed, "t1", "r1")}, // a failure turned into a pass
		{lane(Interrupted, "t1", "r1"), lane(Passed, "t1", "r1")},
	}
	for _, c := range bad {
		if err := Transition(c[0], c[1]); err == nil {
			t.Errorf("%s -> %s (same round) allowed", c[0].State, c[1].State)
		}
	}
}

// Every write through Update and UpdateFile is checked (and panics in
// tests); Rebuild isn't.
func TestUpdateChecksTransitions(t *testing.T) {
	root := t.TempDir()
	if err := Update(root, 1, 1, func(l *Lane) { l.State, l.StartedAt, l.Run = Failed, Str("t1"), "r1" }); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if r := recover(); r == nil || !strings.Contains(r.(string), "a failed round can't become passed") {
				t.Errorf("an illegal move didn't panic: %v", r)
			}
		}()
		Update(root, 1, 1, func(l *Lane) { l.State = Passed })
	}()
	if f, _ := Load(root); f.Get(1).State != Failed {
		t.Errorf("the illegal write landed: %s", f.Get(1).State)
	}
	if err := Rebuild(root, 1, func(f *File) error { f.Mine(1).State = Passed; return nil }); err != nil {
		t.Fatal(err)
	}
	if f, _ := Load(root); f.Get(1).State != Passed {
		t.Errorf("rebuild: %s", f.Get(1).State)
	}
}
