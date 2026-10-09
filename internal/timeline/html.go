package timeline

import (
	"fmt"
	"html/template"
	"io"

	"github.com/gates-brightly/swimlane/internal/status"
)

// HTML writes a self-contained Gantt chart of the run (inline SVG, no
// external assets), for sharing after an incident.
func HTML(w io.Writer, t *Timeline) error {
	const rowH, labelW, chartW = 22, 70, 900
	sp := span(t)
	if sp <= 0 {
		sp = 1
	}
	x := func(sec float64) float64 { return labelW + sec/sp*chartW }
	type rect struct {
		X, W  float64
		Y     int
		Class string
		Title string
	}
	type row struct {
		Y     int
		Label string
		Rects []rect
		Note  string
		Class string
	}
	var rows []row
	y := 30
	for i := range t.Lanes {
		l := &t.Lanes[i]
		r := row{Y: y, Label: fmt.Sprintf("swim %d", l.Lane), Class: l.State}
		add := func(from, to float64, class, title string) {
			if to <= from {
				return
			}
			r.Rects = append(r.Rects, rect{X: x(from), W: max(1, x(to)-x(from)), Y: y, Class: class, Title: title})
		}
		add(0, l.DepWait, "deps", fmt.Sprintf("waiting on swim %s: %.2fs", joinInts(l.After), l.DepWait))
		ready := val(l.Ready)
		add(ready, ready+l.LockWait, "lock", fmt.Sprintf("waiting for a lock: %.2fs", l.LockWait))
		if l.slot != nil {
			add(*l.slot-l.QueueWait, *l.slot, "queue", fmt.Sprintf("queued: %.2fs", l.QueueWait))
		}
		if l.Ran() {
			add(*l.Start, *l.End+0.0001, "run "+l.State, fmt.Sprintf("swim %d %s · %.2f → %.2f (%.2fs) · %s", l.Lane, l.State, *l.Start, *l.End, l.Duration, l.Round))
			for _, st := range l.Steps {
				add(st.Start, st.Start+st.Dur, "step", fmt.Sprintf("%s %s (%.2fs)", st.Kind, st.Label, st.Dur))
			}
		}
		if l.OnChain {
			r.Note = "◆"
		}
		if l.State == status.Skipped {
			r.Note = "skipped: " + l.Reason
		}
		rows = append(rows, r)
		y += rowH
	}
	data := map[string]any{
		"T": t, "Rows": rows, "H": y + 20, "W": labelW + chartW + 220, "RowH": rowH - 6,
		"Chain": chainText(t.Chain), "NoteX": labelW + chartW + 8,
		"Deps": pct(t.Waiting.Deps), "Queued": pct(t.Waiting.Queued), "Locks": pct(t.Waiting.Locks),
	}
	return htmlTmpl.Execute(w, data)
}

var htmlTmpl = template.Must(template.New("timeline").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>swim timeline {{.T.Run}}</title>
<style>
:root { --bg:#fff; --fg:#1d1d1f; --muted:#6e6e73; --run:#2f80ed; --fail:#d93025; --deps:#e3e8ef; --queue:#c7d0db; --lock:#9aa5b1; --step:rgba(255,255,255,.45); }
@media (prefers-color-scheme: dark) { :root { --bg:#16181c; --fg:#e8e8ea; --muted:#9a9aa1; --deps:#2a2f37; --queue:#3a414b; --lock:#59626e; --step:rgba(0,0,0,.35); } }
body { background:var(--bg); color:var(--fg); font:14px/1.4 ui-monospace, SFMono-Regular, Menlo, monospace; margin:16px; }
h1 { font-size:16px; margin:0 0 4px; } p { margin:2px 0; color:var(--muted); }
.wrap { overflow-x:auto; } svg text { fill:var(--fg); font-size:12px; }
.deps { fill:var(--deps); } .queue { fill:var(--queue); } .lock { fill:var(--lock); }
.run { fill:var(--run); } .run.failed, .run.interrupted { fill:var(--fail); } .step { fill:var(--step); stroke:var(--bg); stroke-width:.5; }
g.skipped text { fill:var(--muted); }
</style></head><body>
<h1>swim run {{.T.Run}}</h1>
<p>{{.T.Started.Format "2006-01-02 15:04:05 UTC"}} · {{len .T.Lanes}} lanes · {{printf "%.2f" .T.Total}}s · longest chain {{printf "%.2f" .T.ChainS}}s (overhead {{printf "%.2f" .T.Overhead}}s)</p>
{{if .T.Chain}}<p>longest chain: {{.Chain}}</p>{{end}}
{{if .T.Delays.Count}}<p>start delay after last parent: median {{printf "%.2f" .T.Delays.Median}}s · p95 {{printf "%.2f" .T.Delays.P95}}s · max {{printf "%.2f" .T.Delays.Max}}s (swim {{.T.Delays.MaxOf}})</p>{{end}}
<p>waiting on dependencies: {{.Deps}} of lane-time · queued: {{.Queued}} · lock waits: {{.Locks}}</p>
<div class="wrap"><svg xmlns="http://www.w3.org/2000/svg" width="{{.W}}" height="{{.H}}">
{{range .Rows}}<g class="{{.Class}}"><text x="0" y="{{.Y}}" dy="12">{{.Label}}</text>
{{range .Rects}}<rect class="{{.Class}}" x="{{printf "%.1f" .X}}" y="{{.Y}}" width="{{printf "%.1f" .W}}" height="{{$.RowH}}" rx="2"><title>{{.Title}}</title></rect>
{{end}}{{if .Note}}<text x="{{$.NoteX}}" y="{{.Y}}" dy="12">{{.Note}}</text>{{end}}</g>
{{end}}
</svg></div></body></html>
`))
