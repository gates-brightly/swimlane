package timeline

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// Bar characters.
const (
	cellRun   = "█"
	cellDeps  = "░"
	cellQueue = "▒"
	cellLock  = "▓"
)

// Options shape the text view.
type Options struct {
	Width int  // terminal width (0: 100)
	Color bool // ANSI colour
	Top   int  // show the N slowest lanes plus the longest chain (0: auto)
	All   bool // every lane
	Steps bool // a row per step (or stage) under each lane
}

// autoTop is how many lanes a long run shows without --all or --top.
const autoTop = 20

// Shown picks the lanes the text view draws, in lane order, and how many it
// left out.
func Shown(t *Timeline, o Options) ([]*Lane, int) {
	top := o.Top
	if top == 0 && !o.All && len(t.Lanes) > autoTop+10 {
		top = autoTop
	}
	var out []*Lane
	if o.All || top <= 0 || len(t.Lanes) <= top {
		for i := range t.Lanes {
			out = append(out, &t.Lanes[i])
		}
		return out, 0
	}
	idx := make([]int, len(t.Lanes))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return t.Lanes[idx[a]].Duration > t.Lanes[idx[b]].Duration })
	keep := map[int]bool{}
	for _, i := range idx[:top] {
		keep[i] = true
	}
	for i := range t.Lanes {
		if t.Lanes[i].OnChain || keep[i] {
			out = append(out, &t.Lanes[i])
		}
	}
	return out, len(t.Lanes) - len(out)
}

// scale maps seconds to bar cells.
type scale struct {
	cells int
	span  float64
}

func (s scale) at(sec float64) int {
	if s.span <= 0 {
		return 0
	}
	c := int(sec / s.span * float64(s.cells))
	if c > s.cells {
		c = s.cells
	}
	if c < 0 {
		c = 0
	}
	return c
}

// bar draws one lane: waits before the run, then the run itself.
func (s scale) bar(l *Lane) string {
	cells := make([]string, s.cells)
	for i := range cells {
		cells[i] = " "
	}
	fill := func(from, to float64, ch string, min1 bool) {
		a, b := s.at(from), s.at(to)
		if min1 && b <= a {
			b = a + 1
		}
		for i := a; i < b && i < s.cells; i++ {
			cells[i] = ch
		}
	}
	if l.DepWait > 0 {
		fill(0, l.DepWait, cellDeps, false)
	}
	ready := val(l.Ready)
	if l.LockWait > 0 {
		fill(ready, ready+l.LockWait, cellLock, false)
	}
	if l.QueueWait > 0 && l.slot != nil {
		fill(*l.slot-l.QueueWait, *l.slot, cellQueue, false)
	}
	if l.Ran() {
		fill(*l.Start, *l.End, cellRun, true)
	}
	return strings.Join(cells, "")
}

func (s scale) stepBar(st Step) string {
	a, b := s.at(st.Start), s.at(st.Start+st.Dur)
	if b <= a {
		b = a + 1
	}
	if b > s.cells {
		a, b = s.cells-1, s.cells
	}
	return strings.Repeat(" ", a) + strings.Repeat(cellRun, b-a) + strings.Repeat(" ", s.cells-b)
}

func secs(f float64) string { return fmt.Sprintf("%.2f", f) }

func span(t *Timeline) float64 {
	s := t.Total
	for _, l := range t.Lanes {
		if l.End != nil && *l.End > s {
			s = *l.End
		}
	}
	return s
}

func barCells(width int) int {
	if width <= 0 {
		width = 100
	}
	return max(20, min(80, width-58))
}

func laneColor(state string) string {
	switch state {
	case status.Failed, status.Interrupted:
		return ui.Red
	case status.Skipped:
		return ui.Dim
	}
	return ""
}

func trunc(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// Text renders a run's timeline.
func Text(w io.Writer, t *Timeline, o Options) {
	p := ui.Painter{On: o.Color}
	shown, hidden := Shown(t, o)
	fmt.Fprintf(w, "run %s · %d lanes · %ss · longest chain %ss (overhead %ss)\n\n",
		t.Run, len(t.Lanes), secs(t.Total), secs(t.ChainS), secs(t.Overhead))
	sc := scale{cells: barCells(o.Width), span: span(t)}
	width := 2
	for _, l := range t.Lanes {
		width = max(width, len(fmt.Sprint(l.Lane)))
	}
	for _, l := range shown {
		label := fmt.Sprintf("swim %*d", width, l.Lane)
		times := "    skipped"
		if l.Ran() {
			times = secs(*l.Start) + " → " + secs(*l.End)
		}
		after := "(root)"
		if len(l.After) > 0 {
			after = "after " + trunc(joinInts(l.After), 24)
		}
		note := ""
		switch {
		case l.State == status.Skipped:
			note = "skipped: " + l.Reason
		case l.State != status.Passed:
			note = l.State
		}
		mark := ""
		if l.OnChain {
			mark = p.Paint(ui.Yellow, "◆")
		}
		row := fmt.Sprintf("%s |%s| %-14s %-30s %s", label, sc.bar(l), times, after, mark)
		if note != "" {
			row += " " + note
		}
		fmt.Fprintln(w, strings.TrimRight(p.Paint(laneColor(l.State), row), " "))
		if o.Steps {
			for _, st := range l.Steps {
				srow := fmt.Sprintf("%*s |%s| %-14s %s", width+5, "·", sc.stepBar(st),
					secs(st.Start)+" → "+secs(st.Start+st.Dur), trunc(st.Label, 40))
				if st.Kind != "" && st.Kind != "PASS" {
					srow += "  " + st.Kind
				}
				fmt.Fprintln(w, p.Paint(ui.StateColor(st.Kind)+ui.Dim, srow))
			}
		}
	}
	if hidden > 0 {
		fmt.Fprintf(w, "… %d more lanes (--all to show them, --top N to choose)\n", hidden)
	}
	fmt.Fprintln(w)
	if len(t.Chain) > 0 {
		fmt.Fprintf(w, "longest chain: %s\n", chainText(t.Chain))
	}
	if t.Delays.Count > 0 {
		fmt.Fprintf(w, "start delay after last parent: median %ss · p95 %ss · max %ss (swim %d)\n",
			secs(t.Delays.Median), secs(t.Delays.P95), secs(t.Delays.Max), t.Delays.MaxOf)
	}
	fmt.Fprintf(w, "waiting on dependencies: %s of lane-time · queued: %s · lock waits: %s\n",
		pct(t.Waiting.Deps), pct(t.Waiting.Queued), pct(t.Waiting.Locks))
	fmt.Fprintf(w, "legend: %s running  %s waiting on dependencies  %s queued (max_parallel)  %s waiting for a lock  ◆ longest chain\n",
		cellRun, cellDeps, cellQueue, cellLock)
}

// HistoryText renders a lane's or job's rounds across runs: one bar per
// round, each from its run's start.
func HistoryText(w io.Writer, h *History, o Options) {
	p := ui.Painter{On: o.Color}
	if len(h.Entries) == 0 {
		fmt.Fprintf(w, "no recorded runs of %s yet (run records start with this version of swim)\n", h.Of)
		return
	}
	fmt.Fprintf(w, "%s · %d rounds\n\n", h.Of, len(h.Entries))
	longest := 0.0
	for _, e := range h.Entries {
		longest = max(longest, val(e.Lane.End))
	}
	sc := scale{cells: barCells(o.Width), span: longest}
	for _, e := range h.Entries {
		l := e.Lane
		times := "    skipped"
		if l.Ran() {
			times = secs(*l.Start) + " → " + secs(*l.End)
		}
		row := fmt.Sprintf("%s %s |%s| %-14s swim %d %s", e.Started.Local().Format("01-02 15:04"), e.Run, sc.bar(&l), times, l.Lane, l.State)
		fmt.Fprintln(w, p.Paint(laneColor(l.State), row))
	}
}

func pct(f float64) string { return fmt.Sprintf("%.0f%%", f*100) }

func joinInts(ns []int) string {
	s := make([]string, len(ns))
	for i, n := range ns {
		s[i] = fmt.Sprint(n)
	}
	return strings.Join(s, " ")
}

func chainText(c []int) string {
	s := make([]string, len(c))
	for i, n := range c {
		s[i] = fmt.Sprint(n)
	}
	if len(s) > 12 {
		s = append(append(s[:6:6], "…"), s[len(s)-5:]...)
	}
	return strings.Join(s, " → ")
}
