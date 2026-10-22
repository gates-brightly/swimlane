package hub

import (
	"fmt"
	"html/template"
	"net/http"
	"strconv"
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

// pct is a progress "7/25" as a percentage (0 if it isn't one).
func pct(p string) int {
	var n, m int
	if _, err := fmt.Sscanf(p, "%d/%d", &n, &m); err != nil || m <= 0 {
		return 0
	}
	return min(100, max(0, n*100/m))
}

// msgAge is how old a status message is, when that's worth saying (a
// minute or more: it may be stuck).
func msgAge(at string) string {
	t, err := time.Parse(time.RFC3339, at)
	if err != nil || time.Since(t) < time.Minute {
		return ""
	}
	return ago(t)
}

var funcs = template.FuncMap{"ago": ago, "state": stateClass, "pct": pct, "msgAge": msgAge, "short": func(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}}

// style links the classic pages' stylesheet (web/classic.css).
const style = `<link rel="stylesheet" href="/assets/classic.css"><link rel="icon" href="/assets/favicon.svg">`

var dashTmpl = template.Must(template.New("dash").Funcs(funcs).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="3"><title>{{if .NS}}{{.Label}} · {{end}}swim hub</title>` + style + `</head><body>
<h1>swim hub</h1>
<div class="muted">{{len .Workers}} worker(s){{if .NS}} in {{.Label}}{{end}} · refreshes every 3s · <a href="/api/enrollments{{if .NS}}?ns={{.NS}}{{end}}">JSON</a></div>
{{if or (gt (len .Tabs) 2) .NS}}<nav class="tabs">{{range .Tabs}}<a href="/classic{{if .Key}}?ns={{.Key}}{{end}}"{{if .Active}} class="on"{{end}}>{{.Label}}<span class="n">{{.Workers}}{{if .Running}} · <span class="run">{{.Running}} running</span>{{end}}{{if .Waiting}} · <span class="wait">{{.Waiting}} waiting</span>{{end}}</span></a>{{end}}</nav>{{end}}
{{if not .Workers}}{{if .NS}}<p class="muted">No workers in {{.Label}} now. <a href="/classic">All workers</a></p>{{else}}<p class="muted">No workers yet. In a repo: <span class="mono">swim hub enroll &lt;this hub's URL&gt; --token &lt;token&gt;</span>, then <span class="mono">swim watch</span>.</p>{{end}}{{end}}
<div class="grid">
{{range .Workers}}<div class="card{{if .Stale}} stale{{end}}">
  <h3><a href="/w/{{.ID}}">{{.Name}}</a> {{if .Stale}}<span class="pill bad">stale</span>{{else}}<span class="pill {{state .Activity}}">{{.Activity}}</span>{{end}}</h3>
  {{if and (not $.NS) .Namespace}}<div class="mono"><a class="muted" href="/classic?ns={{.Namespace}}">{{.Namespace}}</a></div>{{end}}
  <div class="muted mono">{{.Host}}:{{.Root}}</div>
  {{with .Headless}}<div><span class="pill">headless · {{.Mode}}</span>{{if .Yes}} <b class="bad">-y</b>{{end}}</div>{{end}}
  {{with .AutoApprove}}<div><b class="bad">{{.}}</b></div>{{end}}
  <div class="muted">seen {{ago .Seen}}{{if .Version}} · swim {{.Version}}{{end}}</div>
  {{with .Counts}}<div class="mt6">{{range $s, $n := .}}<span class="{{state $s}}">{{$n}} {{$s}}</span> &nbsp;{{end}}</div>{{end}}
  {{with .QueueCounts}}<div class="muted">queue: {{range $s, $n := .}}{{$n}} {{$s}} &nbsp;{{end}}</div>{{end}}
  {{range .Active}}<div class="mono mt4"><span class="{{state .State}}">swim {{.Lane}} {{.State}}</span>{{if .AutoApproved}} <span class="pill bad" title="approved by {{.AutoVia}}: {{range .AutoApproved}}{{.}} {{end}}">-y</span>{{end}}{{if .DryRun}} <span class="pill" title="dry run: {{range .DryRun}}{{.}} {{end}}">[DRY]</span>{{end}} {{.Round}}{{if .Step}} <span class="muted">· {{.Step}}</span>{{end}}{{if .Message}}<br><span class="msg">… {{.Message}}{{if .Progress}} ({{.Progress}}){{end}}</span>{{if .Progress}}<progress class="bar" max="100" value="{{pct .Progress}}"></progress>{{end}}{{with msgAge .MsgAt}} <span class="muted">· {{.}}</span>{{end}}{{end}}</div>{{end}}
</div>{{end}}
</div></body></html>`))

// csrfField is the hidden field every form on a logged-in page carries.
const csrfField = `<input type="hidden" name="csrf" value="{{$.CSRF}}">`

var workerTmpl = template.Must(template.New("worker").Funcs(funcs).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="3"><title>{{.Name}} · swim hub</title>` + style + `</head><body>
<div><a href="/classic">← all workers</a>{{if .Namespace}} · <a href="/classic?ns={{.Namespace}}">{{.Namespace}}</a>{{end}} · {{if .User}}<span class="muted">signed in as <b>{{.User}}</b> ({{.Role}})</span> <form class="inline" method="post" action="/logout">` + csrfField + `<button>log out</button></form>{{else}}<a href="/login">log in</a> <span class="muted">to send commands</span>{{end}}</div>
<h1>{{.Name}} {{if .Stale}}<span class="pill bad">stale</span>{{else}}<span class="pill {{state .Activity}}">{{.Activity}}</span>{{end}}</h1>
<div class="muted mono">{{.Host}}:{{.Root}} · pid {{.PID}}</div>
{{with .Headless}}<div><span class="pill">headless · {{.Mode}}</span>{{if .Since}} <span class="muted">since {{.Since}}</span>{{end}}{{if .Approved}} <span class="muted">· --approved</span>{{end}}{{if .Yes}} <b class="bad">-y: approves every guard in every batch</b>{{end}}{{if .Allow}} <span class="muted">· takes from the hub: {{range .Allow}}{{.}} {{end}}{{if .LogsOff}}(logs: off){{end}}</span>{{end}}</div>{{end}}
{{with .AutoApprove}}<div><b class="bad">{{.}}: approves every guard in every batch</b></div>{{end}}
<div class="muted">seen {{ago .Seen}}{{if .Version}} · swim {{.Version}}{{end}}</div>
{{if $.Can "drain"}}<div class="mt6"><form class="inline" method="post" action="/api/workers/{{.ID}}/commands">` + csrfField + `<input type="hidden" name="kind" value="drain"><button class="danger">Stop the worker after this batch</button></form></div>{{end}}
{{if and .User (not .Headless)}}<p class="muted">This worker isn't headless (swim watch --headless): it takes no commands from the hub.</p>{{end}}
{{if .Waiting}}<h2>Waiting for an answer</h2>
<div class="wrap"><table><tr><th>lane</th><th>guard</th><th>the lane says</th><th>pinned to</th><th>waits until</th>{{if .User}}<th></th>{{end}}</tr>
{{range .Waiting}}<tr><td>swim {{.Lane}}{{if .Cell}} [{{.Cell}}]{{end}}</td><td class="mono">{{.Flag}}</td><td>{{.Desc}}{{if .Signers}}<br><span class="muted">signed: {{range .Signers}}{{.}} {{end}}{{if .Need}}· still {{.Need}}{{end}}</span>{{end}}</td><td class="mono muted">{{if .Job}}{{.Job}} · {{end}}{{short .Script}}{{if .EvidenceSHA}} · evidence {{short .EvidenceSHA}}{{end}}</td><td class="muted">{{.Expires}}</td>{{if $.User}}<td>
{{- if $.Can "answer"}}<a href="/w/{{$.ID}}/answer/{{.ID}}">Sign an answer</a>{{if and (not .SignedOnly) (eq $.Role "operator")}} <form class="inline" method="post" action="/api/workers/{{$.ID}}/commands">` + csrfField + `<input type="hidden" name="kind" value="answer"><input type="hidden" name="request" value="{{.ID}}"><input type="hidden" name="evidence" value="{{.EvidenceSHA}}"><input type="hidden" name="decision" value="approve"><button>Approve</button></form>{{end}}{{if eq $.Role "operator"}} <form class="inline" method="post" action="/api/workers/{{$.ID}}/commands">` + csrfField + `<input type="hidden" name="kind" value="answer"><input type="hidden" name="request" value="{{.ID}}"><input type="hidden" name="evidence" value="{{.EvidenceSHA}}"><input type="hidden" name="decision" value="decline"><button class="danger">Decline</button></form>{{end}}{{if .SignedOnly}}<br><span class="muted">only signed answers approve it</span>{{end}}{{end -}}
</td>{{end}}</tr>{{end}}
</table></div>{{end}}
<h2>Lanes</h2>
<div class="wrap"><table><tr><th>lane</th><th>state</th><th>round</th><th>step</th><th>pass</th><th>fail</th><th>skip</th><th>run</th>{{if .User}}<th></th>{{end}}</tr>
{{range .Lanes}}<tr><td>swim {{.Lane}}</td><td class="{{state .State}}">{{.State}}{{if .Paused}} <span class="pill">paused {{.Paused}}</span>{{end}}{{if .Exit}} (exit {{.Exit}}){{end}}{{if .Reason}} <span class="muted">{{.Reason}}</span>{{end}}{{if .AutoApproved}}<br><span class="pill bad">approved by {{.AutoVia}}, no one asked: {{range $i, $f := .AutoApproved}}{{if $i}}, {{end}}{{$f}}{{end}}</span>{{end}}{{if .FlagApproved}}<br><span class="pill">flag set: {{range $i, $f := .FlagApproved}}{{if $i}}, {{end}}{{$f}}{{end}}</span>{{end}}{{if .DryRun}}<br><span class="pill">[DRY] dry run: {{range $i, $f := .DryRun}}{{if $i}}, {{end}}{{$f}}{{end}}</span>{{end}}{{if .Scheduled}}<br><span class="pill">scheduled {{.Scheduled}}</span>{{end}}{{if .HeldBy}}<br><span class="pill">held by schedule {{.HeldBy}}</span>{{end}}</td><td>{{.Round}}</td><td class="muted">{{.Step}}{{if .Cells}}<br>cells: {{.Cells}}{{end}}{{if .Message}}<br><span class="msg">… {{.Message}}{{if .Progress}} ({{.Progress}}){{end}}</span>{{with msgAge .MsgAt}} · {{.}}{{end}}{{end}}</td><td>{{.Pass}}</td><td>{{.Fail}}</td><td>{{.Skip}}</td><td class="mono muted">{{.Run}}</td>{{if $.User}}<td>
{{- if and (eq .State "running") .Run (not .Paused) ($.Can "pause")}}<form class="inline" method="post" action="/api/workers/{{$.ID}}/commands">` + csrfField + `<input type="hidden" name="kind" value="pause"><input type="hidden" name="lane" value="{{.Lane}}"><input type="hidden" name="run" value="{{.Run}}"><button>Pause</button></form> {{end}}
{{- if and .Paused .Run ($.Can "unpause")}}<form class="inline" method="post" action="/api/workers/{{$.ID}}/commands">` + csrfField + `<input type="hidden" name="kind" value="unpause"><input type="hidden" name="lane" value="{{.Lane}}"><input type="hidden" name="run" value="{{.Run}}"><button>Unpause</button></form> {{end}}
{{- if and (eq .State "running") .Run ($.Can "interrupt")}}<form class="inline" method="post" action="/api/workers/{{$.ID}}/commands">` + csrfField + `<input type="hidden" name="kind" value="interrupt"><input type="hidden" name="lane" value="{{.Lane}}"><input type="hidden" name="run" value="{{.Run}}"><button class="danger">Interrupt</button></form> {{end}}
{{- if and .Run ($.Can "log")}}<form class="inline" method="post" action="/api/workers/{{$.ID}}/commands">` + csrfField + `<input type="hidden" name="kind" value="log"><input type="hidden" name="lane" value="{{.Lane}}"><input type="hidden" name="run" value="{{.Run}}"><button>Log</button></form> {{end}}
{{- if and .Script (ne .State "running") ($.Can "run")}}<form class="inline" method="post" action="/api/workers/{{$.ID}}/commands">` + csrfField + `<input type="hidden" name="kind" value="run"><input type="hidden" name="lane" value="{{.Lane}}"><input type="hidden" name="sha256" value="{{.Script}}"><input type="hidden" name="manifest" value="{{.Manifest}}"><button>Run now</button> <span class="mono muted">{{short .Script}}</span></form>{{end -}}
{{- if and .TrashJob (ne .State "running") ($.Can "trash")}} <a class="danger" href="/w/{{$.ID}}/trash/{{.Lane}}?job={{.TrashJob}}">Trash</a>{{end -}}
</td>{{end}}</tr>{{end}}
</table></div>
{{range .Lanes}}{{if .Tail}}<h2>swim {{.Lane}} · {{.Round}}</h2><pre>{{range .Tail}}{{.}}
{{end}}</pre>{{end}}{{end}}
<h2>Queue</h2>
{{if .Queue}}<div class="wrap"><table><tr><th>#</th><th>id</th><th>state</th><th>lanes</th><th>by</th><th>note</th></tr>
{{range .Queue}}<tr><td>{{if .Seq}}{{.Seq}}{{end}}</td><td class="mono">{{.ID}}</td><td class="{{state .State}}">{{.State}}{{if .Result}}: {{.Result}}{{end}}{{if .Daily}}<br><span class="muted">{{.Daily}}</span>{{else if .At}}<br><span class="muted">at {{.At}}</span>{{end}}{{if .HeldBy}}<br><span class="pill">held until {{.HeldBy}} has run</span>{{end}}</td><td>{{range .Lanes}}{{.}} {{end}}</td><td>{{.By}}</td><td class="muted">{{.Note}}</td></tr>{{end}}
</table></div>{{else}}<p class="muted">empty</p>{{end}}
{{if .User}}<h2>Commands</h2>
{{if .Commands}}<div class="wrap"><table><tr><th>#</th><th>command</th><th>by</th><th>sent</th><th>outcome</th></tr>
{{range .Commands}}{{$st := $.StateOf .}}<tr><td>{{.Seq}}</td><td>{{.Kind}}{{with .Answer}} {{.Decision}}{{if .Sig}} (signed){{end}}{{end}}{{if .Lane}} swim {{.Lane}}{{end}}{{if .Pin.Run}} <span class="mono muted">{{.Pin.Run}}</span>{{end}}{{if .Pin.SHA256}} <span class="mono muted">{{short .Pin.SHA256}}</span>{{end}}{{if and (eq .Kind "log") .Pin.Run}} · <a href="/w/{{$.ID}}/logs/{{.Pin.Run}}/{{.Lane}}">view</a>{{end}}</td><td>{{.By}}</td><td class="muted">{{.At}}</td><td class="{{state $st}}">{{$st}}{{if .Detail}}: <span class="muted">{{.Detail}}</span>{{end}}</td></tr>{{end}}
</table></div>{{else}}<p class="muted">none sent</p>{{end}}{{end}}
<h2>Recent events</h2>
<pre>{{range .Events}}{{.}}
{{end}}</pre>
</body></html>`))

var loginTmpl = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>log in · swim hub</title>` + style + `</head><body>
<div><a href="/">← all workers</a></div>
<h1>Log in to the swim hub</h1>
{{if .Failed}}<p class="bad">That token isn't a user's.</p>{{end}}
<p class="muted">Your token came from <span class="mono">swim hub user add NAME</span> (the hub's admin runs it). The login lasts 12 hours.</p>
<form method="post" action="/api/login"><input type="password" name="token" placeholder="your hub user token" autocomplete="off" autofocus> <button>Log in</button></form>
</body></html>`))

var logTmpl = template.Must(template.New("log").Funcs(funcs).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
{{if not .Have}}<meta http-equiv="refresh" content="2">{{end}}<title>swim {{.Lane}} log · {{.Name}} · swim hub</title>` + style + `</head><body>
<div><a href="/w/{{.ID}}">← {{.Name}}</a></div>
<h1>swim {{.Lane}} · run <span class="mono">{{.Run}}</span></h1>
{{if .Have}}<div class="muted">uploaded {{ago .At}} · masked of secrets by the worker · kept in the hub's memory for 15 minutes</div><pre>{{.Text}}</pre>
{{else}}<p class="muted">{{.State}}{{if .Detail}}: {{.Detail}}{{end}}{{if eq .State "sent"}} · waiting for the worker to upload it…{{end}}</p>{{end}}
</body></html>`))

// workerData is what the worker page shows: the worker, and the user
// logged in ("" for nobody), their CSRF token and the worker's commands.
type workerData struct {
	View
	User, Role, CSRF string
	Commands         []Command
	now              time.Time
}

// Can reports whether the logged-in user may send command kind here and
// the worker takes it.
func (d workerData) Can(kind string) bool {
	if d.User == "" || (kind != KindLog && kind != KindAnswer && d.Role != RoleOperator) {
		return false
	}
	return d.Headless.Allows(kind)
}

// StateOf is a command's state now.
func (d workerData) StateOf(c Command) string { return c.State(d.now) }

// dashData is the dashboard: the namespace tabs and the selected one's
// workers (NS "" is every worker).
type dashData struct {
	Tabs    []Tab
	Workers []View
	NS      string
	Label   string
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	if !s.canView(r) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	views := s.Workers()
	d := dashData{NS: nsQuery(r)}
	d.Tabs = tabs(views, d.NS)
	d.Workers = inNamespace(views, d.NS)
	for _, t := range d.Tabs {
		if t.Active {
			d.Label = t.Label
		}
	}
	if d.Label == "" { // a namespace with no workers now
		d.Label = d.NS
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	dashTmpl.Execute(w, d)
}

func (s *Server) workerPage(w http.ResponseWriter, r *http.Request) {
	if !s.canView(r) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	v, ok := s.worker(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	d := workerData{View: v, now: s.now()}
	d.User, d.Role, d.CSRF = s.viewer(r)
	if d.User != "" {
		d.Commands = s.Commands(v.ID)
		if len(d.Commands) > 20 {
			d.Commands = d.Commands[:20]
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	workerTmpl.Execute(w, d)
}

// worker is one worker as Workers shows it.
func (s *Server) worker(id string) (View, bool) {
	for _, v := range s.Workers() {
		if v.ID == id {
			return v, true
		}
	}
	return View{}, false
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	loginTmpl.Execute(w, map[string]bool{"Failed": r.URL.Query().Get("failed") != ""})
}

// logPage shows a log a user asked for, polling until the worker uploads
// it (or says why it won't).
func (s *Server) logPage(w http.ResponseWriter, r *http.Request) {
	if user, _, _ := s.viewer(r); user == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if _, ok := s.requireUser(w, r, RoleViewer); !ok {
		return
	}
	v, ok := s.worker(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	run := r.PathValue("run")
	lane, _ := strconv.Atoi(r.PathValue("lane"))
	d := struct {
		ID, Name, Run, State, Detail, Text string
		Lane                               int
		Have                               bool
		At                                 time.Time
	}{ID: v.ID, Name: v.Name, Run: run, Lane: lane, State: "not asked for: use the Log button on the worker's page"}
	for _, c := range s.Commands(v.ID) {
		if c.Kind == KindLog && c.Pin.Run == run && c.Lane == lane {
			d.State, d.Detail = c.State(s.now()), c.Detail
			break
		}
	}
	if data, at, ok := s.logs.get(logKey(v.ID, run, lane)); ok {
		d.Have, d.Text, d.At = true, string(data), at
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	logTmpl.Execute(w, d)
}
