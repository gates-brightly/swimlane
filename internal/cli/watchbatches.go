package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/launcher"
	"github.com/gates-brightly/swimlane/internal/queue"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/step"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// swim watch runs queued batches side by side, up to --batches (or
// watch_batches): each in a process of its own (swim _batch), so one
// batch's guard flags and queue variables never reach another's. A batch
// starts once none of its lanes is in another batch or another swim run,
// oldest first: a later batch whose lanes are free starts ahead of one
// that has to wait. Lanes that depend on (After:) or share locks with a
// lane of another batch wait for it as they do across swim runs.

// batchInterrupted is swim _batch's exit status for an interrupted batch.
const batchInterrupted = 130

// cmdBatch runs one batch swim watch claimed: swim _batch ENTRY [--yes].
// It becomes the claim's worker (its liveness and heartbeat), runs the
// batch with plain output and consumes the entry, as swim watch would.
func cmdBatch(args []string) error {
	var yes bool
	rest, err := flags{bools: map[string]*bool{"yes": &yes}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return usagef("usage: swim _batch ENTRY [--yes]")
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	e, err := queue.Load(rest[0])
	if err != nil {
		return err
	}
	if err := queue.Adopt(e); err != nil {
		return err
	}
	res, interrupted := runClaimed(root, cfg, e, yes, false, true)
	switch {
	case interrupted:
		return exitError{batchInterrupted}
	case res.Exit == launcher.ExitDryRun:
		return exitError{launcher.ExitDryRun}
	case res.Exit != 0:
		return exitError{1}
	}
	return nil
}

// batchProc is a batch running in its own process.
type batchProc struct {
	e     *queue.Entry
	lanes []int
	cmd   *exec.Cmd
	out   string        // its output file ("" when it writes to swim watch's)
	done  chan struct{} // closed when it exits
	code  int
	// started is when the worker started it, for the batch's elapsed time.
	started time.Time
}

// batchRunner starts and tracks swim watch's batches.
type batchRunner struct {
	root   string
	yes    bool
	toFile bool // the watch screen is up: each batch writes to a file of its own
	max    int

	mu    sync.Mutex
	procs map[string]*batchProc // by entry id
	wake  chan struct{}         // a batch exited
}

func newBatchRunner(root string, yes, toFile bool, max int) *batchRunner {
	return &batchRunner{root: root, yes: yes, toFile: toFile, max: max, procs: map[string]*batchProc{}, wake: make(chan struct{}, 1)}
}

// batchOutPath is where a batch's output goes while the watch screen is
// up: .swim/queue/out/<seq>-<id>.log.
func batchOutPath(root string, e *queue.Entry) string {
	base := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(e.Path), ".running"), ".queue")
	return filepath.Join(queue.Dir(root), "out", base+".log")
}

// running lists the batches still running, oldest first.
func (b *batchRunner) running() []*batchProc {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []*batchProc
	for _, p := range b.procs {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].e.Seq < out[j].e.Seq })
	return out
}

// proc returns the running batch with entry id.
func (b *batchRunner) proc(id string) *batchProc {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.procs[id]
}

// reap removes the batches that have exited and returns them.
func (b *batchRunner) reap() []*batchProc {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []*batchProc
	for id, p := range b.procs {
		select {
		case <-p.done:
			out = append(out, p)
			delete(b.procs, id)
		default:
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].e.Seq < out[j].e.Seq })
	return out
}

// full reports whether as many batches run as allowed.
func (b *batchRunner) full() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.procs) >= b.max
}

// startable lists the waiting batches that may start now, in start-time
// order (queue.SortWaiting), as many as there's room for: due (one an
// older swim scheduled not before its time), and none of the lanes
// it runs (its own, and the Always lanes they pull in) in a running batch
// (any worker's), running in another swim, or in a batch picked before it.
func (b *batchRunner) startable(cfg *config.Config, waiting []*queue.Entry) []*queue.Entry {
	b.mu.Lock()
	room := b.max - len(b.procs)
	b.mu.Unlock()
	waiting = append([]*queue.Entry(nil), waiting...)
	queue.SortWaiting(waiting)
	now := time.Now()
	// A batch needs its own lanes and the Always lanes its run pulls in
	// (After: 1 on an Always lane 1): two batches that both pull in lane 1
	// can't run side by side, or the second finds it already running and
	// fails at once.
	needs := func(e *queue.Entry) []int {
		var ns []int
		for _, l := range e.Lanes {
			ns = append(ns, l.Lane)
		}
		return ns
	}
	taken := map[int]bool{}
	for _, e := range waiting {
		if e.State == queue.Running {
			for _, n := range needs(e) {
				taken[n] = true
			}
		}
	}
	st, _ := status.Load(b.root)
	var out []*queue.Entry
	for _, e := range waiting {
		if room <= 0 {
			break
		}
		if e.State != queue.Queued || !e.Due(now) {
			continue // a batch scheduled for later holds no lanes yet
		}
		if why := cantRunHere(b.root, e); why != "" {
			holdHere(b.root, e, why) // queued for a worker that can; holds no lanes here
			continue
		}
		holdHere(b.root, e, "")
		free := true
		need := needs(e)
		for _, n := range need {
			if taken[n] || laneBusy(b.root, st, n) != "" {
				free = false
			}
		}
		// Its lanes count as taken either way: a later batch doesn't get
		// ahead of this one on the lanes they share.
		for _, n := range need {
			taken[n] = true
		}
		if free {
			out = append(out, e)
			room--
		}
	}
	return out
}

// laneBusy says why lane n can't start now ("" when it can): its script
// is running, or another swim run has it waiting, queued or locked.
func laneBusy(root string, st *status.File, n int) string {
	if pid, ok := lane.Running(root, n); ok {
		return fmt.Sprintf("swim %d is running (pid %d)", n, pid)
	}
	if st == nil {
		return ""
	}
	if l := st.Get(n); l != nil {
		switch l.State {
		case status.Waiting, status.Queued, status.Locked, status.Running:
			if l.Alive() {
				return fmt.Sprintf("swim %d is %s in another swim run (pid %d)", n, l.State, l.PID)
			}
		}
	}
	return ""
}

// start claims e and runs it in a process of its own, in swim watch's
// process group: Ctrl-C at the terminal reaches it as it reaches swim
// watch (its launcher interrupts the batch, and escalates as swim run
// does), and so does a kill of the group.
func (b *batchRunner) start(cfg *config.Config, e *queue.Entry) (*batchProc, error) {
	claimed, err := queue.Claim(b.root, e, operator(), cfg.QueueLeaseFor())
	if err != nil {
		return nil, err
	}
	args := []string{"_batch", claimed.Path}
	if b.yes {
		args = append(args, "--yes")
	}
	cmd := exec.Command(self(), args...)
	cmd.Dir = b.root
	p := &batchProc{e: claimed, cmd: cmd, done: make(chan struct{}), started: time.Now()}
	for _, l := range claimed.Lanes {
		p.lanes = append(p.lanes, l.Lane)
	}
	var out io.WriteCloser
	if b.toFile {
		p.out = batchOutPath(b.root, claimed)
		os.MkdirAll(filepath.Dir(p.out), 0o755)
		f, err := os.Create(p.out)
		if err != nil {
			b.fail(claimed, err)
			return nil, err
		}
		out = f
		cmd.Stdout, cmd.Stderr = f, f
	} else {
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	}
	if err := cmd.Start(); err != nil {
		if out != nil {
			out.Close()
		}
		b.fail(claimed, err)
		return nil, err
	}
	b.mu.Lock()
	b.procs[claimed.ID] = p
	b.mu.Unlock()
	go func() {
		err := cmd.Wait()
		if out != nil {
			out.Close()
		}
		if ee, ok := err.(*exec.ExitError); ok {
			p.code = ee.ExitCode()
		} else if err != nil {
			p.code = 1
		}
		close(p.done)
		select {
		case b.wake <- struct{}{}:
		default:
		}
	}()
	return p, nil
}

// fail consumes a claimed batch that couldn't start, with the reason.
func (b *batchRunner) fail(e *queue.Entry, err error) {
	at := time.Now().UTC().Format(time.RFC3339)
	why := "swim watch couldn't start the batch: " + err.Error()
	queue.Consume(b.root, e, queue.Result{Started: at, Finished: at, Exit: 1, Result: "error", Error: why})
	history.Log(b.root, history.Entry{Event: history.QueueDone, Detail: fmt.Sprintf("%s: error: %s", e.ID, why)})
}

// signal sends sig to every running batch's process.
func (b *batchRunner) signal(sig os.Signal) {
	for _, p := range b.running() {
		if p.cmd.Process != nil {
			p.cmd.Process.Signal(sig)
		}
	}
}

// lanesText is a batch's lanes for a message: "swim 1,3".
func lanesText(lanes []int) string {
	var ns []string
	for _, n := range lanes {
		ns = append(ns, strconv.Itoa(n))
	}
	return "swim " + strings.Join(ns, ",")
}

// watchBatches is swim watch's loop when it runs batches side by side
// (batches > 1): it starts every batch that may start, then waits for one
// to end, the queue to change or a signal. Ctrl-C (or SIGTERM) interrupts
// the batches in hand, as it did the one batch in hand, and stops it
// taking new ones; it exits once they've ended. It returns swim watch's
// exit status: 1 if a batch failed.
func watchBatches(root string, cfg *config.Config, b *batchRunner, view *watchView, once, approved bool, every time.Duration,
	sigs chan os.Signal, say func(string)) (int, error) {
	p := ui.Painter{On: ui.ColorEnabled(os.Stdout)}
	code := 0
	stopping := false // no new batches: the ones in hand finish
	if view != nil {
		view.batches = b
	}
	note := func(msg string) {
		if view != nil {
			view.picker.Note = msg
			return
		}
		fmt.Println(p.Paint(ui.Yellow, "swim watch: "+msg))
	}
	// A terminal's Ctrl-C reaches the batches itself (one process
	// group); anything else is passed on to them.
	interrupt := func(sig os.Signal) {
		if sig != syscall.SIGINT {
			b.signal(sig)
		}
		if running := b.running(); !stopping && len(running) > 0 {
			note(fmt.Sprintf("interrupting %d batches; taking no new ones", len(running)))
		}
		stopping = true
	}
	for {
		for _, bp := range b.reap() {
			word := batchOutcome(root, bp)
			switch word {
			case "passed", "skipped": // a dry run waits on an approval; it didn't fail
			case "interrupted":
				stopping = true
			default:
				code = 1
			}
			if view != nil {
				view.picker.Note = fmt.Sprintf("#%d %s (%s)", bp.e.Seq, word, lanesText(bp.lanes))
			}
		}
	drain:
		for {
			select {
			case sig := <-sigs:
				interrupt(sig)
			default:
				break drain
			}
		}
		if stopping && len(b.running()) == 0 {
			return code, nil
		}
		if approved && !stopping {
			for _, msg := range queueApproved(root, cfg) {
				fmt.Println(p.Paint(ui.Cyan, "swim watch: "+msg))
			}
		}
		if c, err := loadCfg(root); err == nil {
			cfg = c
		}
		if !stopping {
			unlock, ok, err := queue.LockWorker(root)
			if err != nil {
				return code, err
			}
			if ok {
				// Listed under the lock: another worker picks only after this
				// one's claims are made.
				waiting, err := queue.List(root, false)
				if err != nil {
					unlock()
					return code, err
				}
				if _, paused := lane.RepoPause(root); paused {
					waiting = nil // swim pause --all: no batch starts until swim unpause --all
				}
				for _, e := range b.startable(cfg, waiting) {
					bp, err := b.start(cfg, e)
					if err != nil {
						note(fmt.Sprintf("#%d: %v", e.Seq, err))
						continue
					}
					if view == nil {
						fmt.Println(p.Paint(ui.Bold, fmt.Sprintf("swim watch: started %s (#%d), %s", bp.e.ID, bp.e.Seq, lanesText(bp.lanes))))
					}
				}
				unlock()
			}
		}
		waiting, err := queue.List(root, false)
		if err != nil {
			return code, err
		}
		queued := 0
		for _, e := range waiting {
			if e.State == queue.Queued {
				queued++
			}
		}
		running := b.running()
		if once && len(running) == 0 && (queued == 0 || stopping) {
			return code, nil
		}
		var parts []string
		for _, bp := range running {
			parts = append(parts, fmt.Sprintf("%s (#%d, %s)", bp.e.ID, bp.e.Seq, lanesText(bp.lanes)))
		}
		switch {
		case len(running) > 0 && queued > 0:
			say(fmt.Sprintf("running %s; %d queued, waiting for their lanes or a free slot", strings.Join(parts, ", "), queued))
		case len(running) > 0:
			say("running " + strings.Join(parts, ", "))
		case queued > 0:
			say(fmt.Sprintf("%d queued; waiting for their lanes to be free", queued))
		default:
			say("queue empty; waiting for swim queue add")
		}
		if view == nil {
			select {
			case sig := <-sigs:
				interrupt(sig)
			case <-b.wake:
			case <-time.After(every):
			}
			continue
		}
		var sig os.Signal
		ok := view.wait(func() bool {
			select {
			case sig = <-sigs:
				return true
			case <-b.wake:
				return true
			default:
			}
			if stopping {
				return false
			}
			w, _ := queue.List(root, false)
			return len(b.startable(cfg, w)) > 0
		})
		switch {
		case sig != nil:
			interrupt(sig)
		case !ok:
			// Ctrl-C on the screen is a key, not a signal: pass it on.
			b.signal(syscall.SIGINT)
			interrupt(syscall.SIGINT)
		}
	}
}

// showOutput follows a batch's output on the screen until Esc: one this
// swim watch runs, or one it ran (the file outlives the batch).
func (w *watchView) showOutput(e *queue.Entry) string {
	var bp *batchProc
	if w.batches != nil {
		bp = w.batches.proc(e.ID)
	}
	path := batchOutPath(w.root, e)
	if bp != nil {
		path = bp.out
	}
	if _, err := os.Stat(path); err != nil {
		if e.State == queue.Queued {
			return fmt.Sprintf("#%d hasn't started yet", e.Seq)
		}
		return fmt.Sprintf("#%d has no output here (another swimmer, or swim watch --batches 1 runs it on the screen)", e.Seq)
	}
	end := func() *display.FollowEnd { return batchEnd(w.root, e.ID, bp) }
	title := fmt.Sprintf("swim watch · #%d %s (%s)", e.Seq, e.ID, lanesText(laneNums(e)))
	views := []display.FollowView{
		{Name: "overview", Lines: display.FileLines(path)},
		{Name: "verbose", Lines: func() []string { return verboseLines(w.root, laneNums(e)) }},
	}
	if err := display.Follow(os.Stdin, os.Stdout, title, views, end); err != nil {
		return err.Error()
	}
	return ""
}

// showLaneOutput follows lane n's log while it runs: in a batch claimed
// here or by another worker (v then shows that batch's overview, when it
// runs here), or on its own (swim run in another terminal).
func (w *watchView) showLaneOutput(n int) string {
	var batch *queue.Entry
	entries, _ := queue.List(w.root, false)
	for _, e := range entries {
		if e.State != queue.Running {
			continue
		}
		for _, l := range e.Lanes {
			if l.Lane == n {
				batch = e
			}
		}
	}
	running := func() bool { _, ok := lane.Running(w.root, n); return ok }
	if batch == nil && !running() {
		return fmt.Sprintf("swim %d isn't running: enter follows a running lane's log (swim log %d shows its last round)", n, n)
	}
	title := fmt.Sprintf("swim watch · swim %d", n)
	views := []display.FollowView{{Name: "log", Lines: func() []string { return verboseLines(w.root, []int{n}) }}}
	end := func() *display.FollowEnd { return laneEnd(w.root, n, running) }
	if batch != nil {
		title += fmt.Sprintf(" · #%d %s", batch.Seq, batch.ID)
		var bp *batchProc
		if w.batches != nil {
			bp = w.batches.proc(batch.ID)
		}
		path := batchOutPath(w.root, batch)
		if bp != nil {
			path = bp.out
		}
		if _, err := os.Stat(path); err == nil {
			views = append(views, display.FollowView{Name: "batch", Lines: display.FileLines(path)})
		}
		// A lane waiting on others in its batch isn't running yet: follow
		// until the batch ends, not until the lane next looks idle; then
		// the header says how the lane ended.
		id := batch.ID
		end = func() *display.FollowEnd {
			be := batchEnd(w.root, id, bp)
			if be == nil {
				return nil
			}
			if le := laneEnd(w.root, n, func() bool { return false }); le != nil {
				return le
			}
			return be
		}
	}
	if err := display.Follow(os.Stdin, os.Stdout, title, views, end); err != nil {
		return err.Error()
	}
	return ""
}

// showLaneLogs (l on a lane) shows lane n's current log, every round in
// it with its job logs, whether or not it runs; while it runs it
// follows the log, the running step's output included.
func (w *watchView) showLaneLogs(n int) string {
	if len(laneLogFiles(w.root, n)) == 0 {
		return fmt.Sprintf("swim %d has no current log (swim log %d --all shows its archives)", n, n)
	}
	running := func() bool { _, ok := lane.Running(w.root, n); return ok }
	title := fmt.Sprintf("swim watch · swim %d · %s", n, rel(w.root, lane.Log(w.root, n)))
	views := []display.FollowView{
		{Name: "log", Lines: func() []string { return logLines(w.root, []int{n}, true) }},
		{Name: "last round", Lines: func() []string { return logLines(w.root, []int{n}, false) }},
	}
	if err := display.Follow(os.Stdin, os.Stdout, title, views, func() *display.FollowEnd { return laneEnd(w.root, n, running) }); err != nil {
		return err.Error()
	}
	return ""
}

// showBatchLogs (l on a batch) shows its lanes' logs, their latest round
// each: the last time they ran for a waiting batch, as they go for a
// running one. v switches to the batch's own output, when it has one.
func (w *watchView) showBatchLogs(e *queue.Entry) string {
	lanes := laneNums(e)
	has := false
	for _, n := range lanes {
		has = has || len(laneLogFiles(w.root, n)) > 0
	}
	if !has {
		return fmt.Sprintf("#%d's lanes (%s) have no logs yet", e.Seq, lanesText(lanes))
	}
	title := fmt.Sprintf("swim watch · #%d %s (%s) · logs", e.Seq, e.ID, lanesText(lanes))
	views := []display.FollowView{{Name: "logs", Lines: func() []string { return logLines(w.root, lanes, false) }}}
	var bp *batchProc
	if w.batches != nil {
		bp = w.batches.proc(e.ID)
	}
	path := batchOutPath(w.root, e)
	if bp != nil {
		path = bp.out
	}
	if _, err := os.Stat(path); err == nil {
		views = append(views, display.FollowView{Name: "batch", Lines: display.FileLines(path)})
	}
	end := func() *display.FollowEnd {
		if cur, err := queue.Find(w.root, e.ID); err == nil && cur.State == queue.Queued {
			return nil // a waiting batch's logs are its lanes' last rounds: no end yet
		}
		return batchEnd(w.root, e.ID, bp)
	}
	if err := display.Follow(os.Stdin, os.Stdout, title, views, end); err != nil {
		return err.Error()
	}
	return ""
}

// batchEnd is how batch id ended, for the follow view's header: nil while
// it waits or runs (here, or for another worker). The details name each
// lane that didn't pass, and why: its failed steps, or what skipped it.
func batchEnd(root, id string, bp *batchProc) *display.FollowEnd {
	if bp != nil {
		select {
		case <-bp.done:
		default:
			return nil
		}
	}
	e, err := queue.Find(root, id)
	if err == nil && (e.State == queue.Queued || e.State == queue.Running) && bp == nil {
		return nil
	}
	end := &display.FollowEnd{}
	if err != nil || e.Result == nil {
		if bp == nil {
			return &display.FollowEnd{Result: "finished"}
		}
		code := bp.code
		end.Result, end.Exit, end.Elapsed = batchOutcome(root, bp), &code, time.Since(bp.started)
		return end
	}
	r := e.Result
	exit := r.Exit
	end.Result, end.Exit = r.Result, &exit
	if a, err := time.Parse(time.RFC3339, r.Started); err == nil {
		if b, err := time.Parse(time.RFC3339, r.Finished); err == nil {
			end.Elapsed = b.Sub(a)
		}
	}
	st, _ := status.Load(root)
	for _, l := range r.Lanes {
		if l.Result == status.Passed {
			continue
		}
		if l.Reason != "" {
			end.Details = append(end.Details, fmt.Sprintf("swim %d %s: %s", l.Lane, l.Result, l.Reason))
			continue
		}
		var failed []string
		if st != nil {
			if sl := st.Get(l.Lane); sl != nil {
				failed = sl.FailedSteps
			}
		}
		if len(failed) == 0 {
			end.Details = append(end.Details, fmt.Sprintf("swim %d %s", l.Lane, l.Result))
		}
		for _, f := range failed {
			end.Details = append(end.Details, fmt.Sprintf("swim %d: %s", l.Lane, strings.TrimSpace(f)))
		}
	}
	if r.Error != "" {
		end.Details = append(end.Details, r.Error)
	}
	return end
}

// laneEnd is how lane n's round ended, for the follow view's header, from
// its status: nil while it runs, or when it has no finished round.
func laneEnd(root string, n int, running func() bool) *display.FollowEnd {
	if running() {
		return nil
	}
	st, _ := status.Load(root)
	if st == nil {
		return nil
	}
	l := st.Get(n)
	if l == nil {
		return nil
	}
	switch l.State {
	case status.Passed, status.Failed, status.Interrupted, status.Skipped:
	default:
		return nil
	}
	end := &display.FollowEnd{Result: l.State, Exit: l.ExitCode}
	if l.DurationS != nil {
		end.Elapsed = time.Duration(*l.DurationS * float64(time.Second))
	}
	for _, f := range l.FailedSteps {
		end.Details = append(end.Details, strings.TrimSpace(f))
	}
	if l.Reason != "" && len(end.Details) == 0 {
		end.Details = append(end.Details, l.Reason)
	}
	return end
}

// laneLogFiles are lane n's current logs: its own, then its jobs' and
// read-only check's (not archives).
func laneLogFiles(root string, n int) []string {
	var logs []string
	if _, err := os.Stat(lane.Log(root, n)); err == nil {
		logs = append(logs, lane.Log(root, n))
	}
	more, _ := filepath.Glob(filepath.Join(filepath.Dir(lane.Log(root, n)), fmt.Sprintf("agent%d.*.log", n)))
	for _, m := range more {
		if !strings.Contains(filepath.Base(m), ".prev-") {
			logs = append(logs, m)
		}
	}
	return logs
}

// verboseLines are the batch's lanes' current rounds as their logs hold
// them, each line marked with its lane (or job), and the running
// step's output so far (its spool): everything the overview leaves out.
func verboseLines(root string, lanes []int) []string { return logLines(root, lanes, false) }

// logLines are lanes' current logs (their own, their jobs'),
// each line marked with its log, and the running step's output so far:
// every round in them with all, else the latest.
func logLines(root string, lanes []int, all bool) []string {
	var out []string
	for _, n := range lanes {
		for _, path := range laneLogFiles(root, n) {
			tag := "[" + strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "agent"), ".log") + "] "
			if rounds, _ := splitRounds(path); len(rounds) > 0 {
				if !all {
					rounds = rounds[len(rounds)-1:]
				}
				for _, r := range rounds {
					for _, l := range r {
						out = append(out, tag+l)
					}
				}
			}
			if sp, ok := step.ReadSpool(path); ok {
				out = append(out, tag+"  ==> "+sp.Label+" (running)", tag+"  $ "+sp.Cmd)
				for _, l := range sp.Output {
					out = append(out, tag+"  | "+l)
				}
				if sp.Partial != "" {
					out = append(out, tag+"  | "+sp.Partial)
				}
			}
		}
	}
	return out
}

// laneNums are a batch's lane numbers.
func laneNums(e *queue.Entry) []int {
	var ns []int
	for _, l := range e.Lanes {
		ns = append(ns, l.Lane)
	}
	return ns
}

// batchOutcome is how a batch ended: the result its consumed entry
// records (a signal can end the process after it consumed it), else its
// exit status.
func batchOutcome(root string, bp *batchProc) string {
	if e, err := queue.Find(root, bp.e.ID); err == nil && e.State == queue.Done && e.Result != nil {
		return e.Result.Result
	}
	switch bp.code {
	case 0:
		return "passed"
	case batchInterrupted:
		return "interrupted"
	case launcher.ExitDryRun:
		return "skipped"
	}
	return "failed"
}
