package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTailscaleSignIn(t *testing.T) {
	s, srv, worker, _, _ := commandHub(t)
	if _, err := s.AddUserTailscale("zach", RoleOperator, "zach@github"); err != nil {
		t.Fatal(err)
	}
	// do runs a request from a peer, with the identity header, and a cookie.
	do := func(method, path, peer, login, cookie, csrf, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr = peer
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
	const local = "127.0.0.1:50000"
	// Off: the header is nobody.
	if w := do("GET", "/", local, "zach@github", "", "", ""); cookieOf(w) != "" {
		t.Fatal("signed in by the header with --trust-tailscale-identity off")
	}
	s.TrustTailscaleIdentity(true)
	// From the network (not the proxy on this machine): nobody.
	if w := do("GET", "/", "10.0.0.5:50000", "zach@github", "", "", ""); cookieOf(w) != "" {
		t.Error("signed in by a header from a non-loopback peer")
	}
	// A login no user has: nobody, and /login says so.
	if w := do("GET", "/", local, "mallory@github", "", "", ""); cookieOf(w) != "" {
		t.Error("an unknown login was signed in")
	}
	if w := do("GET", "/login", local, "mallory@github", "", "", ""); !strings.Contains(w.Body.String(), "--tailscale mallory@github") {
		t.Errorf("/login for an unknown login:\n%s", w.Body.String())
	}
	// zach's login: a session, as zach, the same as a token's.
	w := do("GET", "/", local, "ZACH@github", "", "", "")
	sid := cookieOf(w)
	if sid == "" || w.Code != 200 {
		t.Fatalf("not signed in: %d", w.Code)
	}
	me := do("GET", "/api/me", local, "", sid, "", "")
	if !strings.Contains(me.Body.String(), `"user":"zach"`) {
		t.Fatalf("/api/me: %s", me.Body.String())
	}
	csrf := between(me.Body.String(), `"csrf":"`, `"`)
	if w := do("GET", "/login", local, "zach@github", "", "", ""); w.Code != http.StatusSeeOther || cookieOf(w) == "" {
		t.Errorf("/login with a known login: %d", w.Code)
	}
	// The header alone authorizes nothing: commands take the session and its CSRF token.
	cmds := "/api/workers/" + worker.ID + "/commands"
	if w := do("POST", cmds, local, "zach@github", "", "", `{"kind":"drain"}`); w.Code != http.StatusUnauthorized {
		t.Errorf("a command by the header alone: %d", w.Code)
	}
	if w := do("POST", cmds, local, "zach@github", sid, "", `{"kind":"drain"}`); w.Code != http.StatusForbidden {
		t.Errorf("a command without the CSRF token: %d", w.Code)
	}
	if w := do("POST", cmds, local, "", sid, csrf, `{"kind":"drain"}`); w.Code != http.StatusCreated {
		t.Errorf("a command with the session: %d %s", w.Code, w.Body.String())
	}
	// Never an approver: no button answers; a signed one gets past this check.
	w = do("POST", cmds, local, "", sid, csrf, `{"kind":"answer","pin":{"request":"q1"},"answer":{"decision":"approve"}}`)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "Tailscale") {
		t.Errorf("a button answer by a Tailscale login: %d %s", w.Code, w.Body.String())
	}
	w = do("POST", cmds, local, "", sid, csrf, `{"kind":"answer","pin":{"request":"q1"},"answer":{"decision":"approve","key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA x","sig":"U1NIU0lHAAAAAQAAADMAAAAL"}}`)
	if strings.Contains(w.Body.String(), "Tailscale") {
		t.Errorf("a signed answer was refused as a Tailscale login's: %d %s", w.Code, w.Body.String())
	}
	// A private hub lets them in.
	s.Private(true)
	if w := do("GET", "/e/x/runs", local, "zach@github", "", "", ""); w.Code != 200 || cookieOf(w) == "" {
		t.Errorf("private hub, Tailscale login: %d", w.Code)
	}
	if w := do("GET", "/e/x/runs", local, "", "", "", ""); w.Code != http.StatusSeeOther {
		t.Errorf("private hub, nobody: %d", w.Code)
	}
	// Logins are checked, and each is one user's.
	if _, err := s.AddUserTailscale("alice2", RoleViewer, "zach@github"); err == nil || !strings.Contains(err.Error(), "zach's already") {
		t.Errorf("a taken login: %v", err)
	}
	if _, err := s.AddUserTailscale("alice2", RoleViewer, "has space"); err == nil {
		t.Error("a login with a space")
	}
	if us := s.Users(); !strings.Contains(strings.Join(func() []string {
		var o []string
		for _, u := range us {
			o = append(o, u.Name+"="+u.Tailscale)
		}
		return o
	}(), " "), "zach=zach@github") {
		t.Errorf("users: %+v", us)
	}
	_ = srv
}

// between is the text in s after the first from, up to to.
func between(s, from, to string) string {
	_, rest, ok := strings.Cut(s, from)
	if !ok {
		return ""
	}
	out, _, _ := strings.Cut(rest, to)
	return out
}
