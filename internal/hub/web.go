package hub

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"strings"
)

// The hub's app: a single page (web/index.html) and its ES modules, CSS
// and the vendored Preact and htm (web/vendor), embedded in the binary,
// with no build step. It reads the read API (api.go) and the event stream
// (events.go); the server renders nothing of a worker's into it. Any path
// that isn't the API, an asset or one of the classic pages gets
// index.html, and the app routes it in the browser:
//
//	/                                  every enrollment
//	/approvals                         every guard waiting for an answer
//	/e/{id}/runs                       an enrollment's runs
//	/e/{id}/runs/{run}                 a run: its lanes as a graph
//	/e/{id}/runs/{run}/jobs/{lane}     a job: its stages, steps and output
//	/e/{id}/lanes, /e/{id}/queue       its lanes and queue now
//
// The classic pages (pages.go) stay at /classic and /w/{id} until the app
// sends commands too.
//
// Every response carries a strict Content-Security-Policy: scripts,
// styles and connections from the hub itself only, no inline script or
// style, no framing.

//go:embed web
var webFiles embed.FS

// CSP is the hub's Content-Security-Policy.
const CSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; " +
	"font-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"

var (
	webFS    fs.FS
	webETags = map[string]string{} // asset path -> its ETag
)

func init() {
	var err error
	if webFS, err = fs.Sub(webFiles, "web"); err != nil {
		panic(err)
	}
	fs.WalkDir(webFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(p, ".test.js") {
			return err // tests aren't served
		}
		data, _ := fs.ReadFile(webFS, p)
		sum := sha256.Sum256(data)
		webETags[p] = `"` + hex.EncodeToString(sum[:8]) + `"`
		return nil
	})
}

// secured adds the hub's security headers to every response.
func secured(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", CSP)
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "same-origin")
		hd.Set("X-Frame-Options", "DENY")
		h.ServeHTTP(w, r)
	})
}

func (s *Server) webRoutes(mux *http.ServeMux) {
	files := http.FileServerFS(webFS)
	mux.HandleFunc("GET /assets/{path...}", func(w http.ResponseWriter, r *http.Request) {
		p := r.PathValue("path")
		tag, ok := webETags[p]
		if !ok || p == "index.html" {
			http.NotFound(w, r)
			return
		}
		// Revalidate each time: an asset changes with the binary, and the
		// ETag makes that a 304 otherwise.
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", tag)
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + p
		files.ServeHTTP(w, r2)
	})
	mux.HandleFunc("GET /api/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such API: see swim hub --help", http.StatusNotFound)
	})
	mux.HandleFunc("GET /", s.app)
}

// app serves index.html for the app's routes (a private hub's to a
// logged-in user only).
func (s *Server) app(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/assets/") {
		http.NotFound(w, r)
		return
	}
	if !s.canView(r) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	data, err := fs.ReadFile(webFS, "index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(data)
}

// meAPI is who the request is logged in as ({"user": ""} for nobody),
// with the session's CSRF token for the app's POSTs, and whether viewing
// needs a login.
func (s *Server) meAPI(w http.ResponseWriter, r *http.Request) {
	user, role, csrf := s.viewer(r)
	writeJSON(w, map[string]any{"schema": APISchema, "user": user, "role": role, "csrf": csrf, "private": s.private})
}
