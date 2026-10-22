package hub

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStreamedLog(t *testing.T) {
	s, srv, worker, _, vic := commandHub(t)
	file := s.runs.streamPath(worker.ID, "r-1", 2)
	read := func() string { b, _ := os.ReadFile(file); return string(b) }
	events := openStream(t, srv.URL+"/api/events?enrollment="+worker.ID)
	for i := 0; s.events.clients() != 1; i++ {
		if i > 300 {
			t.Fatal("no event client")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := worker.StreamLog("r-1", 2, 0, []byte("== ROUND\nstep one \x1b[31mok\n"), false); err != nil {
		t.Fatal(err)
	}
	if k, d := events.next(t); k != "log" || !strings.Contains(d, `"run":"r-1","lane":2`) || strings.Contains(d, "final") {
		t.Errorf("log event: %s %s", k, d)
	}
	// Sanitized as stored, but counted as received: the next offset is the sender's.
	sent := len("== ROUND\nstep one \x1b[31mok\n")
	if err := worker.StreamLog("r-1", 2, sent, []byte("step two\n"), true); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "== ROUND\nstep one ok\nstep two\n" {
		t.Errorf("streamed log: %q", got)
	}
	if k, d := events.next(t); k != "log" || !strings.Contains(d, `"final":true`) {
		t.Errorf("final event: %s %s", k, d)
	}
	// Another offset: refused, with the hub's count.
	if err := worker.StreamLog("r-1", 2, 3, []byte("x\n"), false); !errors.Is(err, ErrStreamOffset) {
		t.Errorf("a wrong offset: %v", err)
	}
	req, _ := http.NewRequest("PUT", srv.URL+"/api/workers/"+worker.ID+"/stream/r-1/2?offset=3", strings.NewReader("x\n"))
	req.Header.Set("Authorization", "Bearer "+worker.WorkerToken)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 409 || resp.Header.Get(StreamOffsetHeader) != strconv.Itoa(sent+len("step two\n")) {
		t.Errorf("409 offset: %v %v", resp.Header.Get(StreamOffsetHeader), err)
	}

	// Read like any job log: a user, Range honored; it wins over a cached one.
	api := srv.URL + "/api/enrollments/" + worker.ID + "/runs/r-1/jobs/2/log"
	if code, _ := call(t, api, ""); code != 401 {
		t.Errorf("anonymous: %d", code)
	}
	if code, body := call(t, api, vic); code != 200 || body != read() {
		t.Errorf("read: %d %q", code, body)
	}
	if code, body := call(t, api, vic, "Range", "bytes=9-16"); code != 206 || body != "step one" {
		t.Errorf("range: %d %q", code, body)
	}

	// offset 0 replaces it (a worker that restarted, or whose masking changed).
	if err := worker.StreamLog("r-1", 2, 0, []byte("== ROUND\nagain\n"), false); err != nil || read() != "== ROUND\nagain\n" {
		t.Errorf("replace: %v %q", err, read())
	}
	// A restarted hub has the log but not the count: the worker starts over.
	s2, _ := NewWithState("sekret", "adm1n", s.dir)
	if s2.streams.received[file] != 0 {
		t.Error("a new hub has a count")
	}

	// Refusals.
	other := NewClient(srv.URL, "sekret", worker.ID)
	other.WorkerToken = "nope"
	if err := other.StreamLog("r-1", 2, 0, []byte("x\n"), false); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("another token: %v", err)
	}
	for _, bad := range []string{"/stream/..x/2?offset=0", "/stream/r-1/0?offset=0", "/stream/r-1/x?offset=0", "/stream/r-1/2?offset=-1", "/stream/r-1/2"} {
		req, _ := http.NewRequest("PUT", srv.URL+"/api/workers/"+worker.ID+bad, strings.NewReader("x\n"))
		req.Header.Set("Authorization", "Bearer "+worker.WorkerToken)
		if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 400 {
			t.Errorf("%s: %v %v", bad, resp.StatusCode, err)
		}
	}
	big := strings.Repeat("x", 1<<20) + "\n"
	for off := len("== ROUND\nagain\n"); off < LogMax; off += len(big) {
		if err := worker.StreamLog("r-1", 2, off, []byte(big), false); err != nil {
			if !strings.Contains(err.Error(), "413") {
				t.Fatalf("at %d: %v", off, err)
			}
			break
		}
	}
	if fi, _ := os.Stat(file); fi.Size() > LogMax {
		t.Errorf("a streamed log grew to %d bytes", fi.Size())
	}
}

// A run's streamed logs go with it; those of a run that never arrived go by age.
func TestStreamedLogRetention(t *testing.T) {
	s, _, worker, _, _ := commandHub(t)
	now := s.now()
	worker.StreamLog("r-old", 1, 0, []byte("old\n"), true)
	worker.StreamLog("r-new", 1, 0, []byte("new\n"), true)
	worker.StreamLog("r-orphan", 1, 0, []byte("orphan\n"), false)
	worker.StreamLog("r-fresh-orphan", 1, 0, []byte("fresh\n"), false)
	if _, err := worker.PutRun(testRun("r-new", now.Add(-time.Hour), "passed")); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.PutRun(testRun("r-old", now.Add(-2*time.Hour), "passed")); err != nil {
		t.Fatal(err)
	}
	info := s.Runs(worker.ID)
	if len(info) != 2 || info[0].Bytes <= int64(len("new\n")) {
		t.Fatalf("runs: %+v", info)
	}
	old := now.Add(-40 * 24 * time.Hour)
	os.Chtimes(s.runs.streamPath(worker.ID, "r-orphan", 1), old, old)
	// Room for one run: the older goes, with its log.
	s.RetainRuns(DefaultRunRetain, info[0].Bytes)
	exists := func(run string) bool { _, err := os.Stat(s.runs.streamPath(worker.ID, run, 1)); return err == nil }
	if exists("r-old") || !exists("r-new") || exists("r-orphan") || !exists("r-fresh-orphan") {
		files, _ := filepath.Glob(filepath.Join(s.runs.dir, worker.ID, "*"))
		t.Errorf("after pruning: %v", files)
	}
}
