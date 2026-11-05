// Package display draws the launcher's live view: a panel pinned to the top
// of the terminal with one row per lane (lane 1..N), and lane output
// scrolling underneath with [N] prefixes.
package display

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// Lane states shown in the panel: status's words (status.States).
const (
	Ready       = status.Ready
	Empty       = status.Empty
	Waiting     = status.Waiting // on the lanes it runs after (WaitingOn)
	Running     = status.Running
	Passed      = status.Passed
	Failed      = status.Failed
	Skipped     = status.Skipped
	Interrupted = status.Interrupted
)

// The panel's finer cuts of those states: what it knows besides the word,
// shown with the word.
const (
	Starting  = "running:starting" // running: selected, about to start
	SlotWait  = "waiting:slot"     // waiting for a free slot (max_parallel); QueuePos
	LockWait  = "waiting:lock"     // waiting for a resource lock (# Locks:); LockText
	Earlier   = "passed:earlier"   // passed in an earlier run; not run again
	Elsewhere = "running:elsewhere"
	// Elsewhere is a lane this run doesn't include that another swim (a
	// concurrent swim run, swim watch) is running now: shown live, never
	// part of this run.
)

// Word is the state word (status.States) a panel state is shown with.
func Word(state string) string {
	w, _, _ := strings.Cut(state, ":")
	return w
}

// LaneView is what the panel knows about one lane.
type LaneView struct {
	N         int
	State     string
	Round     string // round being run, or the round a ready lane holds
	Job       string // that round's id
	WaitingOn []int
	Reason    string // why a lane was skipped
	Exit      int
	QueuePos  int    // lanes ahead of it while SlotWait
	LockText  string // while LockWait, e.g. "orders-table: lane 4"
	Stopping  string // while Running after a stop request: the step it is finishing
	Approval  string // while Running and waiting for the operator to approve a guarded step
	// Paused is how long the round has waited for the operator; PausedAt,
	// when the wait under way began (zero when none). Its clock stops then.
	Paused   time.Duration
	PausedAt time.Time
	// Pre is the operator's answer, given ahead of time in the live view,
	// to the lane's guards: "approved", "declined" or "". KnownGuards are
	// the guards swim knows the lane has; UnknownGuards counts those named
	// at run time (guard "$FLAG"), which can't be answered ahead.
	Pre           string
	KnownGuards   []string
	UnknownGuards int
	Started       time.Time
	Finished      time.Time
	Summarized    bool // the round ended itself and printed its summary: plain output adds no result line
	Dry           bool // a guard of this round ran as a dry run: [DRY]
}

// Ran is how long the round has run by now (or ran, once finished): from
// Started to Finished, less its waits for the operator.
func (v LaneView) Ran(now time.Time) time.Duration {
	end := now
	if !v.Finished.IsZero() {
		end = v.Finished
	}
	d := end.Sub(v.Started) - v.Paused
	if !v.PausedAt.IsZero() && end.After(v.PausedAt) {
		d -= end.Sub(v.PausedAt)
	}
	return max(d, 0)
}

// Pause stops the round's clock at now, or (pause false) starts it again.
func (v *LaneView) Pause(pause bool, now time.Time) {
	switch {
	case pause && v.PausedAt.IsZero():
		v.PausedAt = now
	case !pause && !v.PausedAt.IsZero():
		v.Paused += now.Sub(v.PausedAt)
		v.PausedAt = time.Time{}
	}
}

// Active reports whether the lane is still queued, waiting or running.
func (v LaneView) Active() bool {
	return v.State == Starting || v.State == SlotWait || v.State == LockWait || v.State == Waiting || v.State == Running
}

var throbberFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// ThrobberStep is how long each throbber frame shows (a turn in 800ms): a
// screen with a throbber redraws this often, so no frame is skipped.
const ThrobberStep = 80 * time.Millisecond

// Throbber returns the spinner frame for now.
func Throbber(now time.Time) string {
	return throbberFrames[(now.UnixMilli()/ThrobberStep.Milliseconds())%int64(len(throbberFrames))]
}

// Duration is how long something took, the one way swim shows a
// duration to a person: "0.4s" under ten seconds, then "12s", "1m 5s",
// "1h 4m". logparse.ParseDuration reads it back.
func Duration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if t := d.Round(100 * time.Millisecond); t < 10*time.Second {
		return fmt.Sprintf("%.1fs", t.Seconds())
	}
	s := int(d.Round(time.Second).Seconds())
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm %ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh %dm", s/3600, s/60%60)
}

// Seconds is Duration for a count of seconds (a status file's duration_s).
func Seconds(s float64) string { return Duration(time.Duration(s * float64(time.Second))) }

// Ago is how long ago something ended, in one unit: "12s ago", "5m ago",
// "3h ago", "2d ago".
func Ago(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// Span is a job's start and stop clock times (local) and its duration:
// "14:02:10 → 14:03:39 (1m 29s)", with dates when it crossed midnight.
// Each clock time is the second it falls in.
func Span(start, end time.Time) string {
	took := Duration(end.Sub(start))
	start, end = start.Local().Truncate(time.Second), end.Local().Truncate(time.Second)
	layout := "15:04:05"
	if start.YearDay() != end.YearDay() || start.Year() != end.Year() {
		layout = "Jan 2 15:04:05"
	}
	return fmt.Sprintf("%s → %s (%s)", start.Format(layout), end.Format(layout), took)
}

// WaitingText renders "waiting on lane 1" / "waiting on lane 1, 3".
func WaitingText(deps []int) string {
	if len(deps) == 0 {
		return "waiting"
	}
	parts := make([]string, len(deps))
	for i, d := range deps {
		parts[i] = fmt.Sprint(d)
	}
	return "waiting on lane " + strings.Join(parts, ", ")
}

// The other reasons a lane waits, as every view says them.

// WaitingLock is "waiting for lock orders-table: lane 4".
func WaitingLock(lock string) string { return "waiting for lock " + lock }

// WaitingSlot is "waiting for a slot (#2)": max_parallel lanes run.
func WaitingSlot(pos int) string { return fmt.Sprintf("waiting for a slot (#%d)", pos) }

// WaitingApproval is "waiting for approval: <what>".
func WaitingApproval(what string) string { return "waiting for approval: " + what }

// stateText returns the plain state column text and the colour for it,
// marked [DRY] when a guard of the round ran as a dry run.
func stateText(v LaneView, now time.Time) (string, string) {
	text, color := laneStateText(v, now)
	if v.Dry {
		text = DryMark + " " + strings.TrimLeft(text, " ")
	}
	return text, color
}

// DryMark marks a lane, job or batch whose guards run (or ran) as dry runs.
const DryMark = "[DRY]"

func laneStateText(v LaneView, now time.Time) (string, string) {
	thr := Throbber(now)
	switch v.State {
	case Starting:
		return thr + " running", ui.Cyan
	case SlotWait:
		return thr + " " + WaitingSlot(v.QueuePos+1), ui.Yellow
	case LockWait:
		return thr + " " + WaitingLock(v.LockText), ui.Yellow
	case Waiting:
		return thr + " " + WaitingText(v.WaitingOn), ui.Yellow
	case Running:
		if v.Approval != "" {
			return thr + " " + WaitingApproval(v.Approval), ui.Yellow
		}
		if v.Stopping != "" {
			return fmt.Sprintf("%s running, stopping %s (%s)", thr, v.Stopping, Duration(v.Ran(now))), ui.Yellow
		}
		return thr + " running " + Duration(v.Ran(now)), ui.Cyan
	case Elsewhere:
		return thr + " running elsewhere " + Duration(v.Ran(now)), ui.Blue
	case Passed:
		return "passed " + Duration(v.Ran(now)), ui.Green
	case Failed:
		return fmt.Sprintf("failed exit %d %s", v.Exit, Duration(v.Ran(now))), ui.Red
	case Interrupted:
		return "interrupted " + Duration(v.Ran(now)), ui.Red
	case Skipped:
		if v.Reason != "" {
			return "skipped (" + v.Reason + ")", ui.Yellow
		}
		return "skipped", ui.Yellow
	case Earlier:
		return "  passed (an earlier run)", ui.Green
	case Ready:
		return "  ready", ui.Cyan
	}
	return "  empty", ui.Dim
}

// truncate shortens s to at most w runes, marking the cut with "…".
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= w {
		return s
	}
	r := []rune(s)
	if w == 1 {
		return "…"
	}
	return string(r[:w-1]) + "…"
}

func pad(s string, w int) string {
	if n := utf8.RuneCountInString(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

const stateWidth = 26

// MaxRows is the most lane rows the pinned panel shows. With more lanes,
// it shows MaxRows-1 of them plus a line counting the hidden ones.
const MaxRows = 15

// rank orders states by how much they deserve a panel row: lower stays
// visible longer. Running lanes are hidden last, failures before passes.
func rank(state string) int {
	switch state {
	case Running:
		return 0
	case Failed, Interrupted:
		return 1
	case Starting, SlotWait, LockWait, Waiting, Elsewhere:
		return 2
	case Skipped:
		return 3
	case Passed:
		return 4
	case Earlier:
		return 5
	case Ready:
		return 6
	}
	return 7 // empty
}

// hiddenWord is how a hidden state is counted in the "more" line.
func hiddenWord(state string) string {
	switch state {
	case Elsewhere:
		return "running elsewhere"
	case Earlier:
		return "passed earlier"
	}
	return Word(state)
}

// PanelRows returns how many lane rows (including the "more" line) the
// panel uses for n lanes.
func PanelRows(n int) int {
	if n > MaxRows {
		return MaxRows
	}
	return n
}

// visible picks the lanes to show when there are more than MaxRows: the
// MaxRows-1 best ranked (ties keep lane order), returned in lane order,
// plus a summary of the rest. Lane sel (if not 0) always gets a row.
func visible(views []LaneView, sel int) ([]LaneView, string) {
	if len(views) <= MaxRows {
		return views, ""
	}
	idx := make([]int, len(views))
	for i := range idx {
		idx[i] = i
	}
	key := func(i int) int {
		if views[i].N == sel {
			return -1
		}
		return rank(views[i].State)
	}
	sort.SliceStable(idx, func(a, b int) bool { return key(idx[a]) < key(idx[b]) })
	keep := map[int]bool{}
	for _, i := range idx[:MaxRows-1] {
		keep[i] = true
	}
	var shown []LaneView
	counts := map[string]int{}
	var order []string
	for i, v := range views {
		if keep[i] {
			shown = append(shown, v)
			continue
		}
		w := hiddenWord(v.State)
		if counts[w] == 0 {
			order = append(order, w)
		}
		counts[w]++
	}
	sort.SliceStable(order, func(a, b int) bool { return wordRank(order[a]) < wordRank(order[b]) })
	parts := make([]string, len(order))
	for i, w := range order {
		parts[i] = fmt.Sprintf("%d %s", counts[w], w)
	}
	return shown, fmt.Sprintf(" … %d more: %s", len(views)-len(shown), strings.Join(parts, ", "))
}

func wordRank(w string) int {
	for _, s := range []string{Running, Failed, Interrupted, Waiting, Elsewhere, Skipped, Passed, Earlier, Ready, Empty} {
		if hiddenWord(s) == w {
			return rank(s)
		}
	}
	return 9
}

// RenderPanel returns the pinned panel: a title line, one row per lane and
// a separator. Every line fits in width columns (ANSI codes excluded).
func RenderPanel(title string, views []LaneView, width int, now time.Time, color bool) []string {
	return renderPanel(title, views, width, now, color, 0)
}

// renderPanel is RenderPanel with lane sel (if not 0) marked ▶ and always
// shown, for the interactive view.
func renderPanel(title string, views []LaneView, width int, now time.Time, color bool, sel int) []string {
	p := ui.Painter{On: color}
	if width < 20 {
		width = 20
	}
	lines := []string{p.Paint(ui.Bold, truncate(title, width))}

	maxN := 1
	for _, v := range views {
		if v.N > maxN {
			maxN = v.N
		}
	}
	labelW := len(fmt.Sprintf(" lane %d  ", maxN))
	shown, more := visible(views, sel)
	for _, v := range shown {
		label := pad(fmt.Sprintf(" lane %d", v.N), labelW)
		if v.N == sel {
			label = "▶" + label[1:]
		}
		st, stColor := stateText(v, now)
		st = ui.Safe(st) // a skip reason or approval text can be a lane's
		stW := stateWidth
		if labelW+stW > width {
			stW = width - labelW
		}
		st = pad(truncate(st, stW), stW)
		round := ui.Safe(v.Round)
		if round == "" {
			round = "-"
		}
		job := ""
		if v.Job != "" {
			job = " " + lane.ShortJob(v.Job)
		}
		avail := width - labelW - stW - 1
		if utf8.RuneCountInString(round)+len(job) > avail {
			job = "" // the round matters more than the id on a narrow screen
		}
		round = truncate(round, avail)

		roundColor := ""
		if v.State == Ready || v.State == Empty || v.State == Earlier {
			roundColor = ui.Dim
		}
		line := p.Paint(ui.LaneColor(v.N)+ui.Bold, label) + p.Paint(stColor, st)
		if round != "" {
			line += " " + p.Paint(roundColor, round)
		}
		if job != "" {
			line += p.Paint(ui.Dim, job)
		}
		if mark, color := preMark(v); mark != "" {
			line += "  " + p.Paint(color, mark)
		}
		lines = append(lines, line)
	}
	if more != "" {
		lines = append(lines, p.Paint(ui.Dim, truncate(more, width)))
	}
	lines = append(lines, p.Paint(ui.Dim, strings.Repeat("─", width)))
	return lines
}

// Prefix renders the [N] prefix for a lane's output line.
func Prefix(n int, color bool) string {
	return JobPrefix(n, "", color)
}

// JobPrefix is a line's prefix for job <job> of lane n ("[1.3] ", in the
// lane's colour), or for the lane itself when job is "" ("[1] ").
func JobPrefix(n int, job string, color bool) string {
	if job == "" {
		job = fmt.Sprint(n)
	}
	p := ui.Painter{On: color}
	return p.Paint(ui.LaneColor(n), "[") + p.Job(job, "") + p.Paint(ui.LaneColor(n), "]") + " "
}

// WrapANSI splits s into lines of at most w visible columns. SGR (colour)
// sequences take no columns and are never split; the colour active at a
// break is re-applied at the start of the next line. Tabs expand to 8-column
// stops. Swim wraps lines itself so the terminal never soft-wraps inside the
// scroll region (some terminals then join rows when copying or resizing).
// The text is a lane's, so it goes through ui.SafeKeepSGR first: no other
// escape sequence, control character (\r, \b, C1) or bidi control reaches
// the screen to move the cursor or rewrite what is shown.
func WrapANSI(s string, w int) []string {
	if w < 1 {
		w = 1
	}
	s = ui.SafeKeepSGR(s)
	var lines []string
	var cur strings.Builder
	active := "" // SGR sequences in effect since the last reset
	col := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := ui.EscEnd(s, i)
			seq := s[i:j]
			cur.WriteString(seq)
			if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
				if seq == "\x1b[0m" || seq == "\x1b[m" {
					active = ""
				} else {
					active += seq
				}
			}
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		text := string(r)
		width := 1
		if r == '\t' {
			width = 8 - col%8
			text = strings.Repeat(" ", width)
		}
		if col+width > w {
			lines = append(lines, cur.String())
			cur.Reset()
			cur.WriteString(active)
			col = 0
			if r == '\t' {
				width = 8
				text = strings.Repeat(" ", width)
				if width > w {
					width, text = w, strings.Repeat(" ", w)
				}
			}
		}
		cur.WriteString(text)
		col += width
	}
	return append(lines, cur.String())
}

// preMark is a lane's pre-approval mark in the panel: ✓ pre-approved,
// ✗ dry run, or ? N guards while it would still ask.
func preMark(v LaneView) (string, string) {
	switch {
	case v.Pre == "approved":
		return "✓ pre-approved", ui.Green
	case v.Pre == "declined":
		return "✗ dry run", ui.Yellow
	case !(v.Active() || v.State == Starting):
		return "", ""
	case v.UnknownGuards > 0 && len(v.KnownGuards) == 0:
		return fmt.Sprintf("? %d guard not known in advance", v.UnknownGuards), ui.Dim
	case len(v.KnownGuards) == 1:
		return "? 1 guard", ui.Dim
	case len(v.KnownGuards) > 1:
		return fmt.Sprintf("? %d guards", len(v.KnownGuards)), ui.Dim
	}
	return "", ""
}
