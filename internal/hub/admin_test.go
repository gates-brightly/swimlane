package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An admin user does what the admin token does, by token or by session.
func TestAdminRole(t *testing.T) {
	s, srv, worker, alice, _ := commandHub(t)
	adminTok, err := NewAdmin(srv.URL, "adm1n").AddUser("root", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, tok string) int {
		t.Helper()
		r, _ := http.NewRequest(method, srv.URL+path, nil)
		if tok != "" {
			r.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for _, path := range []string{"/api/users", "/api/grants"} {
		if c := call("GET", path, adminTok); c != 200 {
			t.Errorf("an admin user's token, GET %s: %d", path, c)
		}
		if c := call("GET", path, alice); c != http.StatusForbidden {
			t.Errorf("an operator's token, GET %s: %d", path, c)
		}
		if c := call("GET", path, ""); c != http.StatusUnauthorized {
			t.Errorf("nobody, GET %s: %d", path, c)
		}
	}
	// An admin approves a worker's key, as the admin token would.
	if _, err := worker.Enroll(Worker{Name: "billing", Host: "box", Root: "/r", Key: "age1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq"}); err != nil {
		t.Fatal(err)
	}
	if c := call("POST", "/api/workers/"+worker.ID+"/approve", adminTok); c != 200 {
		t.Errorf("an admin user approving a worker: %d", c)
	}
	if c := call("POST", "/api/workers/"+worker.ID+"/approve", alice); c != http.StatusForbidden {
		t.Errorf("an operator approving a worker: %d", c)
	}
	// And is an operator too: it may send commands.
	if !covers(RoleAdmin, RoleOperator) || !covers(RoleAdmin, RoleViewer) || covers(RoleOperator, RoleAdmin) || covers(RoleViewer, RoleOperator) {
		t.Error("covers: an admin covers operator and viewer, nothing covers admin but admin")
	}
	if _, err := s.AddUser("x", "root"); err == nil {
		t.Error("a role that isn't one was accepted")
	}
}

// --admin-tailscale: the named login's first sign-in makes it an admin
// user; a session's admin requests still need its CSRF token; a login not
// named gets nothing; and a demoted admin is made admin again.
func TestAdminTailscale(t *testing.T) {
	s, _, _, _, _ := commandHub(t)
	s.TrustTailscaleIdentity(true)
	if err := s.AdminTailscale("Zgates@bevel.work"); err != nil {
		t.Fatal(err)
	}
	if err := s.AdminTailscale("two words"); err == nil {
		t.Error("an invalid login was accepted")
	}
	s.AdminTailscale("Zgates@bevel.work")
	do := func(method, path, login, cookie, csrf, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:50000"
		if login != "" {
			r.Header.Set(TailscaleHeader, login)
		}
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
		}
		if csrf != "" {
			r.Header.Set("X-CSRF-Token", csrf)
		}
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	cookieOf := func(w *httptest.ResponseRecorder) string {
		for _, c := range w.Result().Cookies() {
			if c.Name == sessionCookie {
				return c.Value
			}
		}
		return ""
	}
	if w := do("GET", "/", "mallory@bevel.work", "", "", ""); cookieOf(w) != "" {
		t.Fatal("a login --admin-tailscale doesn't name was signed in")
	}
	sid := cookieOf(do("GET", "/", "zgates@bevel.work", "", "", ""))
	if sid == "" {
		t.Fatal("the named admin login wasn't signed in")
	}
	me := do("GET", "/api/me", "", sid, "", "").Body.String()
	if !strings.Contains(me, `"user":"zgates"`) || !strings.Contains(me, `"role":"admin"`) {
		t.Fatalf("/api/me: %s", me)
	}
	csrf := between(me, `"csrf":"`, `"`)
	if c := do("GET", "/api/users", "", sid, "", "").Code; c != 200 {
		t.Errorf("an admin session reading users: %d", c)
	}
	add := `{"name":"bob","role":"viewer"}`
	if c := do("POST", "/api/users", "", sid, "", add).Code; c != http.StatusForbidden {
		t.Errorf("an admin session's change without its CSRF token: %d", c)
	}
	if c := do("POST", "/api/users", "zgates@bevel.work", "", "", add).Code; c != http.StatusUnauthorized {
		t.Errorf("an admin change by the header alone: %d", c)
	}
	if w := do("POST", "/api/users", "", sid, csrf, add); w.Code != 200 || !strings.Contains(w.Body.String(), `"token"`) {
		t.Errorf("an admin session adding a user: %d %s", w.Code, w.Body.String())
	}
	// Signing in again makes no second user; demoted, they're admin again.
	do("GET", "/", "zgates@bevel.work", "", "", "")
	n := 0
	for _, u := range s.Users() {
		if strings.EqualFold(u.Tailscale, "zgates@bevel.work") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d users with the admin login", n)
	}
	if _, err := s.AddUserTailscale("zgates", RoleViewer, "zgates@bevel.work"); err != nil {
		t.Fatal(err)
	}
	do("GET", "/", "zgates@bevel.work", "", "", "")
	for _, u := range s.Users() {
		if u.Name == "zgates" && u.Role != RoleAdmin {
			t.Errorf("demoted to %s and not made admin again", u.Role)
		}
	}
	if got := s.freeUserName("zgates@other"); got != "zgates-2" {
		t.Errorf("a second zgates: %q", got)
	}
	if got := s.freeUserName("!!!@x"); got != "admin" {
		t.Errorf("a login with no name characters: %q", got)
	}
}
