package cli

import (
	"fmt"
	"sync"
	"time"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/launcher"
	"github.com/gates-brightly/swimlane/internal/notify"
	"github.com/gates-brightly/swimlane/internal/queue"
)

// watchNotes is swim watch's side of the terminal's notifications and
// progress bar (package notify; the notify setting): a notification when a
// batch asks the operator and when one ends, and a bar for the batches of
// the work in hand, from the first batch until the queue is empty again:
// how many have ended of those ended, running and queued, red once one
// failed, paused while a question waits. notify_command hears of the same
// questions and batch ends. A nil watchNotes sends nothing.
type watchNotes struct {
	t     *notify.Terminal // nil: no terminal notifications or bar
	hook  *notify.Hook     // nil: no notify_command
	root  string
	from  notify.Origin // what each title starts with
	asks  *watchAsks    // the screen's questions (nil without the screen)
	phone *phoneAnswers // gives each question its phone links (nil: off)

	mu           sync.Mutex
	ended, fails int // batches of the work in hand that ended, and failed
	stopTick     chan struct{}
	ticking      sync.WaitGroup
}

// newWatchNotes starts keeping the bar up to date (nil: notify is off and
// there's no notify_command).
func newWatchNotes(t *notify.Terminal, hook *notify.Hook, root string) *watchNotes {
	if t == nil && hook == nil {
		return nil
	}
	n := &watchNotes{t: t, hook: hook, root: root, from: notifyOrigin(root), stopTick: make(chan struct{})}
	if t == nil {
		return n
	}
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

// stop hides the bar, swim watch leaves none behind, and waits for the
// notify_command still running.
func (n *watchNotes) stop() {
	if n == nil {
		return
	}
	close(n.stopTick)
	n.ticking.Wait()
	n.t.Done()
	n.hook.Wait()
}

// asked tells the terminal a batch's lane is waiting on the operator.
func (n *watchNotes) asked(q watchAsk) {
	if n == nil {
		return
	}
	what := "approval of " + q.msg.Flag
	title := n.from.Title(fmt.Sprintf("lane %d needs you", q.msg.Lane))
	body := what + ": " + approval.Clean(q.msg.Desc)
	if q.msg.Reason != "" {
		body += " · why: " + approval.Clean(q.msg.Reason)
	}
	body += fmt.Sprintf(" · batch #%d", q.seq)
	n.t.Notify(title, body)
	note := notify.Note{Event: notify.EventAsk, Title: title, Body: body, Lanes: []int{q.msg.Lane}, Batch: q.seq}
	if note.ApproveURL, note.RejectURL, note.Expires = n.phone.issue(q); note.ApproveURL != "" {
		note.Body += " · the links work until " + note.Expires.Format("15:04")
	}
	n.hook.Send(n.from.Note(note))
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
	body := lanesText(lanes) + " · " + display.Duration(took)
	if result == "skipped" {
		body += " · a guard dry-ran: it waits on an approval"
	}
	if result == "failed" {
		if f := launcher.FirstFailure(n.root, lanes); f != "" {
			body += " · " + f
		}
	}
	title := n.from.Title(fmt.Sprintf("batch #%d %s", seq, result))
	n.t.Notify(title, body)
	n.hook.Send(n.from.Note(notify.Note{Event: notify.EventEnd, Result: result, Title: title, Body: body, Lanes: lanes, Batch: seq}))
	n.progress()
}

// progress brings the bar up to date with the queue.
func (n *watchNotes) progress() {
	if n.t == nil {
		return
	}
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
