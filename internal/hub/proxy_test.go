package hub

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrivateHub(t *testing.T) {
	s, srv, worker, _, vic := commandHub(t)
	s.Private(true)
	if err := worker.Report(Snapshot{Worker: Worker{Name: "billing", Host: "box", Root: "/r"}, Activity: "idle"}); err != nil {
		t.Fatalf("a worker reports to a private hub: %v", err)
	}
	jar, _ := cookiejar.New(nil)
	br := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path, token string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := br.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	for _, p := range []string{"/", "/e/" + worker.ID + "/lanes"} {
		if resp := get(p, ""); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
			t.Errorf("%s with no login: %d %s", p, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	for tok, want := range map[string]int{"": 401, "wrong": 401, "sekret": 401, vic: 200, "adm1n": 200} {
		if resp := get("/api/enrollments", tok); resp.StatusCode != want {
			t.Errorf("/api/enrollments with token %q: %d, want %d", tok, resp.StatusCode, want)
		}
	}
	if resp := get("/login", ""); resp.StatusCode != http.StatusOK {
		t.Errorf("/login on a private hub: %d", resp.StatusCode)
	}
	resp, err := br.PostForm(srv.URL+"/api/login", url.Values{"token": {vic}})
	if err != nil || resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login: %v %v", resp, err)
	}
	resp.Body.Close()
	for _, p := range []string{"/", "/e/" + worker.ID + "/lanes", "/api/enrollments", "/api/enrollments/" + worker.ID} {
		if resp := get(p, ""); resp.StatusCode != http.StatusOK {
			t.Errorf("%s logged in: %d", p, resp.StatusCode)
		}
	}
	// Not private: viewing needs nothing, as before.
	s.Private(false)
	if resp := get("/api/enrollments", "wrong"); resp.StatusCode != http.StatusOK {
		t.Errorf("/api/enrollments on an open hub: %d", resp.StatusCode)
	}
}

func TestClientAddr(t *testing.T) {
	s := New("t")
	req := func(peer string, xff ...string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = peer
		for _, x := range xff {
			r.Header.Add("X-Forwarded-For", x)
		}
		return r
	}
	if got := s.clientAddr(req("127.0.0.1:5000", "100.64.0.5")); got != "127.0.0.1:5000" {
		t.Errorf("not behind a proxy, X-Forwarded-For is ignored: %s", got)
	}
	s.BehindProxy(true)
	for _, c := range []struct {
		r    *http.Request
		want string
	}{
		{req("127.0.0.1:5000", "100.64.0.5"), "100.64.0.5"},
		{req("[::1]:5000", "fd7a:115c::1"), "fd7a:115c::1"},
		// The client's own header comes first; the proxy's address is last.
		{req("127.0.0.1:5000", "6.6.6.6, 100.64.0.5"), "100.64.0.5"},
		{req("127.0.0.1:5000", "6.6.6.6", "100.64.0.5"), "100.64.0.5"},
		// From the network, not the proxy: the peer.
		{req("10.0.0.9:5000", "100.64.0.5"), "10.0.0.9:5000"},
		{req("127.0.0.1:5000"), "127.0.0.1:5000"},
		{req("127.0.0.1:5000", "not-an-ip\x1b[31m"), "127.0.0.1:5000"},
	} {
		if got := s.clientAddr(c.r); got != c.want {
			t.Errorf("%s %q: %s, want %s", c.r.RemoteAddr, c.r.Header.Values("X-Forwarded-For"), got, c.want)
		}
	}
}

func TestLoginAuditNamesForwardedClient(t *testing.T) {
	s, _, _, _, vic := commandHub(t)
	s.BehindProxy(true)
	s.SecureCookies(true)
	r := httptest.NewRequest("POST", "/api/login", strings.NewReader(url.Values{"token": {vic}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "127.0.0.1:41000"
	r.Header.Set("X-Forwarded-For", "100.64.0.5")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if ck := w.Header().Get("Set-Cookie"); !strings.Contains(ck, "Secure") {
		t.Errorf("cookie not Secure behind an https proxy: %s", ck)
	}
	data, _ := os.ReadFile(filepath.Join(s.dir, "audit.log"))
	if !strings.Contains(string(data), "vic logged in (viewer) from 100.64.0.5") {
		t.Errorf("audit log:\n%s", data)
	}
}

// A login outlives a restart; the state holds only its id's hash, and
// a logout or an expiry still ends it.
func TestSessionsSurviveRestart(t *testing.T) {
	s, srv, _, alice, _ := commandHub(t)
	jar, _ := cookiejar.New(nil)
	br := &http.Client{Jar: jar}
	resp, err := br.PostForm(srv.URL+"/api/login", url.Values{"token": {alice}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	u, _ := url.Parse(srv.URL)
	sid := jar.Cookies(u)[0].Value
	saved, _ := os.ReadFile(s.statePath())
	if strings.Contains(string(saved), sid) || !strings.Contains(string(saved), hashToken(sid)) {
		t.Fatalf("the state should hold the session's hash, not its id:\n%s", saved)
	}
	restart := func() *httptest.Server {
		t.Helper()
		s2, err := NewWithState("sekret", "adm1n", s.dir)
		if err != nil {
			t.Fatal(err)
		}
		srv2 := httptest.NewServer(s2.Handler())
		t.Cleanup(srv2.Close)
		jar.SetCookies(mustURL(srv2.URL), []*http.Cookie{{Name: sessionCookie, Value: sid, Path: "/"}})
		return srv2
	}
	me := func(base string) string { return getWith(t, br, base+"/api/me") }
	srv2 := restart()
	if body := me(srv2.URL); !strings.Contains(body, `"user":"alice"`) {
		t.Fatalf("after a restart: %s", body)
	}
	// Expired in the file: not loaded.
	data, _ := os.ReadFile(s.statePath())
	var st state
	json.Unmarshal(data, &st)
	st.Sessions[hashToken(sid)].Expires = time.Now().Add(-time.Minute)
	data, _ = json.Marshal(st)
	os.WriteFile(s.statePath(), data, 0o600)
	if body := me(restart().URL); !strings.Contains(body, `"user":""`) {
		t.Errorf("an expired session came back: %s", body)
	}
	// Logged out: gone after a restart too.
	st.Sessions[hashToken(sid)].Expires = time.Now().Add(time.Hour)
	data, _ = json.Marshal(st)
	os.WriteFile(s.statePath(), data, 0o600)
	srv3 := restart()
	var csrf struct{ CSRF string }
	json.Unmarshal([]byte(me(srv3.URL)), &csrf)
	req, _ := http.NewRequest("POST", srv3.URL+"/logout", nil)
	req.Header.Set("X-CSRF-Token", csrf.CSRF)
	if resp, err := br.Do(req); err != nil || resp.StatusCode != 200 {
		t.Fatalf("logout: %v %v", resp, err)
	}
	if body := me(restart().URL); !strings.Contains(body, `"user":""`) {
		t.Errorf("a logged-out session came back: %s", body)
	}
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}
