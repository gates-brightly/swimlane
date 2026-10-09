// Package display draws the launcher's live view: a panel pinned to the top
// of the terminal with one row per lane (swim 1..N), and lane output
// scrolling underneath with [N] prefixes.
package display

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// Lane states shown in the panel (same words as status.yml).
const (
	Idle        = "idle"
	Starting    = "starting" // selected, about to start
	Queued      = "queued"   // dependencies passed; waiting for a free slot (max_parallel)
	Waiting     = "waiting"
	Running     = "running"
	Passed      = "passed"
	Failed      = "failed"
	Skipped     = "skipped"
	Interrupted = "interrupted"
	Done        = "done" // passed in an earlier run; not run again
)

// LaneView is what the panel knows about one lane.
type LaneView struct {
	N         int
	State     string
	Round     string // round being run, or the pending round for idle lanes
	Job       string // job id of that round
	WaitingOn []int
	Reason    string // why a lane was skipped
	Exit      int
	QueuePos  int // lanes ahead of it while Queued
	Started   time.Time
	Finished  time.Time
}

// Active reports whether the lane is still queued, waiting or running.
func (v LaneView) Active() bool {
	return v.State == Starting || v.State == Queued || v.State == Waiting || v.State == Running
}

var throbberFrames = []string{"|", "/", "-", `\`}

// Throbber returns the spinner frame for now (one frame per 100ms).
func Throbber(now time.Time) string {
	return throbberFrames[(now.UnixMilli()/100)%int64(len(throbberFrames))]
}

// Elapsed formats a duration as m:ss or h:mm:ss.
func Elapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d.Round(time.Second).Seconds())
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// WaitingText renders "waiting on swim 1" / "waiting on swim 1, 3".
func WaitingText(deps []int) string {
	parts := make([]string, len(deps))
	for i, d := range deps {
		parts[i] = fmt.Sprint(d)
	}
	return "waiting on swim " + strings.Join(parts, ", ")
}

// stateText returns the plain state column text and the colour for it.
func stateText(v LaneView, now time.Time) (string, string) {
	thr := Throbber(now)
	switch v.State {
	case Starting:
		return thr + " starting", ui.Cyan
	case Queued:
		return fmt.Sprintf("%s queued (#%d)", thr, v.QueuePos+1), ui.Yellow
	case Waiting:
		return thr + " " + WaitingText(v.WaitingOn), ui.Yellow
	case Running:
		return thr + " running " + Elapsed(now.Sub(v.Started)), ui.Cyan
	case Passed:
		return "PASS " + Elapsed(v.Finished.Sub(v.Started)), ui.Green
	case Failed:
		return fmt.Sprintf("FAIL exit %d %s", v.Exit, Elapsed(v.Finished.Sub(v.Started))), ui.Red
	case Interrupted:
		return "INTERRUPTED " + Elapsed(v.Finished.Sub(v.Started)), ui.Red
	case Skipped:
		if v.Reason != "" {
			return "SKIP (" + v.Reason + ")", ui.Yellow
		}
		return "SKIP", ui.Yellow
	}
	if v.State == Done {
		return "  done (passed earlier)", ui.Green
	}
	return "  idle", ui.Dim
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
	case Starting, Queued, Waiting:
		return 2
	case Skipped:
		return 3
	case Passed:
		return 4
	case Done:
		return 5
	}
	return 6 // idle
}

// hiddenWord is how a hidden state is counted in the "more" line.
func hiddenWord(state string) string {
	switch state {
	case Starting, Waiting:
		return "waiting"
	case Queued:
		return "queued"
	case Failed:
		return "failed"
	case Done:
		return "done earlier"
	}
	return state
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
// plus a summary of the rest.
func visible(views []LaneView) ([]LaneView, string) {
	if len(views) <= MaxRows {
		return views, ""
	}
	idx := make([]int, len(views))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return rank(views[idx[a]].State) < rank(views[idx[b]].State) })
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
	for _, s := range []string{Running, Failed, Interrupted, Queued, Waiting, Skipped, Passed, Done, Idle} {
		if hiddenWord(s) == w {
			return rank(s)
		}
	}
	return 9
}

// RenderPanel returns the pinned panel: a title line, one row per lane and
// a separator. Every line fits in width columns (ANSI codes excluded).
func RenderPanel(title string, views []LaneView, width int, now time.Time, color bool) []string {
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
	labelW := len(fmt.Sprintf(" swim %d  ", maxN))
	shown, more := visible(views)
	for _, v := range shown {
		label := pad(fmt.Sprintf(" swim %d", v.N), labelW)
		st, stColor := stateText(v, now)
		stW := stateWidth
		if labelW+stW > width {
			stW = width - labelW
		}
		st = pad(truncate(st, stW), stW)
		round := v.Round
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
		if v.State == Idle || v.State == Done {
			roundColor = ui.Dim
		}
		line := p.Paint(ui.LaneColor(v.N)+ui.Bold, label) + p.Paint(stColor, st)
		if round != "" {
			line += " " + p.Paint(roundColor, round)
		}
		if job != "" {
			line += p.Paint(ui.Dim, job)
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
	return ui.Painter{On: color}.Paint(ui.LaneColor(n), fmt.Sprintf("[%d]", n)) + " "
}

// WrapANSI splits s into lines of at most w visible columns. Escape
// sequences take no columns and are never split; the colour active at a
// break is re-applied at the start of the next line. Tabs expand to 8-column
// stops. Swim wraps lines itself so the terminal never soft-wraps inside the
// scroll region (some terminals then join rows when copying or resizing).
func WrapANSI(s string, w int) []string {
	if w < 1 {
		w = 1
	}
	var lines []string
	var cur strings.Builder
	active := "" // SGR sequences in effect since the last reset
	col := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := escEnd(s, i)
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

// escEnd returns the index just past the escape sequence starting at i.
func escEnd(s string, i int) int {
	if i+1 >= len(s) {
		return len(s)
	}
	switch s[i+1] {
	case '[': // CSI: parameters, then a final byte 0x40-0x7e
		for j := i + 2; j < len(s); j++ {
			if s[j] >= 0x40 && s[j] <= 0x7e {
				return j + 1
			}
		}
		return len(s)
	case ']': // OSC: ends with BEL or ESC \
		for j := i + 2; j < len(s); j++ {
			if s[j] == 0x07 {
				return j + 1
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
		}
		return len(s)
	}
	j := i + 1
	for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f { // intermediates, as in ESC ( B
		j++
	}
	if j < len(s) {
		j++
	}
	return j
}
