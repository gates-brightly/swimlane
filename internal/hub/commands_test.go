package hub

import (
	"bytes"
	"context"
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

// commandHub is a hub with state, an enrolled worker, an operator (alice)
// and a viewer (vic).
func commandHub(t *testing.T) (s *Server, srv *httptest.Server, worker *Client, alice, vic string) {
	t.Helper()
	s, err := NewWithState("sekret", "adm1n", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv = httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	worker = NewClient(srv.URL, "sekret", "")
	if _, err := worker.Enroll(Worker{Name: "billing", Host: "box", Root: "/r"}); err != nil {
		t.Fatal(err)
	}
	admin := NewAdmin(srv.URL, "adm1n")
	if alice, err = admin.AddUser("alice", RoleOperator); err != nil {
		t.Fatal(err)
	}
	if vic, err = admin.AddUser("vic", RoleViewer); err != nil {
		t.Fatal(err)
	}
	return s, srv, worker, alice, vic
}

func TestCommandLifecycle(t *testing.T) {
	s, srv, worker, alice, _ := commandHub(t)
	u, _ := NewUser(srv.URL, alice, nil)
	ctx := context.Background()

	epoch, cmds, err := worker.Poll(ctx, 0, "", 0)
	if err != nil || len(cmds) != 0 || epoch == "" {
		t.Fatalf("empty poll: %q %v %v", epoch, cmds, err)
	}
	// A long poll returns as soon as a command is sent.
	got := make(chan []Command, 1)
	go func() {
		_, cmds, _ := worker.Poll(ctx, 0, epoch, 5*time.Second)
		got <- cmds
	}()
	time.Sleep(100 * time.Millisecond)
	sent, err := u.Send(worker.ID, Command{Kind: KindInterrupt, Lane: 2, Pin: Pin{Run: "r-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if sent.Seq != 1 || sent.By != "alice" || sent.Epoch != epoch {
		t.Errorf("sent: %+v", sent)
	}
	at, _ := time.Parse(time.RFC3339, sent.At)
	exp, _ := time.Parse(time.RFC3339, sent.Expires)
	if exp.Sub(at) != InterruptTTL {
		t.Errorf("an interrupt lives %s", exp.Sub(at))
	}
	select {
	case cmds := <-got:
		if len(cmds) != 1 || cmds[0].CID != sent.CID {
			t.Fatalf("long poll: %+v", cmds)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the long poll didn't wake")
	}
	// Seen past its seq: nothing; another epoch: every unacked one again.
	if _, cmds, _ := worker.Poll(ctx, 1, epoch, 0); len(cmds) != 0 {
		t.Errorf("after seq 1: %+v", cmds)
	}
	if _, cmds, _ := worker.Poll(ctx, 1, "old-epoch", 0); len(cmds) != 1 {
		t.Errorf("another epoch: %+v", cmds)
	}
	if err := worker.Ack(sent.CID, OutcomeRefused, "stale: swim 2 is now run r-2\x1b[31m"); err != nil {
		t.Fatal(err)
	}
	if err := worker.Ack(sent.CID, OutcomeDone, ""); err == nil || !strings.Contains(err.Error(), "409") {
		t.Errorf("a second, different ack: %v", err)
	}
	if err := worker.Ack("nope", OutcomeDone, ""); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("unknown cid: %v", err)
	}
	if _, cmds, _ := worker.Poll(ctx, 0, epoch, 0); len(cmds) != 0 {
		t.Errorf("acked commands aren't handed out again: %+v", cmds)
	}
	c := s.Commands(worker.ID)[0]
	if c.Outcome != OutcomeRefused || c.Detail != "stale: swim 2 is now run r-2" {
		t.Errorf("ack: %+v", c)
	}
	// Another worker's token, or the enroll token, can't poll or ack.
	other := NewClient(srv.URL, "sekret", "")
	other.Enroll(Worker{Name: "other", Host: "box", Root: "/o"})
	other.ID = worker.ID
	if _, _, err := other.Poll(ctx, 0, "", 0); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("another worker polled: %v", err)
	}

	// Expiry: unacked past its expiry, the page says so.
	run, _ := u.Send(worker.ID, Command{Kind: KindRun, Lane: 3, Pin: Pin{SHA256: strings.Repeat("a", 64)}})
	s.now = func() time.Time { return time.Now().Add(11 * time.Minute) }
	if st := s.Commands(worker.ID)[0].State(s.now()); st != "expired (never picked up)" {
		t.Errorf("state: %q", st)
	}
	worker.Ack(run.CID, OutcomeExpired, "sent 11m ago; headless.command_ttl is 10m")
	if st := s.Commands(worker.ID)[0].State(s.now()); st != OutcomeExpired {
		t.Errorf("state: %q", st)
	}

	// Bad commands.
	for _, c := range []Command{{Kind: "deploy"}, {Kind: KindRun, Lane: 1}, {Kind: KindInterrupt, Lane: 1}, {Kind: "answer"},
		{Kind: KindLog, Lane: 1, Pin: Pin{Run: "r 1;<b>"}}} {
		if _, err := u.Send(worker.ID, c); err == nil || !strings.Contains(err.Error(), "400") {
			t.Errorf("%+v: %v", c, err)
		}
	}
	if _, err := u.Send("nobody", Command{Kind: KindDrain}); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("unknown worker: %v", err)
	}

	// Durable: a restarted hub has them, and the same epoch.
	s2, err := NewWithState("sekret", "adm1n", s.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.Commands(worker.ID)) != 2 || s2.epoch != s.epoch || s2.nextSeq[worker.ID] != 2 {
		t.Errorf("after a restart: %d commands, epoch %q (was %q)", len(s2.Commands(worker.ID)), s2.epoch, s.epoch)
	}
}

func TestCommandRoles(t *testing.T) {
	_, srv, worker, alice, vic := commandHub(t)
	v, _ := NewUser(srv.URL, vic, nil)
	for _, kind := range []string{KindInterrupt, KindDrain, KindRun} {
		c := Command{Kind: kind, Lane: 1, Pin: Pin{Run: "r-1", SHA256: strings.Repeat("b", 64)}}
		if _, err := v.Send(worker.ID, c); err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "operator role") {
			t.Errorf("a viewer sent %s: %v", kind, err)
		}
	}
	if _, err := v.Send(worker.ID, Command{Kind: KindLog, Lane: 1, Pin: Pin{Run: "r-1"}}); err != nil {
		t.Errorf("a viewer asks for logs: %v", err)
	}
	// The admin token manages users, and sends nothing.
	adm, _ := NewUser(srv.URL, "adm1n", nil)
	if _, err := adm.Send(worker.ID, Command{Kind: KindDrain}); err == nil || !strings.Contains(err.Error(), "manages users only") {
		t.Errorf("the admin sent a command: %v", err)
	}
	// Nor does a user manage users, or the worker's token send commands.
	if _, err := NewAdmin(srv.URL, alice).AddUser("mallory", RoleOperator); err == nil {
		t.Error("a user added a user")
	}
	wk, _ := NewUser(srv.URL, worker.WorkerToken, nil)
	if _, err := wk.Send(worker.ID, Command{Kind: KindDrain}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("the worker's token sent a command: %v", err)
	}
	if _, err := NewAdmin(srv.URL, "adm1n").AddUser("bad name", RoleViewer); err == nil {
		t.Error("a name with a space")
	}
	if _, err := NewAdmin(srv.URL, "adm1n").AddUser("bob", "root"); err == nil {
		t.Error("an unknown role")
	}
	// Removed: its token stops working.
	if err := NewAdmin(srv.URL, "adm1n").RemoveUser("vic"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Send(worker.ID, Command{Kind: KindLog, Lane: 1, Pin: Pin{Run: "r-1"}}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("a removed user: %v", err)
	}
	us, _ := NewAdmin(srv.URL, "adm1n").Users()
	if len(us) != 1 || us[0].Name != "alice" || us[0].Role != RoleOperator {
		t.Errorf("users: %+v", us)
	}
}

// A browser logs in for a session cookie (HttpOnly, SameSite=Strict); its
// form POSTs need the session's CSRF token.
func TestLoginAndCSRF(t *testing.T) {
	s, srv, worker, alice, _ := commandHub(t)
	jar, _ := cookiejar.New(nil)
	br := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	post := func(path string, form url.Values) *http.Response {
		resp, err := br.PostForm(srv.URL+path, form)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if resp := post("/api/login", url.Values{"token": {"wrong"}}); resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "failed") {
		t.Errorf("a bad token: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp := post("/api/login", url.Values{"token": {alice}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	ck := resp.Header.Get("Set-Cookie")
	for _, want := range []string{"swim_session=", "HttpOnly", "SameSite=Strict", "Max-Age=43200"} {
		if !strings.Contains(ck, want) {
			t.Errorf("cookie lacks %s: %s", want, ck)
		}
	}
	if strings.Contains(ck, "Secure") {
		t.Errorf("Secure on a plain-HTTP hub: %s", ck)
	}
	// The app reads who's logged in, and the session's CSRF token.
	worker.Report(Snapshot{Worker: Worker{Name: "billing", Host: "box", Root: "/r"}, Activity: "running",
		Headless: &HeadlessInfo{Mode: "systemd", Allow: []string{"interrupt", "drain", "log"}},
		Lanes:    []Lane{{Lane: 1, State: "running", Run: "r-9"}}})
	var me struct{ User, Role, CSRF string }
	json.Unmarshal([]byte(getWith(t, br, srv.URL+"/api/me")), &me)
	csrf := me.CSRF
	if me.User != "alice" || me.Role != RoleOperator || csrf == "" {
		t.Fatalf("/api/me: %+v", me)
	}
	// Without the token, or with another, a form POST is refused.
	for _, bad := range []string{"", "0000"} {
		f := url.Values{"kind": {"drain"}}
		if bad != "" {
			f.Set("csrf", bad)
		}
		if resp := post("/api/workers/"+worker.ID+"/commands", f); resp.StatusCode != http.StatusForbidden {
			t.Errorf("csrf %q: %d", bad, resp.StatusCode)
		}
	}
	resp = post("/api/workers/"+worker.ID+"/commands", url.Values{"kind": {"interrupt"}, "lane": {"1"}, "run": {"r-9"}, "csrf": {csrf}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/e/"+worker.ID+"/lanes" || len(s.Commands(worker.ID)) != 1 {
		t.Fatalf("with the token: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if c := s.Commands(worker.ID)[0]; c.Kind != KindInterrupt || c.By != "alice" || c.Pin.Run != "r-9" {
		t.Errorf("command: %+v", c)
	}
	// A JSON POST with the cookie needs the header instead.
	req, _ := http.NewRequest("POST", srv.URL+"/api/workers/"+worker.ID+"/commands", strings.NewReader(`{"kind":"drain"}`))
	req.Header.Set("Content-Type", "application/json")
	if resp, _ := br.Do(req); resp.StatusCode != http.StatusForbidden {
		t.Errorf("cookie JSON without X-CSRF-Token: %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("POST", srv.URL+"/api/workers/"+worker.ID+"/commands", strings.NewReader(`{"kind":"drain"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf)
	if resp, _ := br.Do(req); resp.StatusCode != http.StatusCreated {
		t.Errorf("cookie JSON with X-CSRF-Token: %d", resp.StatusCode)
	}
	// Logged out (or never in): no controls, and a POST is refused.
	post("/logout", url.Values{"csrf": {csrf}})
	if body := getWith(t, br, srv.URL+"/api/me"); !strings.Contains(body, `"user":""`) {
		t.Errorf("/api/me after logout: %s", body)
	}
	if resp := post("/api/workers/"+worker.ID+"/commands", url.Values{"kind": {"drain"}, "csrf": {csrf}}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("after logout: %d", resp.StatusCode)
	}
	// Under TLS the cookie is Secure.
	s.SecureCookies(true)
	resp = post("/api/login", url.Values{"token": {alice}})
	if !strings.Contains(resp.Header.Get("Set-Cookie"), "Secure") {
		t.Errorf("TLS cookie: %s", resp.Header.Get("Set-Cookie"))
	}
	// Sessions end after 12 hours.
	s.now = func() time.Time { return time.Now().Add(SessionTTL + time.Minute) }
	if page := getWith(t, br, srv.URL+"/w/"+worker.ID); strings.Contains(page, "signed in as") {
		t.Error("a session outlived 12h")
	}
}

func getWith(t *testing.T, c *http.Client, u string) string {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b bytes.Buffer
	b.ReadFrom(resp.Body)
	return b.String()
}

// Logs: only one asked for, at most 8 MiB, made safe; read by users only.
func TestLogUpload(t *testing.T) {
	_, srv, worker, _, vic := commandHub(t)
	v, _ := NewUser(srv.URL, vic, nil)
	if err := worker.UploadLog("r-1", 2, []byte("unasked")); err == nil || !strings.Contains(err.Error(), "409") {
		t.Errorf("an unasked log: %v", err)
	}
	v.Send(worker.ID, Command{Kind: KindLog, Lane: 2, Pin: Pin{Run: "r-1"}})
	if err := worker.UploadLog("r-1", 2, bytes.Repeat([]byte("x"), LogMax+1)); err == nil || !strings.Contains(err.Error(), "413") {
		t.Errorf("over 8 MiB: %v", err)
	}
	if _, err := v.Log(worker.ID, "r-1", 2); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("before upload: %v", err)
	}
	if err := worker.UploadLog("r-1", 2, []byte("== ROUND\n\x1b]0;pwned\x07step ok\n")); err != nil {
		t.Fatal(err)
	}
	got, err := v.Log(worker.ID, "r-1", 2)
	if err != nil || got != "== ROUND\nstep ok\n" {
		t.Errorf("log: %q %v", got, err)
	}
	resp, _ := http.Get(srv.URL + "/api/enrollments/" + worker.ID + "/runs/r-1/jobs/2/log")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous read: %d", resp.StatusCode)
	}

}

func TestLogCacheBounds(t *testing.T) {
	now := time.Now()
	c := newLogCache(10, 25, time.Minute, func() time.Time { return now })
	if c.put("big", make([]byte, 11)) {
		t.Error("over the per-log limit")
	}
	c.put("a", make([]byte, 10))
	c.put("b", make([]byte, 10))
	c.get("a") // a is now the most recently used
	c.put("c", make([]byte, 10))
	if _, _, ok := c.get("b"); ok {
		t.Error("b, least recently used, should have gone")
	}
	if _, _, ok := c.get("a"); !ok {
		t.Error("a was read since; it stays")
	}
	if c.size() != 20 {
		t.Errorf("size %d", c.size())
	}
	now = now.Add(2 * time.Minute)
	if _, _, ok := c.get("a"); ok {
		t.Error("past its TTL")
	}
	c.put("d", make([]byte, 5))
	if c.size() != 5 {
		t.Errorf("expired logs weren't dropped: size %d", c.size())
	}
}

// A v1 state file (before users and commands) loads, and is saved as v2.
func TestStateV1Migrates(t *testing.T) {
	dir := t.TempDir()
	v1 := `{"schema":"swim.hub-state/v1","workers":{"abc":{"id":"abc","name":"old","host":"h","root":"/r","approved":true,"token_hash":"x"}},"grants":{}}`
	os.WriteFile(filepath.Join(dir, "state.json"), []byte(v1), 0o600)
	s, err := NewWithState("t", "a", dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Workers()) != 1 || !s.Workers()[0].Approved {
		t.Errorf("workers: %+v", s.Workers())
	}
	var st state
	data, _ := os.ReadFile(filepath.Join(dir, "state.json"))
	json.Unmarshal(data, &st)
	if st.Schema != StateSchema || st.Epoch == "" || st.Workers["abc"] == nil {
		t.Errorf("saved: %s", data)
	}
	os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"schema":"swim.hub-state/v9"}`), 0o600)
	if _, err := NewWithState("t", "a", dir); err == nil {
		t.Error("an unknown schema loaded")
	}
}
