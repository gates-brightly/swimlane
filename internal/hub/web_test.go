package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

func TestAppRoutes(t *testing.T) {
	s, srv, worker, _, vic := commandHub(t)
	worker.Report(Snapshot{Worker: Worker{Name: "billing", Host: "box", Root: "/r"}, Activity: "idle"})
	page := func(path string) (*http.Response, string) {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	// The app's routes all get index.html.
	for _, p := range []string{"/", "/approvals", "/e/" + worker.ID + "/runs", "/e/x/runs/r-1/jobs/2", "/anything/else"} {
		resp, body := page(p)
		if resp.StatusCode != 200 || !strings.Contains(body, `<script type="module" src="/assets/app.js">`) {
			t.Errorf("%s: %d\n%s", p, resp.StatusCode, body)
		}
	}
	// Assets, with ETags and their types; nothing else under /assets/.
	for p, typ := range map[string]string{"/assets/app.js": "javascript", "/assets/app.css": "text/css",
		"/assets/vendor/preact-htm.module.js": "javascript", "/assets/favicon.svg": "image/svg+xml", "/assets/login.css": "text/css"} {
		resp, _ := page(p)
		if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), typ) || resp.Header.Get("ETag") == "" {
			t.Errorf("%s: %d %s etag %q", p, resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("ETag"))
		}
		req, _ := http.NewRequest("GET", srv.URL+p, nil)
		req.Header.Set("If-None-Match", resp.Header.Get("ETag"))
		if r2, err := http.DefaultClient.Do(req); err != nil || r2.StatusCode != http.StatusNotModified {
			t.Errorf("%s again: %v %v", p, r2.StatusCode, err)
		}
	}
	for _, p := range []string{"/assets/index.html", "/assets/nope.js", "/assets/lib.test.js", "/api/nope"} {
		if resp, body := page(p); resp.StatusCode != 404 || strings.Contains(body, "<script") {
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
	}
	// No source leaves through a path that climbs out of web/.
	for _, p := range []string{"/assets/../hub.go", "/assets/%2e%2e/hub.go", "/assets/vendor/../../web.go"} {
		if _, body := page(p); strings.Contains(body, "package hub") {
			t.Errorf("%s served Go source", p)
		}
	}
	// The old pages are gone: a worker page's link goes to its lanes.
	nofollow0 := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if resp, err := nofollow0.Get(srv.URL + "/w/" + worker.ID); err != nil || resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != "/e/"+worker.ID+"/lanes" {
		t.Errorf("/w/ID: %v %v", resp, err)
	}
	if _, body := page("/login"); !strings.Contains(body, `href="/assets/login.css"`) {
		t.Errorf("login page:\n%s", body)
	}
	// Who's logged in.
	if code, body := call(t, srv.URL+"/api/me", ""); code != 200 || !strings.Contains(body, `"user":""`) {
		t.Errorf("/api/me, nobody: %d %s", code, body)
	}
	if code, body := call(t, srv.URL+"/api/me", vic); code != 200 || !strings.Contains(body, `"user":""`) {
		t.Errorf("/api/me with a bearer token (sessions only): %d %s", code, body)
	}
	// A private hub's app needs a login.
	s.Private(true)
	nofollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if resp, err := nofollow.Get(srv.URL + "/e/x/runs"); err != nil || resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Errorf("private app: %v %v", resp, err)
	}
	if resp, _ := page("/assets/app.js"); resp.StatusCode != 200 {
		t.Errorf("assets on a private hub: %d", resp.StatusCode)
	}
}

// Every response carries the CSP, and no page the hub serves needs what it
// refuses: inline script, inline style or event handler attributes.
func TestCSPEverywhere(t *testing.T) {
	_, srv, worker, alice, _ := commandHub(t)
	worker.Report(Snapshot{Worker: Worker{Name: "billing", Host: "box", Root: "/r"}, Activity: "running",
		Headless: &HeadlessInfo{Mode: "systemd", Allow: []string{"interrupt", "drain", "log", "run", "trash"}},
		Lanes:    []Lane{{Lane: 1, State: "running", Run: "r-9", Progress: "3/9"}, {Lane: 2, State: "passed", Script: strings.Repeat("c", 64), TrashJob: "j"}},
		Waiting:  []PendingApproval{{ID: "q1", Lane: 1, Flag: "GO", Desc: "go", Expires: "2099-01-01T00:00:00Z"}}})
	u, _ := NewUser(srv.URL, alice, nil)
	u.Send(worker.ID, Command{Kind: KindLog, Lane: 1, Pin: Pin{Run: "r-9"}})
	jar := loginJar(t, srv.URL, alice)
	inline := regexp.MustCompile(`(?i)<style|\sstyle=|\son[a-z]+=|<script(?:\s[^>]*)?>[^<]|javascript:`)
	for _, p := range []string{"/", "/e/x/runs", "/e/" + worker.ID + "/questions/q1", "/login", "/api/enrollments", "/api/enrollments/" + worker.ID + "/questions/q1",
		"/api/enrollments/" + worker.ID + "/commands", "/api/health", "/assets/app.js", "/api/nope"} {
		resp, err := jar.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.Header.Get("Content-Security-Policy") != CSP || resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s: headers %v", p, resp.Header)
		}
		if strings.Contains(resp.Header.Get("Content-Type"), "html") {
			if m := inline.FindString(string(b)); m != "" {
				t.Errorf("%s has %q, which the CSP refuses", p, m)
			}
		}
	}
}

// The app's own scripts never build HTML from strings.
func TestAppScriptsBuildNoHTML(t *testing.T) {
	bad := regexp.MustCompile(`innerHTML|outerHTML|insertAdjacentHTML|document\.write|\beval\(|new Function|dangerouslySetInnerHTML`)
	fs.WalkDir(webFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".js") || strings.HasPrefix(p, "vendor/") {
			return err
		}
		data, _ := fs.ReadFile(webFS, p)
		if m := bad.FindString(string(data)); m != "" {
			t.Errorf("%s uses %s", p, m)
		}
		return nil
	})
}

func TestVendoredHash(t *testing.T) {
	data, err := fs.ReadFile(webFS, "vendor/preact-htm.module.js")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	readme, _ := fs.ReadFile(webFS, "vendor/README.md")
	if !strings.Contains(string(readme), hex.EncodeToString(sum[:])) {
		t.Errorf("vendor/preact-htm.module.js (sha256 %x) isn't the file vendor/README.md names", sum)
	}
}

// lib.js's own tests, when node is here.
func TestAppLib(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH")
	}
	cmd := exec.Command(node, "--test", "lib.test.js")
	cmd.Dir = "web"
	cmd.Env = append(os.Environ(), "NODE_OPTIONS=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("node --test web/lib.test.js: %v\n%s", err, out)
	}
}

// loginJar is a browser logged in as token.
func loginJar(t *testing.T, base, token string) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	resp, err := c.PostForm(base+"/api/login", map[string][]string{"token": {token}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return c
}
