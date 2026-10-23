package hub

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/version"
)

// call is a GET as token ("" for nobody): its status and body.
func call(t *testing.T, url, token string, header ...string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestReadAPI(t *testing.T) {
	s, srv, worker, _, vic := commandHub(t)
	worker.Report(Snapshot{Worker: Worker{Name: "billing", Host: "box", Root: "/r", Namespace: "github.com/org/billing"}, Activity: "idle"})
	now := s.now()
	for i, id := range []string{"r-1", "r-2", "r-3"} {
		rep := testRun(id, now.Add(time.Duration(i-3)*time.Hour), "passed", "failed")
		rep.Jobs[1].Failed = []string{"FAIL  deploy"}
		if _, err := worker.PutRun(rep); err != nil {
			t.Fatal(err)
		}
	}
	api := srv.URL + "/api/enrollments/" + worker.ID

	if code, body := call(t, srv.URL+"/api/health", ""); code != 200 || !strings.Contains(body, `"ok":true`) {
		t.Errorf("health: %d %s", code, body)
	}
	var list struct {
		Schema      string
		Namespaces  []Tab
		Enrollments []View
	}
	_, body := call(t, srv.URL+"/api/enrollments", "")
	json.Unmarshal([]byte(body), &list)
	if list.Schema != APISchema || len(list.Enrollments) != 1 || len(list.Namespaces) != 2 || list.Namespaces[1].Key != "github.com/org/billing" {
		t.Errorf("enrollments: %s", body)
	}
	if code, body := call(t, api, ""); code != 200 || !strings.Contains(body, `"name":"billing"`) {
		t.Errorf("enrollment: %d %s", code, body)
	}
	if code, _ := call(t, srv.URL+"/api/enrollments/nope", ""); code != 404 {
		t.Errorf("unknown enrollment: %d", code)
	}

	// Runs, a page at a time, newest first.
	var page struct {
		Runs []RunInfo
		Next string
	}
	_, body = call(t, api+"/runs?limit=2", "")
	json.Unmarshal([]byte(body), &page)
	if names(page.Runs) != "r-3 r-2" || page.Next != "r-2" {
		t.Fatalf("first page: %s", body)
	}
	_, body = call(t, api+"/runs?limit=2&before="+page.Next, "")
	page.Next = "x"
	json.Unmarshal([]byte(body), &page)
	if names(page.Runs) != "r-1" || page.Next != "" {
		t.Errorf("second page: %s", body)
	}

	// A run: its record and each job's summary, never step output.
	code, body := call(t, api+"/runs/r-2", "")
	if code != 200 || !strings.Contains(body, `"result":"failed"`) || !strings.Contains(body, `"failed_steps":["FAIL  deploy"]`) || strings.Contains(body, "there") {
		t.Errorf("run: %d %s", code, body)
	}
	if code, _ := call(t, api+"/runs/r-9", ""); code != 404 {
		t.Errorf("unknown run: %d", code)
	}

	// A job: its round, steps and output, for a user only.
	if code, _ := call(t, api+"/runs/r-2/jobs/1", ""); code != 401 {
		t.Errorf("anonymous job: %d", code)
	}
	if code, body := call(t, api+"/runs/r-2/jobs/1", vic); code != 200 || !strings.Contains(body, `"label":"say"`) || !strings.Contains(body, `"output":["hi there"]`) {
		t.Errorf("job: %d %s", code, body)
	}
	for _, p := range []string{"/runs/r-2/jobs/9", "/runs/r-2/jobs/x", "/runs/r-9/jobs/1"} {
		if code, _ := call(t, api+p, vic); code != 404 {
			t.Errorf("%s: %d", p, code)
		}
	}

	// A job's log: once a log command had it uploaded; Range works.
	if code, _ := call(t, api+"/runs/r-2/jobs/1/log", ""); code != 401 {
		t.Errorf("anonymous log: %d", code)
	}
	if code, _ := call(t, api+"/runs/r-2/jobs/1/log", vic); code != 404 {
		t.Errorf("log not uploaded: %d", code)
	}
	v, _ := NewUser(srv.URL, vic, nil)
	v.Send(worker.ID, Command{Kind: KindLog, Lane: 1, Pin: Pin{Run: "r-2"}})
	if err := worker.UploadLog("r-2", 1, []byte("line one\nline two\n")); err != nil {
		t.Fatal(err)
	}
	if code, body := call(t, api+"/runs/r-2/jobs/1/log", vic); code != 200 || body != "line one\nline two\n" {
		t.Errorf("log: %d %q", code, body)
	}
	if code, body := call(t, api+"/runs/r-2/jobs/1/log", vic, "Range", "bytes=5-7"); code != http.StatusPartialContent || body != "one" {
		t.Errorf("log range: %d %q", code, body)
	}

	// Private: viewing needs a user; health doesn't.
	s.Private(true)
	for _, p := range []string{"", "/runs", "/runs/r-2"} {
		if code, _ := call(t, api+p, ""); code != 401 {
			t.Errorf("private %s: %d", p, code)
		}
		if code, _ := call(t, api+p, vic); code != 200 {
			t.Errorf("private %s as a user: %d", p, code)
		}
	}
	if code, _ := call(t, srv.URL+"/api/health", ""); code != 200 {
		t.Errorf("private health: %d", code)
	}
	if code, _ := call(t, srv.URL+"/api/workers", "adm1n"); code == 200 {
		t.Error("GET /api/workers still answers")
	}
}

// stream reads server-sent events: each event's kind and data.
type stream struct {
	resp *http.Response
	sc   *bufio.Scanner
}

func openStream(t *testing.T, url string) *stream {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("events: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	t.Cleanup(func() { resp.Body.Close() })
	return &stream{resp: resp, sc: bufio.NewScanner(resp.Body)}
}

// next is the next event, or a comment as kind ":" ("" at the end).
func (st *stream) next(t *testing.T) (kind, data string) {
	t.Helper()
	got := make(chan [2]string, 1)
	go func() {
		var k, d string
		for st.sc.Scan() {
			line := st.sc.Text()
			switch {
			case strings.HasPrefix(line, ": "):
				got <- [2]string{":", line}
				return
			case strings.HasPrefix(line, "event: "):
				k = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				d = strings.TrimPrefix(line, "data: ")
			case line == "" && k != "":
				got <- [2]string{k, d}
				return
			}
		}
		got <- [2]string{}
	}()
	select {
	case e := <-got:
		return e[0], e[1]
	case <-time.After(3 * time.Second):
		t.Fatal("no event in 3s")
	}
	return "", ""
}

func TestEvents(t *testing.T) {
	s, srv, worker, _, vic := commandHub(t)
	other := NewClient(srv.URL, "sekret", "")
	other.Enroll(Worker{Name: "other", Host: "box", Root: "/o"})
	s.Admit(other.ID, "test")
	all := openStream(t, srv.URL+"/api/events")
	mine := openStream(t, srv.URL+"/api/events?enrollment="+worker.ID)
	waitClients := func(n int) {
		t.Helper()
		for i := 0; s.events.clients() != n; i++ {
			if i > 300 {
				t.Fatalf("%d clients, want %d", s.events.clients(), n)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitClients(2)

	other.Report(Snapshot{Worker: Worker{Name: "other", Host: "box", Root: "/o"}, Activity: "idle"})
	if k, d := all.next(t); k != "snapshot" || !strings.Contains(d, `"name":"other"`) {
		t.Errorf("all, other's report: %s %s", k, d)
	}
	worker.Report(Snapshot{Worker: Worker{Name: "billing", Host: "box", Root: "/r"}, Activity: "running q-1"})
	for _, st := range []*stream{all, mine} {
		if k, d := st.next(t); k != "snapshot" || !strings.Contains(d, `"activity":"running q-1"`) {
			t.Errorf("billing's report: %s %s", k, d)
		}
	}
	worker.PutRun(testRun("r-1", s.now(), "passed"))
	if k, d := mine.next(t); k != "run" || !strings.Contains(d, `"run":"r-1"`) || !strings.Contains(d, `"result":"passed"`) {
		t.Errorf("run: %s %s", k, d)
	}
	v, _ := NewUser(srv.URL, vic, nil)
	sent, _ := v.Send(worker.ID, Command{Kind: KindLog, Lane: 1, Pin: Pin{Run: "r-1"}})
	if k, d := mine.next(t); k != "command" || d != `{"enrollment":"`+worker.ID+`","cid":"`+sent.CID+`"}` {
		t.Errorf("command: %s %s", k, d)
	}
	worker.UploadLog("r-1", 1, []byte("abc\n"))
	if k, d := mine.next(t); k != "log" || d != `{"enrollment":"`+worker.ID+`","run":"r-1","lane":1,"bytes":4}` {
		t.Errorf("log: %s %s", k, d)
	}

	// Shutting down ends every stream, and refuses new ones.
	s.CloseEvents()
	for _, st := range []*stream{all, mine} {
		for {
			if k, _ := st.next(t); k == "" {
				break
			}
		}
	}
	if code, _ := call(t, srv.URL+"/api/events", ""); code != http.StatusServiceUnavailable {
		t.Errorf("events after shutdown: %d", code)
	}
}

func TestEventsHeartbeatAndPrivate(t *testing.T) {
	old := EventHeartbeat
	EventHeartbeat = 50 * time.Millisecond
	defer func() { EventHeartbeat = old }()
	s, srv, _, _, vic := commandHub(t)
	if k, d := openStream(t, srv.URL+"/api/events").next(t); k != ":" || d != ": keep-alive" {
		t.Errorf("heartbeat: %s %s", k, d)
	}
	s.Private(true)
	if code, _ := call(t, srv.URL+"/api/events", ""); code != 401 {
		t.Errorf("private events: %d", code)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/events", nil)
	req.Header.Set("Authorization", "Bearer "+vic)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("private events as a user: %v %v", resp, err)
	}
	resp.Body.Close()
}

// A client that falls behind is dropped, never waited for.
func TestSlowEventClientDropped(t *testing.T) {
	e := newEventHub()
	slow := e.subscribe("")
	done := make(chan struct{})
	go func() {
		for i := 0; i < eventBuffer+10; i++ {
			e.publish("snapshot", "w", i)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publish blocked on a slow client")
	}
	n := 0
	for range slow.ch {
		n++
	}
	if n != eventBuffer || e.clients() != 0 {
		t.Errorf("the slow client got %d events (want %d, then closed); %d clients left", n, eventBuffer, e.clients())
	}
	e.unsubscribe(slow) // after a drop: no double close
}

func TestCommandsAPI(t *testing.T) {
	s, srv, worker, alice, vic := commandHub(t)
	worker.Report(Snapshot{Worker: Worker{Name: "billing", Host: "box", Root: "/r"}, Activity: "idle"})
	u, _ := NewUser(srv.URL, alice, nil)
	first, _ := u.Send(worker.ID, Command{Kind: KindDrain})
	second, _ := u.Send(worker.ID, Command{Kind: KindInterrupt, Lane: 2, Pin: Pin{Run: "r-1"}})
	if err := worker.Ack(first.CID, OutcomeRefused, "drain isn't in headless.allow"); err != nil {
		t.Fatal(err)
	}
	api := srv.URL + "/api/enrollments/" + worker.ID + "/commands"
	if code, _ := call(t, api, ""); code != 401 {
		t.Errorf("anonymous: %d", code)
	}
	var got struct{ Commands []CommandView }
	_, body := call(t, api, vic)
	json.Unmarshal([]byte(body), &got)
	if len(got.Commands) != 2 || got.Commands[0].CID != second.CID || got.Commands[0].State != "sent" ||
		got.Commands[1].State != OutcomeRefused || got.Commands[1].Detail != "drain isn't in headless.allow" {
		t.Errorf("commands: %s", body)
	}
	for i := 0; i < RecentCommands+5; i++ {
		u.Send(worker.ID, Command{Kind: KindDrain})
	}
	json.Unmarshal([]byte(func() string { _, b := call(t, api, vic); return b }()), &got)
	if len(got.Commands) != RecentCommands {
		t.Errorf("%d commands listed, want %d", len(got.Commands), RecentCommands)
	}
	if code, _ := call(t, srv.URL+"/api/enrollments/nope/commands", vic); code != 404 {
		t.Errorf("no such enrollment: %d", code)
	}
	_ = s
}

// The app's footer and the login page say which swim the hub runs.
func TestHubVersion(t *testing.T) {
	_, srv, _, _, _ := commandHub(t)
	if body := get(t, srv.URL+"/api/me"); !strings.Contains(body, `"version":"`+version.Long()+`"`) {
		t.Errorf("/api/me lacks the version: %s", body)
	}
	if body := get(t, srv.URL+"/login"); !strings.Contains(body, "swim hub · swim "+version.Long()) {
		t.Errorf("the login page lacks the version:\n%s", body)
	}
}
