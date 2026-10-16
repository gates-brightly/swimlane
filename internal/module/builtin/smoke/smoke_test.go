package smoke

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/module/gomod/gomodtest"
)

func TestSmoke(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
		case "/auth":
			if r.Header.Get("Authorization") != "Bearer tok123" {
				w.WriteHeader(401)
			}
		case "/slow":
			time.Sleep(80 * time.Millisecond)
		case "/ping":
			if r.Method != "POST" {
				w.WriteHeader(405)
				return
			}
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()

	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "smoke"), 0o755)
	os.WriteFile(filepath.Join(root, "smoke", "ok.yml"), []byte(`base: ${API}
checks:
  - get: /health
  - name: with a token
    get: /auth
    headers: {Authorization: "Bearer ${TOKEN}"}
  - post: /ping
    body: '{}'
    status: [200, 204]
  - get: /missing
    status: 4xx
  - tcp: `+ln.Addr().String()+`
`), 0o644)
	os.WriteFile(filepath.Join(root, "smoke", "bad.yml"), []byte(`base: ${API}
checks:
  - get: /missing
  - get: /slow
    within: 20ms
  - get: /health
`), 0o644)
	env := map[string]string{"SWIM_ROOT": root, "API": srv.URL, "TOKEN": "tok123"}

	f := gomodtest.NewFake(t.TempDir(), env, false, "smoke/ok.yml")
	if err := Run(&f.Ctx); err != nil {
		t.Fatalf("%v\n%s", err, f.StdoutB.String())
	}
	if !strings.Contains(f.StdoutB.String(), "smoke: all 5 checks passed") || !strings.Contains(f.StdoutB.String(), "ok   with a token: HTTP 200") {
		t.Errorf("ok: %s", f.StdoutB.String())
	}

	f = gomodtest.NewFake(t.TempDir(), env, false, "smoke/bad.yml")
	err := Run(&f.Ctx)
	out := f.StdoutB.String()
	if err == nil || !strings.Contains(err.Error(), "2 of 3 checks failed") || !strings.Contains(out, "FAIL get /missing: HTTP 404, want 2xx") ||
		!strings.Contains(out, "but took") || !strings.Contains(out, "ok   get /health") {
		t.Errorf("bad: %v\n%s", err, out)
	}

	// Under swim run, only a pinned (staged) file is read.
	stage := t.TempDir()
	env["SWIM_STAGE"] = stage
	f = gomodtest.NewFake(t.TempDir(), env, false, "smoke/ok.yml")
	if err := Run(&f.Ctx); err == nil || !strings.Contains(err.Error(), "add `# Includes: smoke/ok.yml`") {
		t.Errorf("unpinned: %v", err)
	}
	os.WriteFile(filepath.Join(root, "smoke", "dup.yml"), []byte("checks:\n  - get: /a\n    post: /b\n"), 0o644)
	delete(env, "SWIM_STAGE")
	f = gomodtest.NewFake(t.TempDir(), env, false, "smoke/dup.yml")
	if err := Run(&f.Ctx); err == nil || !strings.Contains(err.Error(), "exactly one of") {
		t.Errorf("two methods: %v", err)
	}
}
