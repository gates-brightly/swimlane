package hub

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/runrec"
)

// A worker's configuration is served alone, to a hub user, sanitized; the
// lists and events never carry it.
func TestWorkerConfig(t *testing.T) {
	_, srv, worker, _, vic := commandHub(t)
	conf := "effective:\n  shell: bash\n\x1b]0;evil\x07hub_access: read\n"
	if err := worker.Report(Snapshot{Worker: Worker{Name: "billing", Host: "box", Root: "/r"}, Config: conf}); err != nil {
		t.Fatal(err)
	}
	get := func(path, tok string) (int, string) {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, _ := get("/api/enrollments/"+worker.ID+"/config", ""); code != http.StatusUnauthorized {
		t.Errorf("config for nobody: %d", code)
	}
	code, body := get("/api/enrollments/"+worker.ID+"/config", vic)
	var out struct{ Config string }
	json.Unmarshal([]byte(body), &out)
	if code != 200 || !strings.Contains(out.Config, "shell: bash") || !strings.Contains(out.Config, "hub_access: read") || strings.Contains(out.Config, "\x1b") {
		t.Errorf("config: %d %q", code, out.Config)
	}
	_, list := get("/api/enrollments", vic)
	if strings.Contains(list, "shell: bash") || !strings.Contains(list, `"has_config":true`) {
		t.Errorf("the list carries the config, or no has_config:\n%s", list)
	}
	if code, _ := get("/api/enrollments/nope/config", vic); code != http.StatusNotFound {
		t.Errorf("unknown worker: %d", code)
	}
}

// The hub tells what it knows of a runner: the address it reports from,
// when it first enrolled, and how many runs it has sent (a run sent again
// counts once).
func TestRunnerInfo(t *testing.T) {
	s, _, worker, _, _ := commandHub(t)
	worker.Report(Snapshot{Worker: Worker{Name: "billing", Host: "box", Root: "/r"}})
	for _, run := range []string{"r-1", "r-2", "r-2"} {
		if _, err := worker.PutRun(testRun(run, s.now(), "passed")); err != nil {
			t.Fatal(err)
		}
	}
	v, _ := s.worker(worker.ID)
	ri := v.Runner
	if ri.Addr != "127.0.0.1" || ri.From != "127.0.0.1" || ri.EnrolledAt == "" || ri.Runs != 2 || ri.KeptRuns != 2 || ri.LastRun == "" {
		t.Errorf("runner info: %+v", ri)
	}
}

// A run sent without step output shows it once its log is fetched: the
// hub fills each step's output from the round in the log.
func TestJobOutputFromFetchedLog(t *testing.T) {
	_, srv, worker, _, vic := commandHub(t)
	logText := "== ROUND 2026-10-09T12:00:00Z  job=j1  Lint\n   run: r-1\n" +
		"  PASS  say  0.1s  12:00:01\n        $ echo hi\n        | hi \x1b[31mthere\n" +
		"== END PASS  pass=1 fail=0 skip=0 drift=0  exit=0  1.0s  2026-10-09T12:00:02Z\n"
	rep := &RunReport{Run: runrec.Run{Schema: runrec.Schema, Run: "r-1", Started: time.Now(), Finished: time.Now()}}
	rep.Run.Lanes = []runrec.Lane{{Lane: 1, State: "passed"}}
	rep.Jobs = []logparse.Doc{{Schema: logparse.DocSchema, Lane: 1, Run: "r-1", Result: "passed",
		Results: []logparse.ResultDoc{{Result: "PASS", Label: "say", Step: true, Command: "echo hi"}}}}
	if _, err := worker.PutRun(rep); err != nil {
		t.Fatal(err)
	}
	job := func() (string, []string) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/enrollments/"+worker.ID+"/runs/r-1/jobs/1", nil)
		req.Header.Set("Authorization", "Bearer "+vic)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out struct {
			Job        logparse.Doc
			OutputFrom string `json:"output_from"`
		}
		json.NewDecoder(resp.Body).Decode(&out)
		return out.OutputFrom, out.Job.Results[0].Output
	}
	if from, o := job(); from != "" || len(o) != 0 {
		t.Fatalf("output before any log: %q %q", from, o)
	}
	v, _ := NewUser(srv.URL, vic, nil)
	v.Send(worker.ID, Command{Kind: KindLog, Lane: 1, Pin: Pin{Run: "r-1"}})
	if err := worker.UploadLog("r-1", 1, []byte(logText)); err != nil {
		t.Fatal(err)
	}
	if from, o := job(); from != "log" || len(o) != 1 || !strings.HasPrefix(o[0], "hi ") || strings.ContainsRune(o[0], '\x1b') {
		t.Errorf("output from the fetched log: %q %q", from, o)
	}
}
