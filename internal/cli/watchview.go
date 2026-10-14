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
// queue it. A batch the worker takes from the queue still runs on its own,
// as without the screen.
type watchView struct {
	root     string
	cfg      *config.Config
	picker   *display.Picker
	yes      bool   // swim watch -y: approves what a batch doesn't
	activity string // what the worker is waiting for
}

const (
	watchKeys  = "rq"
	watchHints = "↑ ↓ pick  r run now  q queue  ctrl-c quit"
)

func newWatchView(root string, cfg *config.Config, yes bool) *watchView {
	return &watchView{root: root, cfg: cfg, yes: yes, picker: &display.Picker{In: os.Stdin, Out: os.Stdout, Color: ui.ColorEnabled(os.Stdout)}}
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
	if link, color := hubLink(); link != "" {
		rows = append(rows, display.PickRow{Text: link, Color: color})
	}
	if w.activity != "" {
		rows = append(rows, display.PickRow{Text: w.activity, Color: ui.Dim}, display.PickRow{Text: " ", Color: ui.Dim})
	}
	rows = append(rows, display.PickRow{Text: "Jobs"})
	st, _ := status.Load(w.root)
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
		rows = append(rows, display.PickRow{ID: "lane:" + strconv.Itoa(n), Text: fmt.Sprintf("swim %-2d  %-11s %s  %s", n, state, job, round), Color: color})
	}
	rows = append(rows, display.PickRow{Text: " ", Color: ui.Dim}, display.PickRow{Text: "Queue"})
	entries, err := queue.List(w.root, false)
	if err != nil {
		rows = append(rows, display.PickRow{Text: "  " + err.Error(), Color: ui.Red})
	}
	if len(entries) == 0 && err == nil {
		rows = append(rows, display.PickRow{Text: "  (empty: q on a job queues it)", Color: ui.Dim})
	}
	for _, e := range entries {
		var ns []string
		for _, l := range e.Lanes {
			ns = append(ns, strconv.Itoa(l.Lane))
		}
		text := fmt.Sprintf("#%-4d %-9s swim %s  by %s", e.Seq, e.State, strings.Join(ns, ","), e.By)
		if e.Note != "" {
			text += "  " + e.Note
		}
		color := ""
		switch e.State {
		case queue.Running:
			color = ui.Cyan
		case queue.Orphaned:
			color = ui.Red
		}
		rows = append(rows, display.PickRow{ID: "queue:" + e.ID, Text: text, Color: color})
	}
	return rows
}

// laneRowState is how a lane's row shows its state: a script holding a job
// that hasn't run yet is pending, whatever its last round did.
func laneRowState(l *status.Lane, info lane.Info) (string, string) {
	if l == nil {
		if info.Pending() {
			return "pending", ""
		}
		return "idle", ui.Dim
	}
	state := l.State
	active := state == status.Running || state == status.Waiting || state == status.Queued || state == status.Locked
	if active && (l.PID <= 0 || !lane.Alive(l.PID)) {
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
	if kind == "queue" {
		e, err := queue.Find(w.root, ref)
		if err != nil {
			return err.Error()
		}
		if key == 'q' {
			return fmt.Sprintf("#%d is already in the queue (r runs it now)", e.Seq)
		}
		return w.runQueued(e)
	}
	n, _ := strconv.Atoi(ref)
	info, err := lane.ReadScript(w.root, n)
	if err != nil {
		return err.Error()
	}
	if !info.Pending() {
		return fmt.Sprintf("swim %d holds no pending round (a stub, or no Round: line)", n)
	}
	if key == 'q' {
		return w.queueLane(n)
	}
	return w.runLane(n)
}

// queueLane queues lane n as its own batch, as swim queue add N does: no
// guard is approved, so its guards dry-run unless standing approvals cover
// them.
func (w *watchView) queueLane(n int) string {
	b, err := addBatch(w.root, w.cfg, []int{n}, queueOpts{})
	if err != nil {
		return err.Error()
	}
	msg := fmt.Sprintf("queued swim %d as #%d (%s)", n, b.entry.Seq, b.entry.ID)
	if len(b.guards) > 0 {
		msg += "; its guards dry-run (approve with swim queue add " + strconv.Itoa(n) + " --approve FLAG)"
	}
	if b.warn != "" {
		msg += "; " + b.warn
	}
	return msg
}

// runLane runs lane n now, as swim run N does: the operator is here, so its
// guards may ask.
func (w *watchView) runLane(n int) string {
	if why := launcher.Busy(w.root, w.cfg); why != "" {
		return "not now: " + why
	}
	unlock, ok, err := queue.LockWorker(w.root)
	if err != nil {
		return err.Error()
	}
	if !ok {
		return "not now: another worker is running a batch"
	}
	defer unlock()
	fmt.Printf("\nswim watch: running swim %d (r)\n", n)
	err = runLanes([]string{strconv.Itoa(n)}, runFlags{})
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
	if why := launcher.Busy(w.root, w.cfg); why != "" {
		return "not now: " + why
	}
	unlock, ok, err := queue.LockWorker(w.root)
	if err != nil {
		return err.Error()
	}
	if !ok {
		return "not now: another worker is running a batch"
	}
	defer unlock()
	claimed, err := queue.Claim(w.root, e, operator())
	if err != nil {
		return err.Error()
	}
	res, _ := runClaimed(w.root, w.cfg, claimed, w.yes, true)
	return fmt.Sprintf("#%d %s", claimed.Seq, res.Result)
}
