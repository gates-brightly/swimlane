package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// A webhook hears each alert once: a failed run, a missed schedule, a
// worker gone quiet, a guard waiting; its body reads as a chat message.
func TestWebhookAlerts(t *testing.T) {
	var mu sync.Mutex
	var got []Alert
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var a Alert
		json.NewDecoder(r.Body).Decode(&a)
		mu.Lock()
		got = append(got, a)
		mu.Unlock()
	}))
	defer hook.Close()
	s, _, worker, _, _ := commandHub(t)
	if err := s.Webhooks("ftp://nope"); err == nil {
		t.Error("an ftp webhook accepted")
	}
	if err := s.Webhooks(hook.URL); err != nil {
		t.Fatal(err)
	}
	defer s.CloseAlerts()
	s.PublicURL("https://swim.example.ts.net/")
	now := time.Date(2026, 10, 23, 2, 30, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	worker.Report(Snapshot{Worker: Worker{Name: "backup", Host: "nas", Root: "/r"}, Interval: 2, Schedules: []Schedule{
		{ID: "r-nightly", Lanes: []int{3}, Cadence: "daily at 02:00:00 (UTC)", Next: "2026-10-23T02:00:00Z"}}})
	worker.PutRun(testRun("r-1", now, "passed", "failed"))
	worker.PutRun(testRun("r-1", now, "passed", "failed")) // again: no second alert
	s.CheckAlerts()
	now = now.Add(time.Hour) // the worker has gone quiet
	s.CheckAlerts()
	s.CheckAlerts()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	events := map[string]Alert{}
	for _, a := range got {
		if _, dup := events[a.Event]; dup {
			t.Errorf("%s sent twice", a.Event)
		}
		events[a.Event] = a
	}
	run, ok := events[AlertRunFailed]
	if !ok || run.Run != "r-1" || run.Lane != 2 || !strings.Contains(run.Text, "lane 2 failed") || run.URL != "https://swim.example.ts.net/e/"+worker.ID+"/runs/r-1" || run.Content != run.Text {
		t.Errorf("run alert: %+v", run)
	}
	if m, ok := events[AlertScheduleMissed]; !ok || m.Schedule != "r-nightly" || !strings.Contains(m.Text, "schedule r-nightly (lanes 3, daily at 02:00:00 (UTC)) missed") {
		t.Errorf("missed schedule alert: %+v", m)
	}
	if st, ok := events[AlertWorkerStale]; !ok || !strings.Contains(st.Text, "backup (nas): stopped reporting") {
		t.Errorf("stale worker alert: %+v", st)
	}
}
