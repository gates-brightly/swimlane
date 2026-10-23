package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/hub"
	"github.com/gates-brightly/swimlane/internal/runrec"
)

func TestSendHubRuns(t *testing.T) {
	root := t.TempDir()
	start := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	write := func(i int) {
		t.Helper()
		id := fmt.Sprintf("r-20261022T12000%dZ-abcd", i)
		if err := runrec.Write(root, &runrec.Run{Run: id, Started: start.Add(time.Duration(i) * time.Second), Finished: start.Add(time.Minute),
			Lanes: []runrec.Lane{{Lane: 1, State: "passed"}}}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 3; i++ {
		write(i)
	}
	h, err := hub.NewWithState("sekret", "adm1n", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var puts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" && strings.Contains(r.URL.Path, "/runs/") {
			puts.Add(1)
		}
		h.Handler().ServeHTTP(w, r)
	}))
	defer srv.Close()
	c := hub.NewClient(srv.URL, "sekret", "")
	if _, err := c.Enroll(hub.Worker{Name: "w", Host: "box", Root: root}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Admit(c.ID, "test"); err != nil {
		t.Fatal(err)
	}
	hf := &hubFile{URL: srv.URL, ID: c.ID, WorkerToken: c.WorkerToken}
	send := func(want int32, why string) {
		t.Helper()
		puts.Store(0)
		if err := sendHubRuns(root, &config.Config{}, hf, c); err != nil {
			t.Fatalf("%s: %v", why, err)
		}
		if got := puts.Load(); got != want {
			t.Errorf("%s: %d runs sent, want %d", why, got, want)
		}
	}
	send(3, "first report")
	if n := len(h.Runs(c.ID)); n != 3 {
		t.Fatalf("hub keeps %d runs", n)
	}
	send(0, "nothing new")
	write(4)
	send(1, "a new run")
	// A record written again (a new mtime) goes again.
	later := time.Now().Add(time.Minute)
	os.Chtimes(runrec.Path(root, "r-20261022T120002Z-abcd"), later, later)
	send(1, "a record that changed")
	// A new enrollment (here by the admin, who may enroll it again without
	// its token) gets them all again.
	c.Token, c.WorkerToken = "adm1n", ""
	if _, err := c.Enroll(hub.Worker{Name: "w", Host: "box", Root: root}); err != nil {
		t.Fatal(err)
	}
	send(4, "a new worker token")
}

func TestRecentRuns(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(runrec.Dir(root), 0o755)
	for _, id := range []string{"r-20261022T120003Z-a", "r-20261022T120001Z-b", "r-20261022T120002Z-c"} {
		os.WriteFile(runrec.Path(root, id), []byte("run: "+id), 0o644)
	}
	os.Mkdir(runrec.Path(root, "r-20261022T120004Z-dir")+".d", 0o755)
	if got := strings.Join(recentRuns(root, 2), " "); got != "r-20261022T120002Z-c r-20261022T120003Z-a" {
		t.Errorf("recent: %s", got)
	}
}
