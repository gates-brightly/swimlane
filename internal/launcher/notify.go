package launcher

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/notify"
	"github.com/gates-brightly/swimlane/internal/status"
)

// The terminal's notifications and progress bar for a run (Options.Notify,
// package notify): the bar counts the run's lanes that have ended, red once
// one failed and paused while a lane waits on the operator; a notification
// says when a lane asks and when the run ends.

// progressEvery is how often the run's progress bar is brought up to date.
const progressEvery = 500 * time.Millisecond

// runProgress keeps the bar up to date until the returned func is called,
// which hides it (once: it's deferred too, for a run that ends early).
func runProgress(o Options, sel []int, disp *display.Display) (stop func()) {
	if o.Notify == nil {
		return func() {}
	}
	done, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		tick := time.NewTicker(progressEvery)
		defer tick.Stop()
		for {
			o.Notify.SetProgress(progressOf(disp.Views(), sel))
			select {
			case <-done:
				return
			case <-tick.C:
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			<-finished
			o.Notify.Done()
		})
	}
}

// progressOf is the bar for the run's lanes sel as the panel shows them.
func progressOf(views []display.LaneView, sel []int) notify.Progress {
	ended, failed, asking := 0, false, false
	for _, v := range views {
		if !slices.Contains(sel, v.N) {
			continue
		}
		switch v.State {
		case display.Passed, display.Skipped, display.Done:
			ended++
		case display.Failed, display.Interrupted:
			ended++
			failed = true
		}
		asking = asking || v.Approval != ""
	}
	state := notify.Normal
	switch {
	case asking:
		state = notify.Paused
	case failed:
		state = notify.Error
	}
	return notify.Of(ended, len(sel), state)
}

// notifyAsk tells the operator's terminal, and notify_command, a lane is
// asking them.
func notifyAsk(o Options, req approval.Request) {
	title := fmt.Sprintf("swim %d needs you", req.Lane)
	o.Notify.Notify(title, askText(req))
	o.Hook.Send(notify.Note{Event: notify.EventAsk, Title: title, Body: askText(req), Lanes: []int{req.Lane}})
}

// askText is what a question asks, for a notification: "approval of
// DEPLOY: deploy 2.4.1".
func askText(req approval.Request) string {
	return "approval of " + req.Flag + ": " + req.Desc
}

// notifyEnd tells the operator's terminal, and notify_command, how the run
// ended: "swim run
// failed" / "swim 1,2,3 · 2 passed, 1 failed · 3:12".
func notifyEnd(o Options, sel []int, counts map[string]int, code int, stopped bool, elapsed time.Duration) {
	word, result := "passed", status.Passed
	switch {
	case stopped:
		word, result = "interrupted", status.Interrupted
	case code == ExitDryRun:
		word, result = "waiting on an approval (a guard dry-ran)", status.Skipped
	case code != 0:
		word, result = "failed", status.Failed
	}
	var parts []string
	for _, st := range []string{status.Passed, status.Failed, status.Skipped, status.Interrupted} {
		if counts[st] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[st], st))
		}
	}
	title, body := "swim run "+word, fmt.Sprintf("swim %s · %s · %s", joinInts(sel), strings.Join(parts, ", "), display.Elapsed(elapsed))
	o.Notify.Notify(title, body)
	o.Hook.Send(notify.Note{Event: notify.EventEnd, Result: result, Title: title, Body: body, Lanes: sel})
}
