package cli

import (
	"fmt"
	"sync"
	"time"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/notify"
	"github.com/gates-brightly/swimlane/internal/queue"
)

// watchNotes is swim watch's side of the terminal's notifications and
// progress bar (package notify; the notify setting): a notification when a
// batch asks the operator and when one ends, and a bar for the batches of
// the work in hand, from the first batch until the queue is empty again:
// how many have ended of those ended, running and queued, red once one
// failed, paused while a question waits. A nil watchNotes sends nothing.
type watchNotes struct {
	t    *notify.Terminal
	root string
	asks *watchAsks // the screen's questions (nil without the screen)

	mu           sync.Mutex
	ended, fails int // batches of the work in hand that ended, and failed
	stopTick     chan struct{}
	ticking      sync.WaitGroup
}

// newWatchNotes starts keeping the bar up to date (nil: notify is off).
func newWatchNotes(t *notify.Terminal, root string) *watchNotes {
	if t == nil {
		return nil
	}
	n := &watchNotes{t: t, root: root, stopTick: make(chan struct{})}
	n.ticking.Add(1)
	go func() {
		defer n.ticking.Done()
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			n.progress()
			select {
			case <-n.stopTick:
				return
			case <-tick.C:
			}
		}
	}()
	return n
}

// stop hides the bar: swim watch leaves none behind.
func (n *watchNotes) stop() {
	if n == nil {
		return
	}
	close(n.stopTick)
	n.ticking.Wait()
	n.t.Done()
}

// asked tells the terminal a batch's lane is waiting on the operator.
func (n *watchNotes) asked(q watchAsk) {
	if n == nil {
		return
	}
	what := "approval of " + q.msg.Flag
	if q.msg.Choices > 0 {
		what = "a decision: " + q.msg.Flag
	}
	n.t.Notify(fmt.Sprintf("swim %d needs you", q.msg.Lane),
		fmt.Sprintf("%s: %s · batch #%d", what, approval.Clean(q.msg.Desc), q.seq))
	n.progress()
}

// batchDone tells the terminal batch #seq ended, with result (passed,
// failed, interrupted, skipped: a guard dry-ran, error).
func (n *watchNotes) batchDone(seq int, result string, lanes []int, took time.Duration) {
	if n == nil {
		return
	}
	n.mu.Lock()
	n.ended++
	switch result {
	case "passed", "skipped":
	default:
		n.fails++
	}
	n.mu.Unlock()
	body := lanesText(lanes) + " · " + display.Elapsed(took)
	if result == "skipped" {
		body += " · a guard dry-ran: it waits on an approval"
	}
	n.t.Notify(fmt.Sprintf("swim: #%d %s", seq, result), body)
	n.progress()
}

// progress brings the bar up to date with the queue.
func (n *watchNotes) progress() {
	entries, err := queue.List(n.root, false)
	if err != nil {
		return
	}
	running, queued := 0, 0
	for _, e := range entries {
		switch e.State {
		case queue.Running:
			running++
		case queue.Queued:
			queued++
		}
	}
	asking := false
	if n.asks != nil {
		p, _ := n.asks.pending()
		asking = len(p) > 0
	}
	n.mu.Lock()
	if running+queued == 0 {
		// The work in hand is done: the next batch starts a new count.
		n.ended, n.fails = 0, 0
		n.mu.Unlock()
		n.t.Done()
		return
	}
	state := notify.Normal
	switch {
	case asking:
		state = notify.Paused
	case n.fails > 0:
		state = notify.Error
	}
	p := notify.Of(n.ended, n.ended+running+queued, state)
	n.mu.Unlock()
	n.t.SetProgress(p)
}
