package launcher

import (
	"fmt"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/status"
)

// Interrupt levels: what the launcher has asked running lanes to do.
const (
	levelRunning  = iota
	levelGraceful // stop at the next step boundary (first Ctrl-C)
	levelForce    // SIGINT to every lane's process group, SIGKILL after the grace
	levelKill     // SIGKILL now
)

// laneProc is a running lane's bash process. In its own process group
// (group) the terminal's Ctrl-C reaches only swim, which decides what to
// pass on; a lane that shares swim's group (a single lane reading the
// terminal) gets Ctrl-C straight from the terminal, as before.
type laneProc struct {
	proc    *os.Process
	group   bool
	started time.Time
}

// interrupter turns signals into the graceful → force → kill escalation.
// The time between Ctrl-C presses isn't limited: the next press always
// escalates.
type interrupter struct {
	o         Options
	disp      *display.Display
	sl        *slots
	lt        *lockTable
	mode      string // config.InterruptGraceful | config.InterruptImmediate
	grace     time.Duration
	termGrace time.Duration

	// notice is shown in the panel title; separate from mu, because the
	// display reads it while holding its own lock.
	notice atomic.Value
	mu     sync.Mutex
	level  int
	procs  map[int]*laneProc
	timers []*time.Timer
}

func newInterrupter(o Options, disp *display.Display, sl *slots, lt *lockTable) *interrupter {
	ir := &interrupter{o: o, disp: disp, sl: sl, lt: lt, mode: o.Cfg.InterruptMode(), procs: map[int]*laneProc{}}
	ir.grace, ir.termGrace = o.Cfg.Graces()
	if o.Interrupt != "" {
		ir.mode = o.Interrupt
	}
	return ir
}

// stopping reports whether the run has been interrupted at any level: lanes
// that haven't started are then skipped.
func (ir *interrupter) stopping() bool {
	ir.mu.Lock()
	defer ir.mu.Unlock()
	return ir.level > levelRunning
}

// Notice is the panel title's interrupt state ("" while running).
func (ir *interrupter) Notice() string {
	if ir == nil {
		return ""
	}
	s, _ := ir.notice.Load().(string)
	return s
}

// stop cancels pending timers when the run ends.
func (ir *interrupter) stop() {
	ir.mu.Lock()
	defer ir.mu.Unlock()
	for _, t := range ir.timers {
		t.Stop()
	}
}

// add registers a lane that just started. A lane that starts after an
// interrupt (it was past every check) gets the current level at once.
func (ir *interrupter) add(n int, lp *laneProc) {
	ir.mu.Lock()
	defer ir.mu.Unlock()
	ir.procs[n] = lp
	switch ir.level {
	case levelGraceful:
		ir.requestStopLocked(n, lp, syscall.SIGINT)
	case levelForce:
		ir.signalLocked(lp, syscall.SIGINT)
	case levelKill:
		ir.signalLocked(lp, syscall.SIGKILL)
	}
}

// remove forgets a lane that finished, and its stop request.
func (ir *interrupter) remove(n int) {
	ir.mu.Lock()
	defer ir.mu.Unlock()
	delete(ir.procs, n)
	lane.ClearStop(ir.o.Root, n)
}

// handle reacts to one signal.
//   - SIGINT (Ctrl-C): graceful, then force, then kill; with interrupt:
//     immediate the first press forces.
//   - SIGTERM (CI cancel, kill): graceful, forcing after term_grace without
//     a second signal; another SIGTERM escalates like Ctrl-C.
//   - SIGHUP (terminal closed): force at once (nobody is left to wait for).
func (ir *interrupter) handle(s os.Signal) {
	ir.mu.Lock()
	defer ir.mu.Unlock()
	ir.sl.close()
	ir.lt.close()
	switch {
	case s == syscall.SIGHUP && ir.level < levelForce:
		ir.forceLocked(s)
	case ir.level == levelRunning && ir.mode == config.InterruptImmediate:
		ir.forceLocked(s)
	case ir.level == levelRunning:
		ir.gracefulLocked(s)
		if s == syscall.SIGTERM {
			ir.after(ir.termGrace, func() {
				ir.mu.Lock()
				defer ir.mu.Unlock()
				if ir.level == levelGraceful {
					ir.forceLocked(syscall.SIGTERM)
				}
			})
		}
	case ir.level == levelGraceful:
		ir.forceLocked(s)
	default:
		ir.killLocked()
	}
}

func (ir *interrupter) after(d time.Duration, fn func()) {
	ir.timers = append(ir.timers, time.AfterFunc(d, fn))
}

func (ir *interrupter) running() []int {
	var ns []int
	for n := range ir.procs {
		ns = append(ns, n)
	}
	sort.Ints(ns)
	return ns
}

func (ir *interrupter) gracefulLocked(s os.Signal) {
	ir.level = levelGraceful
	ns := ir.running()
	ir.notice.Store(fmt.Sprintf("stopping · %d steps running · Ctrl-C again to force quit", len(ns)))
	how := "Ctrl-C again to force quit."
	if s == syscall.SIGTERM {
		how = fmt.Sprintf("forcing in %s (term_grace).", ir.termGrace)
	}
	ir.disp.Message(fmt.Sprintf("swim: stopping after current steps (%d running). %s", len(ns), how))
	ir.o.ev.emit(Event{Event: "stop_requested", Run: ir.o.RunID, Lanes: ns, At: now()})
	st, _ := status.Load(ir.o.Root)
	for _, n := range ns {
		lp := ir.procs[n]
		ir.requestStopLocked(n, lp, s)
		step := ""
		if st != nil {
			if l := st.Get(n); l != nil {
				step = l.CurrentStep
			}
		}
		label := step
		if label == "" {
			label = "current step"
		}
		ir.disp.Set(n, func(v *display.LaneView) { v.Stopping = label })
		ir.disp.Message(fmt.Sprintf("   swim %-3d stopping  %s (%s)", n, label, display.Elapsed(time.Since(lp.started))))
		ir.o.ev.emit(Event{Event: "stopping", Lane: n, Label: step, At: now()})
	}
}

// requestStopLocked asks one lane to stop at its next step boundary: the
// stop file keeps swim step from starting another step, and SIGUSR2 to the
// lane's bash (only that pid, never the step command) ends the round once
// the current step returns, or cancels a waiting confirm.
func (ir *interrupter) requestStopLocked(n int, lp *laneProc, s os.Signal) {
	lane.RequestStop(ir.o.Root, n, ir.o.RunID)
	if !lp.group {
		// It shares swim's process group: the terminal already delivered
		// Ctrl-C to it. Pass on what the terminal didn't send.
		if s != syscall.SIGINT {
			lp.proc.Signal(s)
		}
		return
	}
	// Signal only a lane whose bash has reached lane_init (status shows its
	// pid; the trap is set before that): USR2 would kill bash before it.
	// An earlier lane has the stop file, and its first step refuses.
	if st, _ := status.Load(ir.o.Root); st != nil {
		if l := st.Get(n); l != nil && l.PID == lp.proc.Pid && l.Run == ir.o.RunID {
			lp.proc.Signal(syscall.SIGUSR2)
		}
	}
}

func (ir *interrupter) forceLocked(s os.Signal) {
	ir.level = levelForce
	ns := ir.running()
	ir.notice.Store(fmt.Sprintf("force quitting · killing in %s · Ctrl-C again to kill now", ir.grace))
	ir.disp.Message(fmt.Sprintf("swim: force quitting: interrupting %d lanes, killing in %s. Ctrl-C again to kill now.", len(ns), ir.grace))
	ir.o.ev.emit(Event{Event: "force_quit", Run: ir.o.RunID, Lanes: ns, Duration: floatp(ir.grace.Seconds()), At: now()})
	for _, n := range ns {
		lp := ir.procs[n]
		if !lp.group && s == syscall.SIGINT {
			continue // the terminal's Ctrl-C reached it already
		}
		ir.signalLocked(lp, syscall.SIGINT)
	}
	ir.after(ir.grace, func() {
		ir.mu.Lock()
		defer ir.mu.Unlock()
		if ir.level == levelForce {
			ir.killLocked()
		}
	})
}

func (ir *interrupter) killLocked() {
	ir.level = levelKill
	ns := ir.running()
	ir.notice.Store("killing lanes")
	if len(ns) > 0 {
		ir.disp.Message(fmt.Sprintf("swim: killing %d lanes now.", len(ns)))
	}
	ir.o.ev.emit(Event{Event: "kill", Run: ir.o.RunID, Lanes: ns, At: now()})
	for _, n := range ns {
		ir.signalLocked(ir.procs[n], syscall.SIGKILL)
	}
}

// signalLocked sends sig to a lane: to its whole process group (bash, swim
// step and the step command) when it has one.
func (ir *interrupter) signalLocked(lp *laneProc, sig syscall.Signal) {
	if lp.group {
		syscall.Kill(-lp.proc.Pid, sig)
		return
	}
	lp.proc.Signal(sig)
}
