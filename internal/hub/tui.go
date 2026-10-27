package hub

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// The hub's terminal screen (swim hub on a terminal): every worker, the
// selected one's lanes and queue under the list, and one worker's lanes in
// detail. These build its rows; the cli runs them in a display.Picker.

// WorkerState is a worker in one word: stale, running, failed or idle.
func (v View) WorkerState(now time.Time) string {
	if v.Stale {
		return "stale " + shortAgo(now.Sub(v.Seen))
	}
	failed := false
	for _, l := range v.Lanes {
		switch l.State {
		case "running", "waiting", "queued", "locked":
			return "running"
		case "failed", "interrupted":
			failed = true
		}
	}
	if failed {
		return "failed"
	}
	return "idle"
}

func shortAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}

// laneMark is a lane as one character, for a worker's row.
func laneMark(state string) string {
	switch state {
	case "running":
		return "▶"
	case "waiting", "queued", "locked":
		return "…"
	case "passed":
		return "✓"
	case "failed":
		return "✗"
	case "interrupted":
		return "!"
	case "skipped":
		return "-"
	case "pending":
		return "○"
	}
	return "·"
}

func stateColor(state string) string {
	switch {
	case strings.HasPrefix(state, "stale"):
		return ui.Dim
	case state == "running":
		return ui.Cyan
	case state == "failed", state == "interrupted", state == "orphaned":
		return ui.Red
	case state == "waiting", state == "queued", state == "locked":
		return ui.Yellow
	case state == "passed":
		return ui.Green
	}
	return ""
}

// Summary is the screen's title line count: "3 swimmers · 2 lanes running".
func Summary(views []View) string {
	running := 0
	for _, v := range views {
		for _, l := range v.Lanes {
			if l.State == "running" {
				running++
			}
		}
	}
	w := "swimmers"
	if len(views) == 1 {
		w = "swimmer"
	}
	return fmt.Sprintf("%d %s · %d lanes running", len(views), w, running)
}

// ListRows is the main screen: notes (how to enroll, warnings), a row per
// worker (ID "w:<id>"), and the selected worker's lanes and queue.
func ListRows(views []View, sel string, notes []display.PickRow, now time.Time) []display.PickRow {
	rows := append([]display.PickRow{}, notes...)
	if len(notes) > 0 {
		rows = append(rows, display.PickRow{Text: " ", Color: ui.Dim})
	}
	if len(views) == 0 {
		return append(rows, display.PickRow{Text: "no swimmers yet: join one with swim auth login (above)", Color: ui.Dim})
	}
	rows = append(rows, display.PickRow{Text: fmt.Sprintf("%-14s %-16s %-10s %-9s %-40s %s", "SWIMMER", "HOST", "STATE", "SECRETS", "ACTIVITY", "LANES")})
	var chosen *View
	for i := range views {
		v := &views[i]
		id := "w:" + v.ID
		if id == sel || (sel == "" && chosen == nil) {
			chosen = v
		}
		state := v.WorkerState(now)
		var marks strings.Builder
		for _, l := range v.Lanes {
			marks.WriteString(laneMark(l.State))
		}
		activity := v.Activity
		if v.Stale {
			activity = "(no report since " + v.Seen.Format("15:04") + ")"
		}
		color := stateColor(state)
		if v.Pending() && !v.Stale {
			color = ui.Yellow
		}
		rows = append(rows, display.PickRow{ID: id, Color: color,
			Text: fmt.Sprintf("%-14s %-16s %-10s %-9s %-40s %s", clip(v.Name, 14), clip(v.Host, 16), state, secretsWord(*v), clip(activity, 40), marks.String())})
	}
	if chosen == nil {
		return rows
	}
	rows = append(rows, display.PickRow{Text: " ", Color: ui.Dim},
		display.PickRow{Text: "── " + chosen.Name + " · " + chosen.Root})
	if chosen.Pending() {
		rows = append(rows, display.PickRow{Text: "  pending: key " + chosen.KeyFP + " (check it matches what swim printed when it joined, then a to approve)", Color: ui.Yellow})
	}
	rows = append(rows, laneRows(*chosen, now, false)...)
	return rows
}

// secretsWord is a worker's standing for secrets, in one word.
func secretsWord(v View) string {
	switch {
	case v.KeyFP == "":
		return "-"
	case !v.Approved:
		return "PENDING"
	case len(v.Grants) > 0:
		return fmt.Sprintf("%d grants", len(v.Grants))
	}
	return "approved"
}

// laneRows are a worker's lanes doing something (all of them with every),
// then its queue.
func laneRows(v View, now time.Time, every bool) []display.PickRow {
	var rows []display.PickRow
	shown := 0
	for _, l := range v.Lanes {
		if !every && (l.State == "idle" || l.State == "") {
			continue
		}
		shown++
		rows = append(rows, display.PickRow{Text: "  " + laneLine(l, now), Color: stateColor(l.State)})
	}
	if shown == 0 {
		rows = append(rows, display.PickRow{Text: "  every lane idle", Color: ui.Dim})
	}
	for _, b := range v.Queue {
		rows = append(rows, display.PickRow{Text: "  " + batchLine(b), Color: stateColor(b.State)})
	}
	return rows
}

func laneLine(l Lane, now time.Time) string {
	parts := []string{}
	if l.Step != "" {
		parts = append(parts, l.Step)
	}
	if l.Progress != "" {
		parts = append(parts, l.Progress)
	}
	if l.Message != "" {
		parts = append(parts, l.Message)
	}
	if l.Cells != "" {
		parts = append(parts, l.Cells)
	}
	if t, err := time.Parse(time.RFC3339, l.Started); err == nil && l.State == "running" {
		parts = append(parts, display.Elapsed(now.Sub(t)))
	}
	if l.Reason != "" && l.State != "running" {
		parts = append(parts, l.Reason)
	}
	if len(l.DryRun) > 0 {
		parts = append(parts, "[DRY] "+strings.Join(l.DryRun, ", "))
	}
	if len(l.AutoApproved) > 0 {
		parts = append(parts, "approved by "+l.AutoVia+": "+strings.Join(l.AutoApproved, ", "))
	}
	return fmt.Sprintf("swim %-2d %-11s %-30s %s", l.Lane, l.State, clip(l.Round, 30), strings.Join(parts, " · "))
}

func batchLine(b Batch) string {
	ns := make([]string, len(b.Lanes))
	for i, n := range b.Lanes {
		ns[i] = strconv.Itoa(n)
	}
	s := fmt.Sprintf("queue   #%d %-8s swim %s", b.Seq, b.State, strings.Join(ns, ","))
	if b.Seq == 0 {
		s = fmt.Sprintf("queue   %s %-8s swim %s", b.ID, b.State, strings.Join(ns, ","))
	}
	if b.By != "" {
		s += "  by " + b.By
	}
	if b.Note != "" {
		s += "  " + b.Note
	}
	switch {
	case b.At != "":
		s += "  at " + b.At
	}
	return s
}

// WorkerRows is one worker in detail: every lane (ID "l:<n>"), the
// selected lane's status and log tail, its queue and recent events.
func WorkerRows(v View, sel string, now time.Time) []display.PickRow {
	rows := []display.PickRow{
		{Text: fmt.Sprintf("%s · %s · %s", v.Name, v.Host, v.Root)},
		{Text: "  " + v.WorkerState(now) + " · " + v.Activity, Color: ui.Dim},
	}
	switch {
	case v.KeyFP == "":
		rows = append(rows, display.PickRow{Text: "  secrets: none (enrolled without a key)", Color: ui.Dim})
	case !v.Approved:
		rows = append(rows, display.PickRow{Text: "  secrets: pending approval, key " + v.KeyFP, Color: ui.Yellow})
	default:
		line := "  secrets: approved, key " + v.KeyFP
		if len(v.Grants) > 0 {
			line += " · granted: " + strings.Join(v.Grants, ", ")
		}
		if t, err := time.Parse(time.RFC3339, v.Fetched); err == nil {
			line += " · last fetched " + t.Local().Format("15:04:05")
		}
		rows = append(rows, display.PickRow{Text: line, Color: ui.Dim})
	}
	rows = append(rows, display.PickRow{Text: " ", Color: ui.Dim})
	var chosen *Lane
	for i := range v.Lanes {
		l := &v.Lanes[i]
		id := "l:" + strconv.Itoa(l.Lane)
		if id == sel || (sel == "" && chosen == nil) {
			chosen = l
		}
		rows = append(rows, display.PickRow{ID: id, Text: laneLine(*l, now), Color: stateColor(l.State)})
	}
	if chosen != nil {
		rows = append(rows, display.PickRow{Text: " ", Color: ui.Dim},
			display.PickRow{Text: fmt.Sprintf("── swim %d · %s", chosen.Lane, or(chosen.Job, "no job"))})
		if chosen.Message != "" {
			rows = append(rows, display.PickRow{Text: "  msg: " + chosen.Message + progressText(chosen.Progress), Color: ui.Cyan})
		}
		if len(chosen.Tail) == 0 {
			rows = append(rows, display.PickRow{Text: "  (no log tail: the lane isn't running)", Color: ui.Dim})
		}
		for _, line := range chosen.Tail {
			rows = append(rows, display.PickRow{Text: "  " + line, Color: ui.Dim})
		}
	}
	if len(v.Queue) > 0 {
		rows = append(rows, display.PickRow{Text: " ", Color: ui.Dim}, display.PickRow{Text: "── queue"})
		for _, b := range v.Queue {
			rows = append(rows, display.PickRow{Text: "  " + batchLine(b), Color: stateColor(b.State)})
		}
	}
	if len(v.Events) > 0 {
		rows = append(rows, display.PickRow{Text: " ", Color: ui.Dim}, display.PickRow{Text: "── recent (.swim.log)"})
		for _, e := range v.Events {
			rows = append(rows, display.PickRow{Text: "  " + e, Color: ui.Dim})
		}
	}
	return rows
}

func progressText(p string) string {
	if p == "" {
		return ""
	}
	return " (" + p + ")"
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

func or(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
