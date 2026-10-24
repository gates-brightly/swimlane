package hub

import (
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/runrec"
)

func TestActivityAndJobs(t *testing.T) {
	s, _, worker, _, _ := commandHub(t)
	now := s.now()
	worker.Report(Snapshot{Worker: Worker{Name: "billing", Host: "box", Root: "/r", Namespace: "github.com/org/billing"}})
	run := func(id string, at time.Time, round, state string, dry bool, trigger string) {
		zero, one := 0.0, 42.0
		rep := &RunReport{Run: runrec.Run{Schema: runrec.Schema, Run: id, Started: at, Finished: at.Add(time.Minute),
			Lanes: []runrec.Lane{{Lane: 1, State: state, Round: round, Start: &zero, End: &one}}}, Trigger: &Trigger{Kind: trigger}}
		doc := logparse.Doc{Schema: logparse.DocSchema, Lane: 1, Run: id, Result: state}
		if dry {
			doc.Results = []logparse.ResultDoc{{Result: logparse.Skip, Label: "renew", Detail: "dry run: set RENEW=1 to approve"}}
		}
		rep.Jobs = []logparse.Doc{doc}
		if _, err := worker.PutRun(rep); err != nil {
			t.Fatal(err)
		}
	}
	run("r-1", now.Add(-48*time.Hour), "renew certs", "passed", false, TriggerQueue)
	run("r-2", now.Add(-3*time.Hour), "renew certs", "passed", true, TriggerQueue)
	run("r-3", now.Add(-2*time.Hour), "renew certs", "failed", false, TriggerHub)
	run("r-4", now.Add(-1*time.Hour), "backup", "passed", false, TriggerCLI) // lane 1 rewritten: another job

	day := s.Activity(ActivityFilter{Since: 24 * time.Hour})
	if len(day) != 3 || day[0].Run != "r-4" || day[2].Run != "r-2" || day[2].Status != StatusDryRun || day[0].Name != "billing" {
		t.Fatalf("the last day: %+v", day)
	}
	if f := s.Activity(ActivityFilter{Status: "failed"}); len(f) != 1 || f[0].Run != "r-3" {
		t.Errorf("failed: %+v", f)
	}
	if f := s.Activity(ActivityFilter{Trigger: TriggerQueue}); len(f) != 2 {
		t.Errorf("by queue: %+v", f)
	}
	if f := s.Activity(ActivityFilter{Namespace: "github.com/org/other"}); len(f) != 0 {
		t.Errorf("another repo: %+v", f)
	}
	jobs := s.Jobs("")
	if len(jobs) != 2 {
		t.Fatalf("jobs: %+v", jobs)
	}
	j := jobs[0] // failing first
	if j.Round != "renew certs" || j.Runs != 3 || j.Passed != 1 || j.LastStatus != "failed" || j.LastRun != "r-3" || len(j.Recent) != 3 || j.Recent[1].Status != StatusDryRun || j.Recent[0].Duration != 42 {
		t.Errorf("renew certs: %+v", j)
	}
	if b := jobs[1]; b.Round != "backup" || b.Runs != 1 || b.LastStatus != "passed" {
		t.Errorf("backup: %+v", b)
	}
}
