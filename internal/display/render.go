// Package display draws the launcher's live view: a panel pinned to the top
// of the terminal with one row per lane (swim 1..N), and lane output
// scrolling underneath with [N] prefixes.
package display

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"swim/internal/lane"
	"swim/internal/ui"
)

// Lane states shown in the panel (same words as status.yml).
const (
	Idle        = "idle"
	Queued      = "queued"
	Waiting     = "waiting"
	Running     = "running"
	Passed      = "passed"
	Failed      = "failed"
	Skipped     = "skipped"
	Interrupted = "interrupted"
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
	Started   time.Time
	Finished  time.Time
}

// Active reports whether the lane is still queued, waiting or running.
func (v LaneView) Active() bool {
	return v.State == Queued || v.State == Waiting || v.State == Running
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
	case Queued:
		return thr + " starting", ui.Cyan
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
	for _, v := range views {
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
		if v.State == Idle {
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
	lines = append(lines, p.Paint(ui.Dim, strings.Repeat("─", width)))
	return lines
}

// Prefix renders the [N] prefix for a lane's output line.
func Prefix(n int, color bool) string {
	return ui.Painter{On: color}.Paint(ui.LaneColor(n), fmt.Sprintf("[%d]", n)) + " "
}
