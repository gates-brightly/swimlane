package queue

import (
	"os"
	"path/filepath"
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
