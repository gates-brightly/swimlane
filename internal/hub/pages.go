package hub

import (
	"html/template"
	"net/http"

	"github.com/gates-brightly/swimlane/internal/version"
)

// The one server-rendered page: logging in, which needs no JavaScript.
// Everything else is the app (web.go).

var loginTmpl = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>log in · swim hub</title>
<link rel="stylesheet" href="/assets/login.css"><link rel="icon" href="/assets/favicon.svg"></head><body>
<main class="login"><a class="brand" href="/"><img src="/assets/favicon.svg" alt=""> swim hub</a>
<h1>Log in</h1>
{{if .Failed}}<p class="bad">That token isn't a user's.</p>{{end}}
<p class="muted">Your token came from <code>swim auth users add NAME</code> (the hub's admin runs it). The login lasts 12 hours.</p>
<form method="post" action="/api/login"><input type="password" name="token" placeholder="your hub user token" autocomplete="off" autofocus aria-label="your hub user token"> <button>Log in</button></form>
</main><footer class="foot">swim hub · swim {{.Version}}</footer></body></html>`))

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	loginTmpl.Execute(w, map[string]any{"Failed": r.URL.Query().Get("failed") != "", "Version": version.Long()})
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
