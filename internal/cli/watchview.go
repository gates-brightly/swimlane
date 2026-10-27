package cli

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/launcher"
	"github.com/gates-brightly/swimlane/internal/queue"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// watchView is swim watch's screen on a terminal while it waits: every
// lane's job and the queue, with ↑ ↓ to pick one, r to run it now and q to
// add it to the next batch (s submits the batch, as swim queue add N M
// does, so its lanes' After: order holds). A batch the worker takes from
// the queue still runs on its own, as without the screen.
type watchView struct {
	root     string
	cfg      *config.Config
	picker   *display.Picker
	yes      bool         // swim watch -y: approves what a batch doesn't
	activity string       // what the worker is waiting for
	batches  *batchRunner // batches running side by side (nil: one at a time)
	noStream bool         // o: the running output under the queue is hidden
	// staged are the lanes q added to the next batch, in order: s submits
	// them as one batch, x clears them.
	staged []int
}

const (
	watchKeys  = "rqsxlop" + string(display.PickEnter)
	watchHints = "↑ ↓ pick  enter output  l logs  o stream  r run now  q batch  s submit  p pause  ^C quit" // x (clear the batch) is on the submit row
)

func newWatchView(root string, cfg *config.Config, yes bool) *watchView {
	w := &watchView{root: root, cfg: cfg, yes: yes, picker: &display.Picker{In: os.Stdin, Out: os.Stdout, Color: ui.ColorEnabled(os.Stdout)}}
	w.picker.Tail = w.stream
	// The repo-wide pause (swim pause --all) is on the title while it holds.
	w.picker.Title = func() string {
		t := "swim watch · " + w.root
		if _, on := lane.RepoPause(w.root); on {
			t += " · paused: swimmers claim nothing · swim unpause --all"
		}
		return t
	}
	return w
}

// stream is the screen's live part, under the queue: the latest lines of
// every running lane's log (its running step's output included), at most
// n in all, shared between the lanes. o hides it.
func (w *watchView) stream(n int) []display.PickRow {
	if w.noStream {
		return nil
	}
	st, _ := status.Load(w.root)
	var running []int
	for _, l := range lane.Associated(w.root) {
		if _, ok := lane.Running(w.root, l); ok {
			running = append(running, l)
		} else if st != nil {
			if s := st.Get(l); s != nil && s.State == status.Running && s.Alive() {
				running = append(running, l)
			}
		}
	}
	if len(running) == 0 || n < 2 {
		return nil
	}
	rows := []display.PickRow{{Text: "Running"}}
	n--
	// Each lane gets a header and an even share of the lines (2 at least);
	// those that don't fit are counted.
	shown := min(len(running), n/3)
	if shown == 0 {
		shown = 1
	}
	per := n/shown - 1
	for i, l := range running[:shown] {
		head := fmt.Sprintf("swim %d", l)
		if st != nil {
			if s := st.Get(l); s != nil && s.CurrentStep != "" {
				head += " · " + s.CurrentStep
			}
		}
		rows = append(rows, display.PickRow{Text: head, Color: ui.LaneColor(l) + ui.Bold})
		lines := logLines(w.root, []int{l}, false)
		if i == shown-1 {
			per = n - (len(rows) - 1) // the last lane takes what's left
		}
		if len(lines) > per {
			lines = lines[len(lines)-per:]
		}
		for _, line := range lines {
			rows = append(rows, display.PickRow{Text: "  " + line, Color: ui.Dim})
		}
	}
	if more := len(running) - shown; more > 0 {
		rows[0].Text = fmt.Sprintf("Running  (+%d more: l on a lane shows its log)", more)
	}
	return rows
}

// watchViewAllowed reports whether swim watch can show its screen: a
// terminal on both ends, and the interactive view allowed (not CI,
// SWIM_TUI=0 or --no-tui).
func watchViewAllowed(noTUI bool) bool {
	return !noTUI && ui.IsTTY(os.Stdin) && ui.IsTTY(os.Stdout) && display.TUIAllowed(os.Getenv)
}

// rows is the screen's list: the worker's activity, every lane, the queue.
func (w *watchView) rows() []display.PickRow {
	var rows []display.PickRow
	if w.activity != "" {
		rows = append(rows, display.PickRow{Text: w.activity, Color: ui.Dim}, display.PickRow{Text: " ", Color: ui.Dim})
	}
	rows = append(rows, display.PickRow{Text: "Jobs"})
	st, _ := status.Load(w.root)
	now := time.Now()
	for _, n := range lane.Associated(w.root) { // the lanes in use now (a long-lived view)
		var l *status.Lane
		if st != nil {
			l = st.Get(n)
		}
		info, _ := lane.ReadScript(w.root, n)
		state, color := laneRowState(l, info)
		round := "(no pending round)"
		job := "        "
		if info.Pending() {
			round = info.Round
			if info.Job != "" {
				job = fmt.Sprintf("%-8s", lane.ShortJob(info.Job))
			}
		} else {
			color = ui.Dim
		}
		if l != nil && len(l.DryRun) > 0 && state != "pending" {
			round = display.DryMark + " " + round
		}
		text := fmt.Sprintf("swim %-2d  %-11s %-8s %s  %s", n, state, laneTimer(l, state, now), job, round)
		if d := laneRowDetail(l, state); d != "" {
			text += "  · " + d
		}
		if w.isStaged(n) {
			text += "  [+ next batch]"
			color = ui.Green
		}
		rows = append(rows, display.PickRow{ID: "lane:" + strconv.Itoa(n), Text: text, Color: color})
	}
	rows = append(rows, display.PickRow{Text: " ", Color: ui.Dim}, display.PickRow{Text: "Queue"})
	if len(w.staged) > 0 {
		rows = append(rows, display.PickRow{ID: "submit", Color: ui.Green + ui.Bold,
			Text: fmt.Sprintf("▶ submit swim %s as one batch  (enter here, or s; x clears)", joinNums(w.staged))})
	}
	entries, err := queue.List(w.root, false)
	if err != nil {
		rows = append(rows, display.PickRow{Text: "  " + err.Error(), Color: ui.Red})
	}
	if len(entries) == 0 && err == nil {
		rows = append(rows, display.PickRow{Text: "  (empty: q on jobs, then s, queues them as one batch)", Color: ui.Dim})
	}
	queue.SortWaiting(entries)
	for _, e := range entries {
		var ns []string
		for _, l := range e.Lanes {
			ns = append(ns, strconv.Itoa(l.Lane))
		}
		state, when := e.State, ""
		if e.State == queue.Queued && !e.Due(now) { // scheduled by an older swim
			state, when = "scheduled", "  not before "+e.NotBefore
		}
		dry := ""
		if w.batchDry(e, st) {
			dry = display.DryMark + " "
		}
		text := fmt.Sprintf("#%-4d %-9s %-8s %sswim %s  by %s%s", e.Seq, state, queueTimer(e, state, now), dry, strings.Join(ns, ","), e.By, when)
		if e.Note != "" {
			text += "  " + e.Note
		}
		color := ""
		switch state {
		case "scheduled", "held":
			color = ui.Dim
		case queue.Running:
			color = ui.Cyan
		case queue.Orphaned:
			color = ui.Red
		}
		rows = append(rows, display.PickRow{ID: "queue:" + e.ID, Text: text, Color: color})
	}
	return rows
}

// batchDry says whether batch e runs (or ran) a guard as a dry run: while
// waiting, a guard of its lanes that neither the batch nor the worker's -y
// approves (signed approvals may still cover it when it runs); once
// running, a guard its lanes' rounds dry-ran.
func (w *watchView) batchDry(e *queue.Entry, st *status.File) bool {
	if e.State != queue.Queued {
		for _, l := range e.Lanes {
			if st != nil {
				if s := st.Get(l.Lane); s != nil && len(s.DryRun) > 0 && e.Result == nil {
					return true
				}
			}
		}
		return false
	}
	if e.ApproveAll || w.yes {
		return false
	}
	approved := map[string]bool{}
	for _, f := range e.Approve {
		approved[f] = true
	}
	for _, l := range e.Lanes {
		info, err := lane.ReadScript(w.root, l.Lane)
		if err != nil {
			continue
		}
		for _, g := range info.GuardFlags() {
			if !approved[g] {
				return true
			}
		}
	}
	return false
}

// laneTimer is a lane row's time: how long it has been running (or
// waiting) in this round, "1m5s", or how long ago its round finished,
// "3h ago". A pending lane (a new job not run yet) shows none.
func laneTimer(l *status.Lane, state string, now time.Time) string {
	if l == nil || state == "pending" || state == "idle" {
		return ""
	}
	switch l.State {
	case status.Running, status.Waiting, status.Queued, status.Locked:
		if t, ok := statusTime(l.StartedAt); ok {
			return display.Took(now.Sub(t))
		}
		return ""
	}
	if t, ok := statusTime(l.FinishedAt); ok {
		return display.Ago(now.Sub(t))
	}
	return ""
}

// queueTimer is a batch row's time: how long it has been running since a
// worker claimed it, or how long ago it was queued. A scheduled batch's row
// already says when it starts, so it has none.
func queueTimer(e *queue.Entry, state string, now time.Time) string {
	if state == "scheduled" {
		return ""
	}
	if state == queue.Running && e.Worker != nil {
		if t, err := time.Parse(time.RFC3339, e.Worker.Claimed); err == nil {
			return display.Took(now.Sub(t))
		}
	}
	if t, err := time.Parse(time.RFC3339, e.Created); err == nil {
		return display.Ago(now.Sub(t))
	}
	return ""
}

func statusTime(s *string) (time.Time, bool) {
	if s == nil || *s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, *s)
	return t, err == nil
}

// laneRowState is how a lane's row shows its state: a script holding a job
// that hasn't run yet is pending, whatever its last round did.
func laneRowState(l *status.Lane, info lane.Info) (string, string) {
	if l != nil && l.PausedAt != "" && l.Alive() {
		return "paused", ui.Yellow
	}
	if l == nil {
		if info.Pending() {
			return "pending", ""
		}
		return "idle", ui.Dim
	}
	state := l.State
	active := state == status.Running || state == status.Waiting || state == status.Queued || state == status.Locked
	if active && !l.Alive() {
		return "stale", ui.Yellow
	}
	if !active && info.Pending() && (state == "" || state == status.Idle || info.Job != "" && info.Job != l.Job) {
		return "pending", ""
	}
	if state == "" {
		state = "idle"
	}
	return state, ui.StateColor(state)
}

// laneRowDetail is what a waiting, locked or queued lane waits on, for
// the end of its row, as swim status and swim run's view say it: "waiting
// on swim 2, 3", "waiting for lock db", "2 ahead". "" for any other state
// (a stale row's waiting process is gone).
func laneRowDetail(l *status.Lane, state string) string {
	if l == nil {
		return ""
	}
	switch state {
	case status.Waiting:
		if len(l.WaitingOn) > 0 {
			return display.WaitingText(l.WaitingOn)
		}
		return ""
	case status.Locked, status.Queued:
		return l.Reason
	}
	return ""
}

// wait shows the screen until until() is true (the worker has a batch to
// run, or a signal came), or the operator acts: r and q are done here and
// the screen comes back. It returns false when the operator quit.
func (w *watchView) wait(until func() bool) bool {
	for {
		cfg, err := loadCfg(w.root)
		if err == nil {
			w.cfg = cfg
		}
		res, err := w.picker.Pick("swim watch · "+w.root, watchHints, watchKeys, w.rows, time.Second, until)
		if err != nil {
			return true // no screen after all: the caller just polls
		}
		switch {
		case res.Quit:
			return false
		case res.ID == "":
			return true
		}
		w.picker.Note = w.act(res.ID, res.Key)
	}
}

// act does key on item id and says what happened.
func (w *watchView) act(id string, key rune) string {
	kind, ref, _ := strings.Cut(id, ":")
	// The next batch: s submits it from any row (enter or q on its row
	// too), x clears it.
	switch {
	case key == 's' || kind == "submit" && (key == display.PickEnter || key == 'q'):
		return w.submitStaged()
	case key == 'x':
		if len(w.staged) == 0 {
			return "no batch to clear (q on a job adds it)"
		}
		msg := fmt.Sprintf("cleared the next batch (swim %s); nothing queued", joinNums(w.staged))
		w.staged = nil
		return msg
	case kind == "submit":
		return "enter submits the batch; x clears it"
	}
	if kind == "queue" {
		if key == 'p' {
			return "p pauses a lane: pick its row (swim pause --all holds every batch)"
		}
		e, err := queue.Find(w.root, ref)
		if err != nil {
			return err.Error()
		}
		if key == display.PickEnter {
			return w.showOutput(e)
		}
		if key == 'l' {
			return w.showBatchLogs(e)
		}
		if key == 'o' {
			return w.toggleStream()
		}
		if key == 'q' {
			return fmt.Sprintf("#%d is already in the queue (r runs it now)", e.Seq)
		}
		return w.runQueued(e)
	}
	n, _ := strconv.Atoi(ref)
	if key == 'p' {
		return w.togglePause(n)
	}
	if key == display.PickEnter {
		return w.showLaneOutput(n)
	}
	if key == 'l' {
		return w.showLaneLogs(n)
	}
	if key == 'o' {
		return w.toggleStream()
	}
	info, err := lane.ReadScript(w.root, n)
	if err != nil {
		return err.Error()
	}
	if !info.Pending() {
		return fmt.Sprintf("swim %d holds no pending round (a stub, or no Round: line)", n)
	}
	if key == 'q' {
		return w.stage(n)
	}
	return w.runLane(n)
}

func (w *watchView) isStaged(n int) bool {
	for _, s := range w.staged {
		if s == n {
			return true
		}
	}
	return false
}

// stage is q on lane n: add it to the next batch, or take it out again.
// Nothing is queued until s.
func (w *watchView) stage(n int) string {
	for i, s := range w.staged {
		if s == n {
			w.staged = append(w.staged[:i:i], w.staged[i+1:]...)
			if len(w.staged) == 0 {
				return fmt.Sprintf("swim %d out of the next batch; it's empty", n)
			}
			return fmt.Sprintf("swim %d out of the next batch (swim %s; s submits)", n, joinNums(w.staged))
		}
	}
	w.staged = append(w.staged, n)
	return fmt.Sprintf("next batch: swim %s (q on another adds it; s submits as one batch; x clears)", joinNums(w.staged))
}

// submitStaged queues the staged lanes as one batch, as swim queue add N M
// does: one run, so After: between them holds. The batch stays staged if
// it can't be queued.
func (w *watchView) submitStaged() string {
	if len(w.staged) == 0 {
		return "no batch to submit: q on jobs adds them, then s"
	}
	msg := w.queueLanes(w.staged)
	if !strings.HasPrefix(msg, "queued ") {
		return "not queued: " + msg
	}
	w.staged = nil
	return msg
}

// togglePause is p on lane n: pause its running round, or let a paused one
// go on.
func (w *watchView) togglePause(n int) string {
	st, _ := status.Load(w.root)
	if _, paused := lane.LanePause(w.root, n, ""); paused {
		msg, _ := unpauseOne(w.root, st, n, operator())
		return msg
	}
	msg, err := pauseOne(w.root, st, n, operator(), "")
	if err != nil {
		return err.Error() + " (p pauses a running lane)"
	}
	return msg
}

// toggleStream hides or shows the running output under the queue.
func (w *watchView) toggleStream() string {
	w.noStream = !w.noStream
	if w.noStream {
		return "running output hidden (o shows it)"
	}
	return "running output shown"
}

// queueLanes queues lanes as one batch, as swim queue add N M does: no
// guard is approved, so their guards dry-run unless standing approvals
// cover them.
func (w *watchView) queueLanes(lanes []int) string {
	b, err := addBatch(w.root, w.cfg, lanes, queueOpts{})
	if err != nil {
		return err.Error()
	}
	ns := strings.ReplaceAll(joinNums(lanes), " ", "")
	msg := fmt.Sprintf("queued swim %s as #%d (%s)", ns, b.entry.Seq, b.entry.ID)
	if len(b.guards) > 0 {
		msg += "; its guards dry-run (approve with swim queue add " + strings.ReplaceAll(ns, ",", " ") + " --approve FLAG)"
	}
	if b.warn != "" {
		msg += "; " + b.warn
	}
	return msg
}

// runLane runs lane n now, as swim run N does: the operator is here, so its
// guards may ask.
func (w *watchView) runLane(n int) string {
	if w.batches != nil {
		// Alongside the batches running: now if the lane is free.
		st, _ := status.Load(w.root)
		if why := laneBusy(w.root, st, n); why != "" {
			return "not now: " + why
		}
		running, _ := queue.List(w.root, false)
		for _, r := range running {
			for _, l := range r.Lanes {
				if r.State == queue.Running && l.Lane == n {
					return fmt.Sprintf("not now: swim %d is in batch #%d", n, r.Seq)
				}
			}
		}
	} else {
		if why := launcher.Busy(w.root, w.cfg); why != "" {
			return "not now: " + why
		}
		unlock, ok, err := queue.LockWorker(w.root)
		if err != nil {
			return err.Error()
		}
		if !ok {
			return "not now: another swimmer is running a batch"
		}
		defer unlock()
	}
	fmt.Printf("\nswim watch: running swim %d (r)\n", n)
	err := runLanes([]string{strconv.Itoa(n)}, runFlags{})
	var ee exitError
	switch {
	case err == nil:
		return fmt.Sprintf("swim %d passed", n)
	case errors.As(err, &ee):
		return fmt.Sprintf("swim %d ended with exit %d: swim log %d", n, ee.code, n)
	default:
		return fmt.Sprintf("swim %d: %v", n, err)
	}
}

// runQueued runs a waiting batch now, out of turn, exactly as the worker
// would run it in turn: with its own approvals only.
func (w *watchView) runQueued(e *queue.Entry) string {
	if e.State != queue.Queued {
		return fmt.Sprintf("#%d is %s, not waiting", e.Seq, e.State)
	}
	if w.batches != nil {
		// Alongside the batches running: now if its lanes are free.
		st, _ := status.Load(w.root)
		for _, l := range e.Lanes {
			if why := laneBusy(w.root, st, l.Lane); why != "" {
				return "not now: " + why
			}
		}
		mine := map[int]bool{}
		for _, l := range e.Lanes {
			mine[l.Lane] = true
		}
		running, _ := queue.List(w.root, false)
		for _, r := range running {
			if r.State != queue.Running {
				continue
			}
			for _, l := range r.Lanes {
				if mine[l.Lane] {
					return fmt.Sprintf("not now: swim %d is in batch #%d", l.Lane, r.Seq)
				}
			}
		}
		bp, err := w.batches.start(w.cfg, e)
		if err != nil {
			return err.Error()
		}
		return fmt.Sprintf("started #%d (%s) out of turn; enter on it shows its output", bp.e.Seq, lanesText(bp.lanes))
	}
	if why := launcher.Busy(w.root, w.cfg); why != "" {
		return "not now: " + why
	}
	unlock, ok, err := queue.LockWorker(w.root)
	if err != nil {
		return err.Error()
	}
	if !ok {
		return "not now: another swimmer is running a batch"
	}
	defer unlock()
	if why := cantRunHere(w.root, e); why != "" {
		return why
	}
	claimed, err := queue.Claim(w.root, e, operator(), w.cfg.QueueLeaseFor())
	if err != nil {
		return err.Error()
	}
	res, _ := runClaimed(w.root, w.cfg, claimed, w.yes, true, false)
	return fmt.Sprintf("#%d %s", claimed.Seq, res.Result)
}
