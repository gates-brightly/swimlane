package hub

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
		Lanes: []Lane{{Lane: 1, State: "running", Round: "cut over <api>", Step: "deploy", Tail: []string{"  RUNNING  deploy (in progress)", "        | step 3/5"}},
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
	if got.Schema != "swim.hub/v1" || len(got.Workers) != 1 || got.Workers[0].Activity != "running q-1" || got.Workers[0].Stale {
		t.Fatalf("workers: %+v", got)
	}
	// Pages: escaped, with the live tail.
	page := get(t, srv.URL+"/")
	for _, want := range []string{"billing", "/srv/billing", "running q-1", "swim 1 running", "cut over &lt;api&gt;", "1 failed"} {
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
