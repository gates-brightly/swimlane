package hub

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"
)

// Counts summarises a worker's lanes for the dashboard.
func (v View) Counts() map[string]int {
	c := map[string]int{}
	for _, l := range v.Lanes {
		c[l.State]++
	}
	return c
}

// QueueCounts summarises its queue.
func (v View) QueueCounts() map[string]int {
	c := map[string]int{}
	for _, b := range v.Queue {
		c[b.State]++
	}
	return c
}

// Active are its lanes doing something now, or that just finished badly.
func (v View) Active() []Lane {
	var out []Lane
	for _, l := range v.Lanes {
		switch l.State {
		case "idle", "pending", "":
			continue
		}
		out = append(out, l)
	}
	return out
}

func ago(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	return t.Format("Jan 2 15:04")
}

func stateClass(s string) string {
	s = strings.ToLower(s)
	switch {
	case strings.HasPrefix(s, "pass"), s == "done":
		return "ok"
	case strings.HasPrefix(s, "fail"), strings.HasPrefix(s, "interrupt"), s == "orphaned", s == "error":
		return "bad"
	case strings.HasPrefix(s, "running"):
		return "run"
	case strings.HasPrefix(s, "skip"), s == "waiting", s == "queued", s == "locked":
		return "wait"
	}
	return "dim"
}

var funcs = template.FuncMap{"ago": ago, "state": stateClass, "short": func(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}}

const style = `<style>
:root { --bg:#fbfbfa; --fg:#1d1d1f; --muted:#6e6e73; --line:#e3e3e0; --card:#fff; --ok:#1a7f37; --bad:#cf222e; --run:#0969da; --wait:#9a6700; }
@media (prefers-color-scheme: dark) { :root { --bg:#111214; --fg:#e8e8ea; --muted:#9a9aa1; --line:#2a2c30; --card:#18191c; --ok:#3fb950; --bad:#f85149; --run:#58a6ff; --wait:#d29922; } }
* { box-sizing: border-box; }
body { margin:0; padding:16px; background:var(--bg); color:var(--fg); font:14px/1.45 -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
h1 { font-size:18px; margin:0 0 4px; } h2 { font-size:15px; margin:20px 0 8px; }
a { color:var(--run); text-decoration:none; } a:hover { text-decoration:underline; }
.muted, .dim { color:var(--muted); } .ok { color:var(--ok); } .bad { color:var(--bad); } .run { color:var(--run); } .wait { color:var(--wait); }
.grid { display:grid; gap:12px; grid-template-columns:repeat(auto-fill, minmax(320px, 1fr)); margin-top:12px; }
.card { background:var(--card); border:1px solid var(--line); border-radius:8px; padding:12px 14px; }
.card.stale { opacity:.55; }
.card h3 { margin:0 0 2px; font-size:15px; display:flex; justify-content:space-between; gap:8px; }
.mono, pre, td.mono { font-family:ui-monospace, SFMono-Regular, Menlo, monospace; font-size:12.5px; }
table { width:100%; border-collapse:collapse; } th, td { text-align:left; padding:5px 8px; border-bottom:1px solid var(--line); vertical-align:top; }
th { font-weight:600; color:var(--muted); font-size:12px; }
pre { background:var(--card); border:1px solid var(--line); border-radius:6px; padding:8px 10px; overflow-x:auto; margin:6px 0 0; white-space:pre-wrap; word-break:break-word; }
.pill { display:inline-block; padding:0 6px; border-radius:9px; border:1px solid currentColor; font-size:11.5px; }
.wrap { overflow-x:auto; }
</style>`

var dashTmpl = template.Must(template.New("dash").Funcs(funcs).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="3"><title>swim hub</title>` + style + `</head><body>
<h1>swim hub</h1>
<div class="muted">{{len .}} worker(s) · refreshes every 3s · <a href="/api/workers">JSON</a></div>
{{if not .}}<p class="muted">No workers yet. In a repo: <span class="mono">swim hub enroll &lt;this hub's URL&gt; --token &lt;token&gt;</span>, then <span class="mono">swim watch</span>.</p>{{end}}
<div class="grid">
{{range .}}<div class="card{{if .Stale}} stale{{end}}">
  <h3><a href="/w/{{.ID}}">{{.Name}}</a> {{if .Stale}}<span class="pill bad">stale</span>{{else}}<span class="pill {{state .Activity}}">{{.Activity}}</span>{{end}}</h3>
  <div class="muted mono">{{.Host}}:{{.Root}}</div>
  <div class="muted">seen {{ago .Seen}}{{if .Version}} · swim {{.Version}}{{end}}</div>
  {{with .Counts}}<div style="margin-top:6px">{{range $s, $n := .}}<span class="{{state $s}}">{{$n}} {{$s}}</span> &nbsp;{{end}}</div>{{end}}
  {{with .QueueCounts}}<div class="muted">queue: {{range $s, $n := .}}{{$n}} {{$s}} &nbsp;{{end}}</div>{{end}}
  {{range .Active}}<div class="mono" style="margin-top:4px"><span class="{{state .State}}">swim {{.Lane}} {{.State}}</span> {{.Round}}{{if .Step}} <span class="muted">· {{.Step}}</span>{{end}}</div>{{end}}
</div>{{end}}
</div></body></html>`))

var workerTmpl = template.Must(template.New("worker").Funcs(funcs).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="3"><title>{{.Name}} · swim hub</title>` + style + `</head><body>
<div><a href="/">← all workers</a></div>
<h1>{{.Name}} {{if .Stale}}<span class="pill bad">stale</span>{{else}}<span class="pill {{state .Activity}}">{{.Activity}}</span>{{end}}</h1>
<div class="muted mono">{{.Host}}:{{.Root}} · pid {{.PID}}</div>
<div class="muted">seen {{ago .Seen}}{{if .Version}} · swim {{.Version}}{{end}}</div>
<h2>Lanes</h2>
<div class="wrap"><table><tr><th>lane</th><th>state</th><th>round</th><th>step</th><th>pass</th><th>fail</th><th>skip</th><th>run</th></tr>
{{range .Lanes}}<tr><td>swim {{.Lane}}</td><td class="{{state .State}}">{{.State}}{{if .Exit}} (exit {{.Exit}}){{end}}{{if .Reason}} <span class="muted">{{.Reason}}</span>{{end}}</td><td>{{.Round}}</td><td class="muted">{{.Step}}</td><td>{{.Pass}}</td><td>{{.Fail}}</td><td>{{.Skip}}</td><td class="mono muted">{{.Run}}</td></tr>{{end}}
</table></div>
{{range .Lanes}}{{if .Tail}}<h2>swim {{.Lane}} · {{.Round}}</h2><pre>{{range .Tail}}{{.}}
{{end}}</pre>{{end}}{{end}}
<h2>Queue</h2>
{{if .Queue}}<div class="wrap"><table><tr><th>#</th><th>id</th><th>state</th><th>lanes</th><th>by</th><th>note</th></tr>
{{range .Queue}}<tr><td>{{.Seq}}</td><td class="mono">{{.ID}}</td><td class="{{state .State}}">{{.State}}{{if .Result}}: {{.Result}}{{end}}</td><td>{{range .Lanes}}{{.}} {{end}}</td><td>{{.By}}</td><td class="muted">{{.Note}}</td></tr>{{end}}
</table></div>{{else}}<p class="muted">empty</p>{{end}}
<h2>Recent events</h2>
<pre>{{range .Events}}{{.}}
{{end}}</pre>
</body></html>`))

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	dashTmpl.Execute(w, s.Workers())
}

func (s *Server) workerPage(w http.ResponseWriter, r *http.Request) {
	for _, v := range s.Workers() {
		if v.ID == r.PathValue("id") {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			workerTmpl.Execute(w, v)
			return
		}
	}
	http.NotFound(w, r)
}
