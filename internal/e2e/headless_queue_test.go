package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/hub"
	"github.com/gates-brightly/swimlane/internal/queue"
)

// waitingEntries are the batches waiting in r's queue.
func waitingEntries(t *testing.T, r *repo) []queue.Entry {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(r.root, ".swim", "queue", "*.queue"))
	var out []queue.Entry
	for _, f := range files {
		data, _ := os.ReadFile(f)
		var e queue.Entry
		if err := yaml.Unmarshal(data, &e); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

// The hub works the worker's queue as swim watch does: lanes queued as one
// batch, a finished batch queued again, a waiting batch taken off. Every
// batch it queues is unsigned, carries no approvals, and names the user.
func TestHeadlessHubQueue(t *testing.T) {
	long(t)
	r, url, _, id, alice, vic := commandRepo(t)
	r.script(1, "first", `run "one" echo one`)
	r.script(2, "second", `run "two" echo two`)
	r.script(3, "blocker", `run "wait" sleep 4`)
	r.mustSwim("watch", "--headless", "--detach", "--interval", "200ms")
	via := " via " + url + ": "

	// Off under read access; on with readwrite (no swim.yml list needed).
	r.mustSwim("hub", "access", "read")
	sha := strings.Repeat("a", 64)
	alice.Send(id, hub.Command{Kind: hub.KindQueue, Lanes: []hub.LanePin{{Lane: 1, SHA256: sha, Manifest: sha}}})
	waitLog(t, r, "hub queue by alice"+via+"refused: not allowed here: this enrollment's access is read")
	r.mustSwim("hub", "access", "readwrite")
	r.write("swim.yml", "watch_batches: 1\n", 0o644)
	var l1, l2 hub.Lane
	waitFor(t, func() bool {
		l1, l2 = laneOnHub(t, url, id, 1), laneOnHub(t, url, id, 2)
		return l1.Script != "" && l2.Script != ""
	})

	// A viewer can't queue; an operator's stale pin is refused.
	if _, err := vic.Send(id, hub.Command{Kind: hub.KindQueue, Lanes: []hub.LanePin{{Lane: 1, SHA256: l1.Script, Manifest: l1.Manifest}}}); err == nil || !strings.Contains(err.Error(), "operator role") {
		t.Errorf("a viewer queued: %v", err)
	}
	alice.Send(id, hub.Command{Kind: hub.KindQueue, Lanes: []hub.LanePin{{Lane: 1, SHA256: sha, Manifest: l1.Manifest}}})
	waitLog(t, r, "refused: stale: swim 1's script is now "+l1.Script[:12])

	// Two lanes, one batch.
	alice.Send(id, hub.Command{Kind: hub.KindQueue, Lanes: []hub.LanePin{
		{Lane: 1, SHA256: l1.Script, Manifest: l1.Manifest}, {Lane: 2, SHA256: l2.Script, Manifest: l2.Manifest}}})
	waitLog(t, r, "hub queue by alice"+via+"done: queued")
	waitFor(t, func() bool { return len(doneEntries(t, r)) == 1 })
	first := doneEntries(t, r)[0]
	if len(first.Lanes) != 2 || first.By != "hub:alice via "+url || first.QueueSig != "" || len(first.Approve) != 0 || first.ApproveAll {
		t.Fatalf("the hub's batch: %+v", first)
	}

	// Queued again: a new, unsigned batch with none of the old one's approvals.
	alice.Send(id, hub.Command{Kind: hub.KindRequeue, Pin: hub.Pin{Batch: first.ID}})
	waitLog(t, r, "hub requeue by alice"+via+"done: queued "+first.ID)
	waitFor(t, func() bool { return len(doneEntries(t, r)) == 2 })
	for _, e := range doneEntries(t, r) {
		if e.ID == first.ID {
			continue
		}
		if !strings.Contains(e.Note, "was "+first.ID) || e.QueueSig != "" || len(e.Approve) != 0 || len(e.Lanes) != 2 {
			t.Errorf("the batch queued again: %+v", e)
		}
	}
	// A batch the worker doesn't have: refused.
	alice.Send(id, hub.Command{Kind: hub.KindRequeue, Pin: hub.Pin{Batch: "q-nope"}})
	waitLog(t, r, `hub requeue by alice`+via+`refused: no queue entry "q-nope"`)

	// Off the queue: a batch waiting behind a running one (watch_batches: 1).
	r.mustSwim("queue", "add", "3")
	waitFor(t, func() bool { return r.status().Get(3).CurrentStep == "wait" })
	alice.Send(id, hub.Command{Kind: hub.KindQueue, Lanes: []hub.LanePin{{Lane: 1, SHA256: l1.Script, Manifest: l1.Manifest}}})
	var waiting []queue.Entry
	waitFor(t, func() bool { waiting = waitingEntries(t, r); return len(waiting) == 1 })
	alice.Send(id, hub.Command{Kind: hub.KindDequeue, Pin: hub.Pin{Batch: waiting[0].ID}})
	waitLog(t, r, "hub dequeue by alice"+via+"done: removed "+waiting[0].ID)
	if w := waitingEntries(t, r); len(w) != 0 {
		t.Errorf("still waiting after dequeue: %+v", w)
	}
}
