package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/queue"
	"github.com/gates-brightly/swimlane/internal/syntax"
	"github.com/gates-brightly/swimlane/internal/version"
)

// watchPresence is this swim watch's presence file (.swim/workers/), nil
// in any other command.
var watchPresence *queue.Announcer

// batchScript is the newest script syntax among a batch's lanes, and the
// lane that declares it (one newer than this swim reads included).
func batchScript(root string, e *queue.Entry) (newest, n int) {
	for _, l := range e.Lanes {
		v := 0
		info, err := lane.ReadScript(root, l.Lane)
		var tooNew syntax.ErrTooNew
		switch {
		case err == nil:
			v = info.Syntax
		case errors.As(err, &tooNew):
			v = tooNew.Got
		}
		if v > newest {
			newest, n = v, l.Lane
		}
	}
	return newest, n
}

// queuedBy names the swim that queued e, for messages.
func queuedBy(e *queue.Entry) string {
	if e.Swim == "" {
		return "an older swim"
	}
	return "swim " + e.Swim
}

// cantRunHere says why this swim, as a worker, can't run e ("" if it can):
// a lane's script is newer than it reads. The worker leaves such a batch
// queued for one that can.
func cantRunHere(root string, e *queue.Entry) string {
	script, n := batchScript(root, e)
	if script <= syntax.Reads {
		return ""
	}
	return fmt.Sprintf("the watcher, swim %s, can't run it: lane %d's script declares syntax %d, and it reads up to %d (queued by %s). Update the watcher's swim and restart swim watch",
		version.String(), n, script, syntax.Reads, queuedBy(e))
}

// holdHere leaves e queued with why, saying so the first time (on the
// worker's output and in .swim.log); why "" takes the hold back.
func holdHere(root string, e *queue.Entry, why string) {
	if watchPresence.Hold(e.ID, why) && why != "" {
		fmt.Fprintf(os.Stderr, "swim watch: #%d left queued: %s\n", e.Seq, why)
		history.Log(root, history.Entry{Event: history.QueueHeld, Detail: fmt.Sprintf("#%d %s: %s", e.Seq, e.ID, why)})
	}
}

// checkWorkers is swim queue add's look at the workers up now (live), for
// a batch about to be queued: an error when some are up (and announce
// themselves) but none can run it, naming each and its swim; a warning
// when none is up. Workers of a swim from before presence files can't be
// told apart: nothing is said about them.
func checkWorkers(root string, e *queue.Entry, live []*queue.Presence) (refuse error, warn string) {
	if len(live) == 0 {
		if !workerRunning(root) {
			// A swim watch that can't run it says why when it takes the queue.
			return nil, "no swim watch is running yet: it waits for one"
		}
		return nil, ""
	}
	script, n := batchScript(root, e)
	var whys []string
	for _, p := range live {
		if script <= p.Syntax {
			return nil, ""
		}
		whys = append(whys, fmt.Sprintf("  %s: lane %d's script declares syntax %d, and it reads up to %d", p.Name(), n, script, p.Syntax))
	}
	return fmt.Errorf("no watcher running now can run this batch:\n%s\nthis swim is %s (syntax %d). Update the watcher's swim (%s, or the build that matches this one) and restart its swim watch, or run swim watch with this build",
		strings.Join(whys, "\n"), version.String(), syntax.Reads, version.InstallCmd), ""
}

// heldReasons maps each batch a live worker leaves queued to why.
func heldReasons(root string) map[string]string {
	out := map[string]string{}
	for _, p := range queue.LiveWorkers(root) {
		for id, why := range p.Holding {
			out[id] = why
		}
	}
	return out
}
