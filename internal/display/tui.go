package display

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// The interactive view's model: which view is open (all lanes, or one
// lane's log), the two viewports, the lane filter and the keys typed so
// far. It draws whole frames as rows of text; interactive.go owns the
// terminal. Nothing here touches the terminal, so it is tested directly.

// Lane filters for ← → (the f key cycles them).
const (
	filterEvery = iota
	filterRunning
	filterFailed
	filterCount
)

var filterNames = []string{"every lane", "running", "failed"}

// action is what a key asks of the terminal side.
type action int

const (
	actNone action = iota
	actInterrupt
	actQuit
	actYes // an approval question answered yes
	actNo  // ... no (n, Enter, Esc)
	// On a selected lane that isn't asking: answer its guards ahead of
	// time (y), decline them ahead (n), or clear the answer (u).
	actPreYes
	actPreNo
	actPreClear
)

type tui struct {
	color    bool
	all      *viewport // combined output, prefixed [N]
	lane     *viewport // the selected lane's log
	tail     *logTail
	logPath  func(n int) string // the log file of lane n
	logLabel func(n int) string // how the divider names it
	laneN    int                // the lane view's lane; 0 is the all view
	lastSel  int                // last lane selected, for → from the all view
	filter   int
	digits   string // typed lane number, waiting for Enter
	note     string // one-off message for the divider ("no running lanes")
	help     bool
	finished bool // the run ended and the view is held open for the reader

	// Approval questions waiting for an answer, by lane, oldest first in
	// pendOrder. A lane's question shows at the bottom of its lane view,
	// where y or n answers it; elsewhere a banner says who is waiting and
	// Enter jumps there.
	pending    map[int][]string
	pendOrder  []int
	answerLane int // the lane the last actYes/actNo answers
	preLane    int // the lane the last actPre* is for

	// Last frame's size, so keys scroll by what's on screen.
	viewH, cols int
}

func newTUI(color bool, logPath, logLabel func(int) string) *tui {
	return &tui{color: color, all: newViewport(Scrollback), lane: newViewport(Scrollback), logPath: logPath, logLabel: logLabel, viewH: 10, cols: 80,
		pending: map[int][]string{}}
}

// addPending records lane n's approval question.
func (t *tui) addPending(n int, question []string) {
	if _, ok := t.pending[n]; !ok {
		t.pendOrder = append(t.pendOrder, n)
	}
	t.pending[n] = question
}

// removePending forgets lane n's question (answered or cancelled).
func (t *tui) removePending(n int) {
	delete(t.pending, n)
	for i, m := range t.pendOrder {
		if m == n {
			t.pendOrder = append(t.pendOrder[:i:i], t.pendOrder[i+1:]...)
			break
		}
	}
}

// asking reports whether the lane on screen is waiting for an answer.
func (t *tui) asking() bool {
	_, ok := t.pending[t.laneN]
	return ok && t.laneN != 0
}

// cur is the viewport on screen.
func (t *tui) cur() *viewport {
	if t.laneN != 0 {
		return t.lane
	}
	return t.all
}

// holds reports whether the view should stay open when the run ends:
// someone is reading a lane's log or has scrolled back.
func (t *tui) holds() bool { return t.laneN != 0 || !t.all.follow }

// addLine records one line of combined output: lane n's, or one of its
// jobs' (job "1.3").
func (t *tui) addLine(n int, job, text string) {
	prefix, w := "", 0
	if n > 0 {
		prefix, w = JobPrefix(n, job, t.color), len(prefixText(n, job))
	}
	t.all.add(vline{prefix: prefix, prefixW: w, text: text})
}

// order is the lanes ← and → move through: lanes in this run, running
// first, then waiting, then finished (each by number), narrowed by the
// filter.
func (t *tui) order(views []LaneView) []int {
	group := func(v LaneView) int {
		switch v.State {
		case Running:
			return 0
		case Starting, Queued, Locked, Waiting:
			return 1
		case Passed, Failed, Interrupted, Skipped:
			return 2
		}
		return -1 // idle, done: not in this run
	}
	var out []int
	for g := 0; g <= 2; g++ {
		for _, v := range views {
			if group(v) != g {
				continue
			}
			switch t.filter {
			case filterRunning:
				if v.State != Running {
					continue
				}
			case filterFailed:
				if v.State != Failed && v.State != Interrupted {
					continue
				}
			}
			out = append(out, v.N)
		}
	}
	return out
}

// selectLane opens lane n's view at the end of its current round's log.
func (t *tui) selectLane(n int) {
	if n == t.laneN {
		return
	}
	t.laneN, t.lastSel = n, n
	t.lane.clear()
	t.tail = newLogTail(t.logPath(n))
	t.pollLog()
}

// pollLog reads what the selected lane's log gained since the last poll.
func (t *tui) pollLog() bool {
	if t.tail == nil {
		return false
	}
	lines, reset := t.tail.poll()
	if reset {
		t.lane.clear()
	}
	for _, l := range lines {
		t.lane.add(vline{text: colorLogLine(l, t.color)})
	}
	return reset || len(lines) > 0
}

// key applies one key press.
func (t *tui) key(k Key, views []LaneView) action {
	t.note = ""
	if k.Code == KeyCtrlC {
		if t.finished {
			return actQuit
		}
		return actInterrupt
	}
	if t.help {
		t.help = false
		return actNone
	}
	// On a lane waiting for approval, y and n answer it; every other key
	// works as usual (← → other lanes, Esc all lanes), leaving it waiting.
	if t.asking() && k.Code == KeyRune {
		switch k.Rune {
		case 'y', 'Y':
			t.answerLane = t.laneN
			return actYes
		case 'n', 'N':
			t.answerLane = t.laneN
			return actNo
		}
	}
	if !t.asking() && t.laneN != 0 && k.Code == KeyRune {
		switch k.Rune {
		case 'y':
			t.preLane = t.laneN
			return actPreYes
		case 'n':
			t.preLane = t.laneN
			return actPreNo
		case 'u':
			t.preLane = t.laneN
			return actPreClear
		}
	}
	v, h, w := t.cur(), t.viewH, t.cols
	page := h - 1
	if page < 1 {
		page = 1
	}
	if k.Code == KeyRune && k.Rune >= '0' && k.Rune <= '9' {
		if len(t.digits) < 3 {
			t.digits += string(k.Rune)
		}
		return actNone
	}
	if t.digits != "" {
		switch k.Code {
		case KeyEnter:
			n, _ := strconv.Atoi(t.digits)
			t.digits = ""
			for _, lv := range views {
				if lv.N == n {
					t.selectLane(n)
					return actNone
				}
			}
			t.note = fmt.Sprintf("no swim %d", n)
			return actNone
		case KeyBackspace:
			t.digits = t.digits[:len(t.digits)-1]
			return actNone
		case KeyEsc:
			t.digits = ""
			return actNone
		}
		t.digits = ""
	}
	switch {
	case k.Code == KeyEnter && len(t.pendOrder) > 0:
		// Jump to the lane waiting longest for approval.
		if t.asking() {
			t.note = "y approves, n declines"
		} else {
			t.selectLane(t.pendOrder[0])
			t.lane.toEnd()
		}
	case k.Code == KeyRight || k.Code == KeyTab || k.Rune == 'l':
		t.step(views, +1)
	case k.Code == KeyLeft || k.Code == KeyBackTab || k.Rune == 'h':
		t.step(views, -1)
	case k.Code == KeyUp || k.Rune == 'k':
		v.up(1, h, w)
	case k.Code == KeyDown || k.Rune == 'j':
		v.down(1, h, w)
	case k.Code == KeyPgUp || k.Rune == 'b':
		v.up(page, h, w)
	case k.Code == KeyPgDn || k.Rune == ' ':
		v.down(page, h, w)
	case k.Code == KeyHome || k.Rune == 'g':
		v.home(h, w)
	case k.Code == KeyEnd || k.Rune == 'G':
		v.toEnd()
	case k.Rune == 'f':
		t.filter = (t.filter + 1) % filterCount
	case k.Rune == '?':
		t.help = true
	case k.Code == KeyEsc:
		if t.laneN == 0 && t.finished {
			return actQuit
		}
		t.laneN, t.tail, t.filter = 0, nil, filterEvery
		t.lane.clear()
		t.all.toEnd()
	case k.Rune == 'q':
		if t.finished {
			return actQuit
		}
		t.note = "q closes the view once the run ends; Ctrl-C interrupts it"
	}
	return actNone
}

// step moves the selection d places along the lane order, wrapping. From
// the all view, → opens the last lane selected (or the first) and ← the
// last lane.
func (t *tui) step(views []LaneView, d int) {
	order := t.order(views)
	if len(order) == 0 {
		t.note = "no " + filterNames[t.filter] + " lanes"
		return
	}
	at := -1
	for i, n := range order {
		if n == t.laneN {
			at = i
		}
	}
	var next int
	switch {
	case t.laneN == 0 && d > 0:
		next = order[0]
		for _, n := range order {
			if n == t.lastSel {
				next = n
			}
		}
	case t.laneN == 0 || at < 0 && d < 0:
		next = order[len(order)-1]
	case at < 0:
		next = order[0]
	default:
		next = order[(at+d+len(order))%len(order)]
	}
	t.selectLane(next)
}

// banner says which lanes wait for approval, on views other than theirs.
func (t *tui) banner() string {
	first := t.pendOrder[0]
	if len(t.pendOrder) == 1 {
		// The key first, so a narrow terminal cuts the task, not the hint.
		s := fmt.Sprintf("▶ swim %d needs approval · Enter to view it and answer", first)
		for _, l := range t.pending[first] {
			// What the lane says the step does (the question labels it so).
			if _, task, ok := strings.Cut(l, "the lane says: "); ok {
				s += " · " + task
				break
			}
		}
		return s
	}
	ns := make([]string, len(t.pendOrder))
	for i, n := range t.pendOrder {
		ns[i] = fmt.Sprint(n)
	}
	return fmt.Sprintf("▶ swim %s need approval · Enter to view swim %d", strings.Join(ns, ", "), first)
}

// hints is the key reminder on the title line.
func (t *tui) hints() string {
	if t.asking() {
		return "y approve  n decline  ← → lane  esc all"
	}
	if t.laneN != 0 {
		return "← → lane  y/n pre-answer  esc all  ? help"
	}
	return "← → lane  ↑ ↓ scroll  f filter  ? help"
}

// divider describes the view under the panel: which lane, following or
// paused, and the log path.
func (t *tui) divider(views []LaneView) string {
	v := t.cur()
	var parts []string
	if t.laneN != 0 {
		parts = append(parts, fmt.Sprintf("swim %d", t.laneN))
	} else {
		parts = append(parts, "all lanes")
	}
	// What the operator must know first, before the rest: the line is cut
	// at the screen's width.
	if t.finished {
		parts = append(parts, "run finished · q to close")
	}
	if t.note != "" {
		parts = append(parts, t.note)
	}
	if t.laneN != 0 {
		for _, lv := range views {
			if lv.N != t.laneN || len(lv.KnownGuards) == 0 {
				continue
			}
			g := "guards: " + strings.Join(lv.KnownGuards, ", ")
			switch lv.Pre {
			case "approved":
				g += " (pre-approved · u undo)"
			case "declined":
				g += " (pre-declined · u undo)"
			default:
				if lv.Active() || lv.State == Starting {
					g += " (y pre-approve · n pre-decline)"
				}
			}
			parts = append(parts, g)
		}
	}
	switch {
	case t.finished:
	case v.follow:
		parts = append(parts, "following")
	case v.newLines > 0:
		parts = append(parts, fmt.Sprintf("paused · %d new lines · End to follow", v.newLines))
	default:
		parts = append(parts, "paused · End to follow")
	}
	if t.laneN != 0 {
		for _, lv := range views {
			if lv.N == t.laneN && !lv.ranThisRun() {
				parts = append(parts, "last round")
			}
		}
		parts = append(parts, t.logLabel(t.laneN))
	}
	if t.filter != filterEvery {
		parts = append(parts, "lanes: "+filterNames[t.filter])
	}
	if t.digits != "" {
		parts = append(parts, "go to swim "+t.digits+"_ (Enter)")
	}
	return strings.Join(parts, " · ")
}

// ranThisRun reports whether the lane started in this run (so its log's
// last round is this run's).
func (v LaneView) ranThisRun() bool {
	return v.State == Running || v.State == Passed || v.State == Failed || v.State == Interrupted
}

// render draws a whole frame: rows lines for a cols-wide screen.
func (t *tui) render(title string, views []LaneView, now time.Time, rows, cols int) []string {
	p := ui.Painter{On: t.color}
	w := cols - 1 // never write the last column, so the terminal never wraps
	if w < 20 {
		w = 20 // as the panel; a terminal this narrow clips the rest
	}
	panel := renderPanel(title, views, w, now, t.color, t.laneN)
	hints := t.hints()
	if room := w - textWidth(hints) - 2; room >= 20 {
		head := truncate(title, room)
		panel[0] = p.Paint(ui.Bold, head) + strings.Repeat(" ", w-len([]rune(head))-textWidth(hints)) + p.Paint(ui.Dim, hints)
	}
	div := " " + truncate(t.divider(views), w-6) + " ──"
	panel[len(panel)-1] = p.Paint(ui.Dim, strings.Repeat("─", w-textWidth(div))+div)

	h := rows - len(panel)
	if h < 1 {
		h = 1
	}
	t.viewH, t.cols = h, w
	body := t.cur().render(h, w)
	if t.laneN != 0 && len(t.lane.lines) == 0 {
		body[0] = p.Paint(ui.Dim, fmt.Sprintf("(%s has nothing yet)", t.logLabel(t.laneN)))
	}
	if t.help {
		body = overlay(body, helpText, w, p)
	}
	switch {
	case t.asking():
		q := append(append([]string(nil), t.pending[t.laneN]...), "", "y approve   n decline   ← → other lanes   esc all lanes")
		body = bottomBox(body, q, w, p)
	case len(t.pendOrder) > 0 && len(body) > 0:
		body[len(body)-1] = p.Paint(ui.Yellow+ui.Bold, truncate(t.banner(), w))
	}
	out := append(panel, body...)
	if len(out) > rows { // resized smaller than the panel
		out = out[:rows]
	}
	return out
}

var helpText = []string{
	"keys",
	"",
	"→ l Tab        next lane (from all lanes: last selected)",
	"← h Shift-Tab  previous lane",
	"↑ k  ↓ j       scroll one line",
	"PgUp b  PgDn   scroll a page",
	"Home g  End G  top; bottom and follow",
	"4 2 Enter      jump to swim 42",
	"Enter          open the lane waiting longest for approval",
	"y / n          on a lane waiting for approval: approve / decline",
	"y n u          before it asks: pre-approve, pre-decline, undo",
	"f              lanes ← → visit: every / running / failed",
	"Esc            back to all lanes, following",
	"Ctrl-C         interrupt the run (as without the view)",
	"q              close the view once the run has finished",
	"",
	"any key closes this help",
}

// bottomBox draws a box holding text over the bottom of body, so the log
// above it stays readable.
func bottomBox(body, text []string, w int, p ui.Painter) []string {
	out := overlay(body, text, w, p)
	if len(body) < len(text)+2 {
		return out
	}
	// overlay centres the box; move it to the bottom rows instead.
	out = append([]string(nil), body...)
	box := overlay(make([]string, len(text)+2), text, w, p)
	copy(out[len(out)-len(box):], box)
	return out
}

// overlay draws a box holding text over the middle of body.
func overlay(body, text []string, w int, p ui.Painter) []string {
	boxW := 0
	for _, l := range text {
		if n := len([]rune(l)); n > boxW {
			boxW = n
		}
	}
	boxW += 4
	if boxW > w {
		boxW = w
	}
	left := (w - boxW) / 2
	top := (len(body) - len(text) - 2) / 2
	if top < 0 {
		top = 0
	}
	out := append([]string(nil), body...)
	put := func(i int, s string) {
		if i >= 0 && i < len(out) {
			out[i] = strings.Repeat(" ", left) + s
		}
	}
	put(top, p.Paint(ui.Bold, "┌"+strings.Repeat("─", boxW-2)+"┐"))
	for i, l := range text {
		put(top+1+i, p.Paint(ui.Bold, "│ ")+pad(truncate(l, boxW-4), boxW-4)+p.Paint(ui.Bold, " │"))
	}
	put(top+1+len(text), p.Paint(ui.Bold, "└"+strings.Repeat("─", boxW-2)+"┘"))
	return out
}

var logKindRE = regexp.MustCompile(`^  (PASS|FAIL|BLOCKED|SKIP|DRIFT|APPROVED|STOP|WARN)  `)

// colorLogLine colours a log line the way swim log does, lightly: round and
// END banners, stage headers and result kinds. The text is unchanged.
func colorLogLine(l string, color bool) string {
	if !color {
		return l
	}
	p := ui.Painter{On: true}
	switch {
	case strings.HasPrefix(l, logparse.RoundMark):
		return p.Paint(ui.Bold+ui.Cyan, l)
	case strings.HasPrefix(l, logparse.EndMark):
		state := ""
		if f := strings.Fields(l); len(f) >= 3 {
			state = f[2]
		}
		return p.Paint(ui.Bold+ui.StateColor(state), l)
	case strings.HasPrefix(l, logparse.StageMark):
		return p.Paint(ui.Bold+ui.Blue, l)
	case strings.HasPrefix(l, logparse.OutPrefix):
		return l
	case strings.HasPrefix(l, logparse.Indent), strings.HasPrefix(l, "   "), strings.HasPrefix(l, "# swim lane log"):
		return p.Paint(ui.Dim, l)
	}
	if m := logKindRE.FindStringSubmatch(l); m != nil {
		return "  " + p.Paint(ui.StateColor(m[1])+ui.Bold, m[1]) + l[2+len(m[1]):]
	}
	return l
}
