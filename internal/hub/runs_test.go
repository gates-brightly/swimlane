package hub

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/runrec"
)

func testRun(id string, started time.Time, states ...string) *RunReport {
	rep := &RunReport{Run: runrec.Run{Schema: runrec.Schema, Run: id, Started: started, Finished: started.Add(time.Minute)}}
	for i, st := range states {
		rep.Run.Lanes = append(rep.Run.Lanes, runrec.Lane{Lane: i + 1, State: st, Round: "round \x1b[31mred"})
		rep.Jobs = append(rep.Jobs, logparse.Doc{Schema: logparse.DocSchema, Lane: i + 1, Run: id, Result: st,
			Results: []logparse.ResultDoc{{Result: "PASS", Label: "say", Step: true, Output: []string{"hi \x1b]8;;evil\x07there"}}}})
	}
	return rep
}

func TestRunsKept(t *testing.T) {
	s, srv, worker, _, _ := commandHub(t)
	now := s.now()
	if _, err := worker.PutRun(testRun("r-1", now.Add(-2*time.Hour), "passed", "failed")); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.PutRun(testRun("r-2", now.Add(-time.Hour), "passed", "skipped")); err != nil {
		t.Fatal(err)
	}
	runs := s.Runs(worker.ID)
	if len(runs) != 2 || runs[0].Run != "r-2" || runs[0].Result != "passed" || runs[1].Result != "failed" || len(runs[1].Lanes) != 2 {
		t.Fatalf("runs: %+v", runs)
	}
	rep, ok := s.Run(worker.ID, "r-1")
	if !ok || len(rep.Jobs) != 2 || strings.ContainsRune(rep.Jobs[0].Results[0].Output[0], '\x1b') || strings.ContainsRune(rep.Run.Lanes[0].Round, '\x1b') {
		t.Fatalf("kept run isn't sanitized: %+v", rep)
	}
	// Sending a run again replaces it.
	if _, err := worker.PutRun(testRun("r-1", now.Add(-2*time.Hour), "passed")); err != nil {
		t.Fatal(err)
	}
	if rep, _ := s.Run(worker.ID, "r-1"); len(rep.Jobs) != 1 {
		t.Errorf("replaced run: %+v", rep)
	}
	// A restarted hub has them.
	s2, err := NewWithState("sekret", "adm1n", s.dir)
	if err != nil {
		t.Fatal(err)
	}
	if runs := s2.Runs(worker.ID); len(runs) != 2 || s2.RunsBytes() != s.RunsBytes() {
		t.Fatalf("after a restart: %+v (%d bytes, want %d)", runs, s2.RunsBytes(), s.RunsBytes())
	}

	// Refusals: another token, a report for another run, a bad id.
	other := NewClient(srv.URL, "sekret", worker.ID)
	other.WorkerToken = "nope"
	if perm, err := other.PutRun(testRun("r-3", now, "passed")); err == nil || perm {
		t.Errorf("another token: %v (permanent %v)", err, perm)
	}
	bad := testRun("r-4", now, "passed")
	req, _ := http.NewRequest("PUT", srv.URL+"/api/workers/"+worker.ID+"/runs/r-5", strings.NewReader(`{"run":{"run":"r-4"}}`))
	req.Header.Set("Authorization", "Bearer "+worker.WorkerToken)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusBadRequest {
		t.Errorf("mismatched run id: %v %v", resp, err)
	}
	bad.Run.Run = "..x"
	if perm, err := worker.PutRun(bad); err == nil || !perm {
		t.Errorf("bad run id: %v (permanent %v)", err, perm)
	}
	if len(s.Runs(worker.ID)) != 2 {
		t.Errorf("a refused run was kept: %+v", s.Runs(worker.ID))
	}
}

func TestRunsRetention(t *testing.T) {
	s, _, worker, _, _ := commandHub(t)
	now := s.now()
	put := func(id string, ago time.Duration) {
		t.Helper()
		if _, err := worker.PutRun(testRun(id, now.Add(-ago), "passed")); err != nil {
			t.Fatal(err)
		}
	}
	put("r-old", 40*24*time.Hour)
	put("r-a", 3*time.Hour)
	put("r-b", 2*time.Hour)
	put("r-c", time.Hour)
	if got := names(s.Runs(worker.ID)); got != "r-c r-b r-a" {
		t.Fatalf("a run older than 30 days was kept: %s", got)
	}
	// Over the byte limit: the oldest go until the rest fit.
	one := s.Runs(worker.ID)[0].Bytes
	s.RetainRuns(DefaultRunRetain, 2*one)
	if got := names(s.Runs(worker.ID)); got != "r-c r-b" || s.RunsBytes() > 2*one {
		t.Fatalf("byte limit: %s (%d bytes)", got, s.RunsBytes())
	}
	// 0: no limit.
	s.RetainRuns(0, 0)
	put("r-older", 400*24*time.Hour)
	if got := names(s.Runs(worker.ID)); got != "r-c r-b r-older" {
		t.Fatalf("no limits: %s", got)
	}
}

func names(runs []RunInfo) string {
	var out []string
	for _, r := range runs {
		out = append(out, r.Run)
	}
	return strings.Join(out, " ")
}

func TestRunsNeedState(t *testing.T) {
	s := New("sekret")
	if err := s.runs.put("w", testRun("r-1", time.Now(), "passed"), []byte("{}")); err != errNoState {
		t.Errorf("a hub without state kept a run: %v", err)
	}
}

func TestRunResult(t *testing.T) {
	for want, states := range map[string][]string{
		"passed":      {"passed", "skipped"},
		"failed":      {"passed", "interrupted", "failed"},
		"interrupted": {"interrupted", "passed"},
		"skipped":     {"skipped"},
	} {
		r := &runrec.Run{}
		for _, st := range states {
			r.Lanes = append(r.Lanes, runrec.Lane{State: st})
		}
		if got := RunResult(r); got != want {
			t.Errorf("%v: %s, want %s", states, got, want)
		}
	}
}
