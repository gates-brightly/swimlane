package queue

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAddClaimConsume(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 3; i++ {
		if err := Add(root, &Entry{By: "op", Lanes: []Lane{{Lane: i + 1, Job: "j", Round: "r"}}}); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := List(root, true)
	if len(all) != 3 || all[0].Seq != 1 || all[2].Seq != 3 || all[0].State != Queued {
		t.Fatalf("list: %+v", all)
	}
	e, err := ClaimNext(root, "op", 0)
	if err != nil || e == nil || e.Seq != 1 || e.State != Running || e.Worker.PID != os.Getpid() {
		t.Fatalf("claim: %+v %v", e, err)
	}
	if err := Remove(e); err == nil {
		t.Error("removed a running entry")
	}
	if err := Consume(root, e, Result{Result: "passed"}); err != nil {
		t.Fatal(err)
	}
	all, _ = List(root, true)
	if all[0].State != Done || all[0].Result.Result != "passed" || all[1].State != Queued {
		t.Fatalf("after consume: %+v %+v", all[0], all[1])
	}
	// Sequence numbers keep counting past consumed entries.
	Add(root, &Entry{By: "op"})
	all, _ = List(root, false)
	if all[len(all)-1].Seq != 4 {
		t.Fatalf("seq: %d", all[len(all)-1].Seq)
	}
	if got, err := Find(root, "1"); err != nil || got.State != Done {
		t.Fatalf("find by seq: %+v %v", got, err)
	}
}

func TestClaimIsExclusive(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 20; i++ {
		Add(root, &Entry{By: "op"})
	}
	var mu sync.Mutex
	seen := map[int]int{}
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				e, err := ClaimNext(root, "op", 0)
				if err != nil {
					t.Error(err)
					return
				}
				if e == nil {
					return
				}
				mu.Lock()
				seen[e.Seq]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != 20 {
		t.Fatalf("claimed %d of 20 entries", len(seen))
	}
	for seq, n := range seen {
		if n != 1 {
			t.Errorf("entry %d claimed %d times", seq, n)
		}
	}
}

func TestOrphanAndRequeue(t *testing.T) {
	root := t.TempDir()
	Add(root, &Entry{By: "op"})
	e, _ := ClaimNext(root, "op", 0)
	// Its worker dies: a pid that isn't running.
	e.Worker.PID = 999999
	write(e)
	all, _ := List(root, false)
	if all[0].State != Orphaned {
		t.Fatalf("state %s", all[0].State)
	}
	if err := Requeue(root, all[0]); err != nil {
		t.Fatal(err)
	}
	all, _ = List(root, false)
	if len(all) != 1 || all[0].State != Queued || all[0].Seq != 2 || all[0].Worker != nil {
		t.Fatalf("requeued: %+v", all[0])
	}
	if files, _ := filepath.Glob(filepath.Join(Dir(root), "*.running")); len(files) != 0 {
		t.Errorf("old claim left: %v", files)
	}
}

func TestSeqNeverReused(t *testing.T) {
	root := t.TempDir()
	Add(root, &Entry{By: "op"})
	Add(root, &Entry{By: "op"})
	all, _ := List(root, false)
	Remove(all[1]) // the newest
	e := &Entry{By: "op"}
	Add(root, e)
	if e.Seq != 3 {
		t.Fatalf("a removed entry's number was reused: got #%d, want #3", e.Seq)
	}
}

func TestClaimOutOfTurn(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 3; i++ {
		Add(root, &Entry{By: "op", Lanes: []Lane{{Lane: i + 1}}})
	}
	all, _ := List(root, false)
	e, err := Claim(root, all[2], "op", 0)
	if err != nil || e.Seq != 3 || e.State != Running || e.Worker == nil {
		t.Fatalf("claim #3: %+v %v", e, err)
	}
	// The oldest still comes next in turn.
	if next, _ := ClaimNext(root, "op", 0); next == nil || next.Seq != 1 {
		t.Fatalf("next after #3: %+v", next)
	}
	// A stale copy of a claimed entry can't be claimed again.
	if _, err := Claim(root, all[2], "op", 0); err == nil {
		t.Error("claimed #3 twice")
	}
	if _, err := Claim(root, e, "op", 0); err == nil {
		t.Error("claimed a running entry")
	}
}

// A claim seen from another host is alive while its heartbeat is fresher
// than its lease; on the worker's host, the pid must still be the worker
// (same start time).
func TestWorkerLease(t *testing.T) {
	root := t.TempDir()
	Add(root, &Entry{By: "op"})
	e, err := ClaimNext(root, "op", time.Minute)
	if err != nil || e.Worker.Lease != "1m0s" || e.Worker.Heartbeat == "" || e.Worker.Start == "" {
		t.Fatalf("claim: %v %+v", err, e.Worker)
	}
	state := func() string {
		all, _ := List(root, false)
		return all[0].State
	}
	if s := state(); s != Running {
		t.Fatalf("own claim: %s", s)
	}
	// The pid handed to another process (a different start time).
	e.Worker.Start = "1"
	write(e)
	if s := state(); s != Orphaned {
		t.Errorf("reused pid: %s", s)
	}

	defer func(h func() string, n func() time.Time) { hostname, now = h, n }(hostname, now)
	hostname = func() string { return "another-host" }
	t0 := time.Now()
	if s := state(); s != Running {
		t.Errorf("fresh heartbeat from another host: %s", s)
	}
	now = func() time.Time { return t0.Add(2 * time.Minute) }
	if s := state(); s != Orphaned {
		t.Errorf("stale heartbeat (2m, lease 1m): %s", s)
	}
	if err := Touch(e); err != nil {
		t.Fatal(err)
	}
	if s := state(); s != Running {
		t.Errorf("after Touch: %s", s)
	}
	// An older worker's claim (no heartbeat) can't be judged: alive.
	e.Worker.Heartbeat, e.Worker.Lease = "", ""
	write(e)
	now = func() time.Time { return t0.Add(time.Hour) }
	if s := state(); s != Running {
		t.Errorf("no heartbeat: %s", s)
	}
	// Once orphaned and requeued elsewhere, the old worker's Touch fails.
	e.Worker.Heartbeat = t0.UTC().Format(time.RFC3339)
	write(e)
	all, _ := List(root, false)
	if err := Requeue(root, all[0]); err != nil {
		t.Fatal(err)
	}
	if err := Touch(e); err == nil {
		t.Error("Touch on a requeued entry succeeded")
	}
}

// A scheduled entry isn't claimed before its start time, and doesn't hold
// up one behind it; once due, entries go by start time; late is still run.
func TestClaimByStartTime(t *testing.T) {
	root := t.TempDir()
	t0 := time.Date(2026, 10, 15, 13, 0, 0, 0, time.UTC)
	defer func(old func() time.Time) { now = old }(now)
	now = func() time.Time { return t0 }
	at := func(d time.Duration) string { return t0.Add(d).Format(time.RFC3339) }
	add := func(n int, notBefore string) {
		if err := Add(root, &Entry{By: "op", NotBefore: notBefore, Lanes: []Lane{{Lane: n, Job: "j", Round: "r"}}}); err != nil {
			t.Fatal(err)
		}
	}
	add(1, at(30*time.Minute)) // #1 at 13:30
	add(2, "")                 // #2 now
	add(3, at(10*time.Minute)) // #3 at 13:10
	if e, _ := ClaimNext(root, "op", 0); e == nil || e.Seq != 2 {
		t.Fatalf("before 13:10: claimed %+v, want #2", e)
	}
	if e, _ := ClaimNext(root, "op", 0); e != nil {
		t.Fatalf("nothing due yet, claimed #%d", e.Seq)
	}
	// Hours later, with no worker in between: both run, by start time.
	now = func() time.Time { return t0.Add(3 * time.Hour) }
	if e, _ := ClaimNext(root, "op", 0); e == nil || e.Seq != 3 {
		t.Fatalf("late: claimed %+v, want #3 (13:10) first", e)
	}
	if e, _ := ClaimNext(root, "op", 0); e == nil || e.Seq != 1 {
		t.Fatalf("late: claimed %+v, want #1", e)
	}
}

// The statement signs the start time only when there is one: an
// unscheduled entry's reads as it always has.
func TestStatementStartTime(t *testing.T) {
	e := &Entry{ID: "q-x", Lanes: []Lane{{Lane: 1, Job: "j", SHA256: "abc"}}, Approve: []string{"F"}}
	if got := e.Statement(); got != "swim queue v1\nid: q-x\nlane: 1 j abc\napprove: F\napprove-all: false\n" {
		t.Errorf("unscheduled statement changed:\n%q", got)
	}
	e.NotBefore = "2026-10-15T18:30:00Z"
	signed := e.Statement()
	if !strings.Contains(signed, "not-before: 2026-10-15T18:30:00Z\n") {
		t.Errorf("scheduled statement: %q", signed)
	}
	e.NotBefore = "2026-10-15T08:30:00Z"
	if e.Statement() == signed {
		t.Error("moving the start time left the statement the same")
	}
}

// An occurrence carries its recurring schedule's signed statement, which
// vouches for it only while it is what the statement describes.
func TestOccurrenceStatement(t *testing.T) {
	st := "swim recurring v1\nid: r-x\nlane: 4 j sha man\napprove: F\ndaily: 06:00:00 UTC\nfirst: 2026-10-16\nuntil: 2026-11-14\n"
	e := &Entry{ID: "q-1", From: "r-x", Occurrence: "2026-10-20", NotBefore: "2026-10-20T06:00:00Z", FromStatement: st,
		Lanes: []Lane{{Lane: 4, Job: "j", SHA256: "sha", Manifest: "man"}}, Approve: []string{"F"}}
	if e.Statement() != st {
		t.Fatalf("a matching occurrence: %q", e.Statement())
	}
	for what, edit := range map[string]func(*Entry){
		"start time":   func(e *Entry) { e.NotBefore = "2026-10-20T05:00:00Z" },
		"day outside":  func(e *Entry) { e.Occurrence, e.NotBefore = "2026-12-01", "2026-12-01T06:00:00Z" },
		"script":       func(e *Entry) { e.Lanes[0].SHA256 = "other" },
		"another lane": func(e *Entry) { e.Lanes = append(e.Lanes, Lane{Lane: 5}) },
		"approvals":    func(e *Entry) { e.Approve = []string{"F", "G"} },
		"-y":           func(e *Entry) { e.ApproveAll = true },
	} {
		c := *e
		c.Lanes = append([]Lane(nil), e.Lanes...)
		edit(&c)
		if c.Statement() == st {
			t.Errorf("%s edited, the statement still vouches for it", what)
		}
	}
}

// A cron schedule's occurrence is keyed by its minute; one with no end
// (an empty until:) vouches for any later occurrence.
func TestCronOccurrenceStatement(t *testing.T) {
	st := "swim recurring v1\nid: r-c\nlane: 2 j sha man\napprove: \ncron: */30 * * * * UTC\nfirst: 2026-10-16T00:00\nuntil: \n"
	e := &Entry{ID: "q-2", From: "r-c", Occurrence: "2027-03-01T13:30", NotBefore: "2027-03-01T13:30:00Z", FromStatement: st,
		Lanes: []Lane{{Lane: 2, Job: "j", SHA256: "sha", Manifest: "man"}}}
	if e.Statement() != st {
		t.Fatalf("a matching occurrence: %q", e.Statement())
	}
	for what, edit := range map[string]func(*Entry){
		"off the expression": func(e *Entry) { e.Occurrence, e.NotBefore = "2027-03-01T13:31", "2027-03-01T13:31:00Z" },
		"start time":         func(e *Entry) { e.NotBefore = "2027-03-01T14:00:00Z" },
		"before first":       func(e *Entry) { e.Occurrence, e.NotBefore = "2026-10-15T23:30", "2026-10-15T23:30:00Z" },
	} {
		c := *e
		edit(&c)
		if c.Statement() == st {
			t.Errorf("%s, the statement still vouches for it", what)
		}
	}
}

// Illegal moves panic in tests (SWIM_CHECK_STATES elsewhere): consuming an
// entry no worker claimed, consuming one twice, claiming a consumed one.
func TestIllegalQueueMovesPanic(t *testing.T) {
	root := t.TempDir()
	mustPanic := func(what string, f func()) {
		t.Helper()
		defer func() {
			if r := recover(); r == nil || !strings.Contains(fmt.Sprint(r), "illegal state transition") {
				t.Errorf("%s: recovered %v, want an illegal-transition panic", what, r)
			}
		}()
		f()
	}
	e := &Entry{By: "op", Lanes: []Lane{{Lane: 1, Job: "j"}}}
	if err := Add(root, e); err != nil {
		t.Fatal(err)
	}
	mustPanic("consume unclaimed", func() { Consume(root, e, Result{Result: "passed"}) })
	c, err := ClaimNext(root, "op", 0)
	if err != nil || c == nil {
		t.Fatalf("claim: %v %v", c, err)
	}
	if err := Consume(root, c, Result{Result: "passed"}); err != nil {
		t.Fatal(err)
	}
	mustPanic("consume twice", func() { Consume(root, c, Result{Result: "passed"}) })
	mustPanic("claim a done entry", func() { claim(c.Path, "op", 0) })
	// Legal: a done entry requeued, then removed.
	if err := Requeue(root, c); err != nil {
		t.Fatal(err)
	}
	c.State = Queued
	if err := Remove(c); err != nil {
		t.Fatal(err)
	}
	if Transition(Queued, Done) == nil || Transition(Running, Done) != nil {
		t.Error("Transition rules")
	}
}
