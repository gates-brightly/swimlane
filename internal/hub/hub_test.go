package hub

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/display"
)

func TestHubFlow(t *testing.T) {
	s := New("sekret")
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	bad := NewClient(srv.URL, "wrong", "")
	if _, err := bad.Enroll(Worker{Name: "w", Host: "h", Root: "/r"}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("wrong token: %v", err)
	}
	// A worker that enrolled but hasn't reported: lists, not null.
	idle := NewClient(srv.URL, "sekret", "")
	idle.Enroll(Worker{Name: "quiet", Host: "box2", Root: "/srv/quiet"})
	if body := get(t, srv.URL+"/api/workers"); strings.Contains(body, "null") {
		t.Errorf("null in the JSON: %s", body)
	}
	idle.Leave()
	c := NewClient(srv.URL, "sekret", "")
	id, err := c.Enroll(Worker{Name: "billing", Host: "box1", Root: "/srv/billing"})
	if err != nil || id != IDFor("box1", "/srv/billing") {
		t.Fatalf("enroll: %q %v", id, err)
	}
	exit := 1
	snap := Snapshot{Worker: Worker{Name: "billing", Host: "box1", Root: "/srv/billing"}, Activity: "running q-1", Interval: 2,
		Lanes: []Lane{{Lane: 1, State: "running", Round: "cut over <api>", Step: "deploy", Tail: []string{"  RUNNING  deploy (in progress)", "        | step 3/5"},
			Message: "migrating billing", Progress: "7/28", MsgAt: time.Now().Add(-9 * time.Minute).UTC().Format(time.RFC3339)},
			{Lane: 2, State: "failed", Round: "migrate", Exit: &exit}},
		Queue:  []Batch{{Seq: 1, ID: "q-1", State: "running", Lanes: []int{1, 2}}},
		Events: []string{"2026-10-13T15:00:00Z  zach  queue-run  q-1"}}
	if err := c.Report(snap); err != nil {
		t.Fatal(err)
	}

	// JSON for services.
	resp, _ := http.Get(srv.URL + "/api/workers")
	var got struct {
		Schema  string
		Workers []View
	}
	json.NewDecoder(resp.Body).Decode(&got)
	if body := get(t, srv.URL+"/api/workers"); !strings.Contains(body, `"message":"migrating billing"`) || !strings.Contains(body, `"progress":"7/28"`) {
		t.Errorf("api lacks the message: %s", body)
	}
	if got.Schema != "swim.hub/v1" || len(got.Workers) != 1 || got.Workers[0].Activity != "running q-1" || got.Workers[0].Stale {
		t.Fatalf("workers: %+v", got)
	}
	// Pages: escaped, with the live tail.
	page := get(t, srv.URL+"/")
	for _, want := range []string{"billing", "/srv/billing", "running q-1", "swim 1 running", "cut over &lt;api&gt;", "1 failed",
		"… migrating billing (7/28)", `style="width:25%"`, "9m ago"} {
		if !strings.Contains(page, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	detail := get(t, srv.URL+"/w/"+id)
	for _, want := range []string{"RUNNING  deploy (in progress)", "| step 3/5", "q-1", "queue-run", "(exit 1)"} {
		if !strings.Contains(detail, want) {
			t.Errorf("worker page missing %q", want)
		}
	}

	// A restarted hub forgets workers: the next report enrolls again.
	s.mu.Lock()
	s.workers = map[string]*View{}
	s.mu.Unlock()
	if err := c.Report(snap); err != nil {
		t.Fatalf("report after a hub restart: %v", err)
	}
	if len(s.Workers()) != 1 {
		t.Fatal("not re-enrolled")
	}
	// Silent workers go stale.
	s.now = func() time.Time { return time.Now().Add(time.Minute) }
	if !s.Workers()[0].Stale {
		t.Error("a worker silent for a minute should be stale")
	}
	if err := c.Leave(); err != nil || len(s.Workers()) != 0 {
		t.Fatalf("leave: %v", err)
	}
}

func get(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestScreenRows(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	views := []View{
		{Snapshot: Snapshot{Worker: Worker{ID: "a1", Name: "orders", Host: "mac", Root: "/r/orders"}, Activity: "running q-1 (#4)",
			Lanes: []Lane{
				{Lane: 1, State: "running", Round: "Cut over", Step: "plan", Progress: "3/7", Started: now.Add(-42 * time.Second).Format(time.RFC3339), Tail: []string{"| tick"}},
				{Lane: 2, State: "idle"},
			},
			Queue: []Batch{{Seq: 5, State: "queued", Lanes: []int{3, 4}, By: "zach"}}}, Seen: now},
		{Snapshot: Snapshot{Worker: Worker{ID: "b2", Name: "infra", Host: "box"}, Activity: "idle"}, Seen: now.Add(-2 * time.Minute), Stale: true},
	}
	if got := Summary(views); got != "2 workers · 1 lanes running" {
		t.Errorf("summary: %q", got)
	}
	text := func(rows []display.PickRow) string {
		var b strings.Builder
		for _, r := range rows {
			b.WriteString(r.ID + "|" + r.Text + "\n")
		}
		return b.String()
	}
	list := text(ListRows(views, "", nil, now))
	for _, want := range []string{"w:a1|orders", "running", "▶·", "w:b2|infra", "stale 2m", "(no report since 11:58)",
		"── orders · /r/orders", "swim 1  running     Cut over", "plan · 3/7 · 0:42", "queue   #5 queued   swim 3,4  by zach"} {
		if !strings.Contains(list, want) {
			t.Errorf("list lacks %q:\n%s", want, list)
		}
	}
	if strings.Contains(list, "swim 2 ") {
		t.Errorf("the list shows an idle lane:\n%s", list)
	}
	// Selecting the other worker shows its lanes instead.
	if l := text(ListRows(views, "w:b2", nil, now)); !strings.Contains(l, "── infra") || !strings.Contains(l, "every lane idle") {
		t.Errorf("selected infra:\n%s", l)
	}
	detail := text(WorkerRows(views[0], "l:1", now))
	for _, want := range []string{"orders · mac · /r/orders", "l:1|swim 1", "l:2|swim 2", "── swim 1", "| tick", "── queue"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail lacks %q:\n%s", want, detail)
		}
	}
	if d := text(WorkerRows(views[0], "l:2", now)); !strings.Contains(d, "no log tail") {
		t.Errorf("an idle lane's detail:\n%s", d)
	}
}

// A hostile worker's report: its pages escape HTML (html/template) and the
// hub drops escape sequences, controls and bidi overrides on the way in,
// from the pages and the JSON alike.
func TestHubUntrustedText(t *testing.T) {
	s := New("sekret")
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	c := NewClient(srv.URL, "sekret", "")
	id, err := c.Enroll(Worker{Name: "evil\x1b]0;pwned\x07<b>", Host: "box", Root: "/r"})
	if err != nil {
		t.Fatal(err)
	}
	bad := "<script>alert(1)</script>\x1b[2K\r\b‮gnp.exe"
	snap := Snapshot{Worker: Worker{Name: "evil"}, Activity: "running " + bad,
		Lanes: []Lane{{Lane: 1, State: "running", Round: bad, Step: bad, Message: bad, Tail: []string{bad}}},
		Queue: []Batch{{Seq: 1, ID: "q-1", State: "queued", Note: bad, Lanes: []int{1}}}, Events: []string{bad}}
	if err := c.Report(snap); err != nil {
		t.Fatal(err)
	}
	for _, page := range []string{get(t, srv.URL+"/"), get(t, srv.URL+"/w/"+id), get(t, srv.URL+"/api/workers")} {
		if strings.Contains(page, "<script>") || strings.Contains(page, "<b>") {
			t.Errorf("unescaped HTML:\n%s", page)
		}
		for _, c := range []string{"\x1b", "\r", "\b", "‮", "\\u001b", "\\r", "\\u0008", "\\u202e", "\\u202E"} {
			if strings.Contains(page, c) {
				t.Errorf("%q got through:\n%s", c, page)
			}
		}
	}
	if page := get(t, srv.URL+"/w/"+id); !strings.Contains(page, "&lt;script&gt;alert(1)&lt;/script&gt;gnp.exe") {
		t.Errorf("the text itself should still show, escaped:\n%s", page)
	}
}
