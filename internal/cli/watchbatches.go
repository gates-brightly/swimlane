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
	"github.com/gates-brightly/swimlane/internal/lane"
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
	res, interrupted := runClaimed(root, cfg, e, yes, false, false, true)
	switch {
	case interrupted:
		return exitError{batchInterrupted}
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

// startable lists the waiting batches that may start now, oldest first,
// as many as there's room for: none of a batch's lanes may be in a running
// batch (any worker's), running in another swim, or in a batch picked
// before it.
func (b *batchRunner) startable(cfg *config.Config, waiting []*queue.Entry) []*queue.Entry {
	b.mu.Lock()
	room := b.max - len(b.procs)
	b.mu.Unlock()
	taken := map[int]bool{}
	for _, e := range waiting {
		if e.State == queue.Running {
			for _, l := range e.Lanes {
				taken[l.Lane] = true
			}
		}
	}
	st, _ := status.Load(b.root)
	var out []*queue.Entry
	for _, e := range waiting {
		if room <= 0 {
			break
		}
		if e.State != queue.Queued {
			continue
		}
		free := true
		for _, l := range e.Lanes {
			if taken[l.Lane] || laneBusy(b.root, st, l.Lane) != "" {
				free = false
			}
		}
		// Its lanes count as taken either way: a later batch doesn't get
		// ahead of this one on the lanes they share.
		for _, l := range e.Lanes {
			taken[l.Lane] = true
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
			if l.PID > 0 && lane.Alive(l.PID) {
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
	p := &batchProc{e: claimed, cmd: cmd, done: make(chan struct{})}
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
	queue.Consume(b.root, e, queue.Result{Started: at, Finished: at, Exit: 1, Result: "error", Error: "swim watch couldn't start the batch: " + err.Error()})
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
	sigs chan os.Signal, say func(string), setActivity func(string)) (int, error) {
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
			case "passed":
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
		return fmt.Sprintf("#%d has no output here (another worker, or swim watch --batches 1 runs it on the screen)", e.Seq)
	}
	done := func() bool {
		if bp == nil {
			return e.State != queue.Running
		}
		select {
		case <-bp.done:
			return true
		default:
			return false
		}
	}
	title := fmt.Sprintf("swim watch · #%d %s (%s)", e.Seq, e.ID, lanesText(laneNums(e)))
	views := []display.FollowView{
		{Name: "overview", Lines: display.FileLines(path)},
		{Name: "verbose", Lines: func() []string { return verboseLines(w.root, laneNums(e)) }},
	}
	if err := display.Follow(os.Stdin, os.Stdout, title, views, done); err != nil {
		return err.Error()
	}
	return ""
}

// verboseLines are the batch's lanes' current rounds as their logs hold
// them, each line marked with its lane (or job, or cell), and the running
// step's output so far (its spool): everything the overview leaves out.
func verboseLines(root string, lanes []int) []string {
	var out []string
	for _, n := range lanes {
		logs := []string{lane.Log(root, n)}
		more, _ := filepath.Glob(filepath.Join(filepath.Dir(lane.Log(root, n)), fmt.Sprintf("agent%d.*.log", n)))
		for _, m := range more {
			if !strings.Contains(filepath.Base(m), ".prev-") {
				logs = append(logs, m)
			}
		}
		for _, path := range logs {
			tag := "[" + strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "agent"), ".log") + "] "
			if rounds, _ := splitRounds(path); len(rounds) > 0 {
				for _, l := range rounds[len(rounds)-1] {
					out = append(out, tag+l)
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
	}
	return "failed"
}
