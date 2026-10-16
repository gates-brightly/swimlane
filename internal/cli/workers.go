package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/module"
	"github.com/gates-brightly/swimlane/internal/queue"
	"github.com/gates-brightly/swimlane/internal/version"
)

// watchPresence is this swim watch's presence file (.swim/workers/), nil
// in any other command.
var watchPresence *queue.Announcer

// batchScript is the newest script syntax among a batch's lanes, and the
// lane that declares it.
func batchScript(root string, e *queue.Entry) (syntax, n int) {
	for _, l := range e.Lanes {
		if info, err := lane.ReadScript(root, l.Lane); err == nil && info.Syntax > syntax {
			syntax, n = info.Syntax, l.Lane
		}
	}
	return syntax, n
}

// queuedBy names the swim that queued e, for messages.
func queuedBy(e *queue.Entry) string {
	if e.Swim == "" {
		return "an older swim"
	}
	if e.Syntax == nil {
		return "swim " + e.Swim
	}
	return fmt.Sprintf("swim %s, reading %s", e.Swim, *e.Syntax)
}

// cantRunHere says why this swim, as a worker, can't run e ("" if it can):
// the repo's lock pins other syntaxes than it reads (it was upgraded after
// the worker started), or a lane's script is newer than it reads. The
// worker leaves such a batch queued for one that can.
func cantRunHere(root string, e *queue.Entry) string {
	lock, locked, err := version.ReadLock(root)
	if err != nil {
		return "can't read " + version.LockName + ": " + err.Error()
	}
	script, _ := batchScript(root, e)
	why := version.Current.CantRun(lock.Syntaxes(), locked, script)
	if why == "" {
		// Its lanes' built-in modules, as the lock pins them.
		if cfg, err := loadCfg(root); err == nil {
			var whys []string
			for _, l := range e.Lanes {
				if w := moduleLockProblem(root, cfg, l.Lane); w != "" {
					whys = append(whys, fmt.Sprintf("swim %d: %s", l.Lane, w))
				}
			}
			why = strings.Join(whys, "; ")
		}
	}
	if why == "" {
		return ""
	}
	return fmt.Sprintf("this worker, swim %s, can't run it: %s (queued by %s). Update this worker's swim and restart swim watch",
		version.String(), why, queuedBy(e))
}

// holdHere leaves e queued with why, saying so the first time (on the
// worker's output and in .swim.log); why "" takes the hold back.
func holdHere(root string, e *queue.Entry, why string) {
	if watchPresence.Hold(e.ID, why) && why != "" {
		fmt.Fprintf(os.Stderr, "swim watch: #%d left queued: %s\n", e.Seq, why)
		history.Log(root, history.Entry{Event: history.QueueHeld, Detail: fmt.Sprintf("#%d %s: %s", e.Seq, e.ID, why)})
	}
}

// checkWorkers is swim queue add's look at the workers up now, for a batch
// about to be queued: an error when some are up (and announce themselves)
// but none can run it, naming each and its swim; a warning when none is
// up. Workers of a swim from before presence files can't be told apart:
// nothing is said about them.
func checkWorkers(root string, e *queue.Entry) (refuse error, warn string) {
	live := queue.LiveWorkers(root)
	if len(live) == 0 {
		if !workerRunning(root) {
			return nil, fmt.Sprintf("no worker is running: the batch waits for a swim watch that reads %s (as this swim, %s, does)",
				version.Current, version.String())
		}
		return nil, ""
	}
	lock, locked, err := version.ReadLock(root)
	if err != nil {
		return nil, ""
	}
	script, n := batchScript(root, e)
	builtins := batchBuiltins(root, e)
	var whys []string
	for _, p := range live {
		why := p.Syntax.CantRun(lock.Syntaxes(), locked, script)
		if why == "" {
			why = workerModules(lock, p, builtins)
		}
		if why == "" {
			return nil, ""
		}
		if script > p.Syntax.Script {
			why = fmt.Sprintf("swim %d's script declares syntax %d, and it reads up to %d", n, script, p.Syntax.Script)
		}
		whys = append(whys, "  "+p.Name()+": "+why)
	}
	return fmt.Errorf("no worker running now can run this batch:\n%s\nthis swim is %s (%s). Update the worker's swim (%s, or the build that matches this one) and restart its swim watch, or run swim watch with this build",
		strings.Join(whys, "\n"), version.String(), version.Current, version.InstallCmd), ""
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

// batchBuiltins are the built-in modules a batch's lanes use.
func batchBuiltins(root string, e *queue.Entry) []string {
	cfg, err := loadCfg(root)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, l := range e.Lanes {
		mods, _ := module.Resolve(root, cfg.Modules, uniq(laneRefs(root, l.Lane)))
		for _, m := range mods {
			if m.Builtin && !seen[m.Name] {
				seen[m.Name] = true
				out = append(out, m.Name)
			}
		}
	}
	return out
}

// workerModules says why worker p's built-in modules can't run a batch
// using builtins in a repo whose lock pins them ("" if they can, or the
// worker doesn't say which it has).
func workerModules(lock version.Lock, p *queue.Presence, builtins []string) string {
	if p.Builtins == nil {
		return ""
	}
	var whys []string
	for _, name := range builtins {
		have, ok := p.Builtins[name]
		if !ok {
			if _, pinned := lock.Builtin(name); pinned {
				whys = append(whys, "its swim has no built-in "+name)
			}
			continue
		}
		if why := module.PinProblem(lock, name, have.Version, have.SHA256); why != "" {
			whys = append(whys, why)
		}
	}
	return strings.Join(whys, "; ")
}
