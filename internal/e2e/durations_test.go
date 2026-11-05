package e2e

import (
	"regexp"
	"strings"
	"testing"
)

var (
	clockTime = regexp.MustCompile(`\d+:\d\d:\d\d`) // 15:04:41, 2026-11-05T15:04:41Z
	minSec    = regexp.MustCompile(`\b\d+:\d\d\b`)
)

// Durations read one way everywhere a person reads them ("0.4s", "12s",
// "1m 5s"): no m:ss in a run's summary, status, watch or a lane's log.
// Clock times stay.
func TestDurationsReadOneWay(t *testing.T) {
	r := newRepo(t)
	r.shortLane(1, "# Round: lists", "stage check\nstep \"wait\" true\nstep \"list\" ls -a")
	r.shortLane(2, "# Round: fails", "stage check\ngate \"config exists\" false")
	run, _ := r.swim("run", "1", "2", "--plain")
	one, _ := r.swim("run", "2", "--plain")
	status, _ := r.swim("status")
	watch, _ := r.swim("watch", "--plain", "--once")
	history, _ := r.swim("log")
	for what, out := range map[string]string{
		"run": run, "run one": one, "status": status, "watch": watch, "log 1": r.log(1), "log 2": r.log(2), ".swim.log": history,
	} {
		if m := minSec.FindString(clockTime.ReplaceAllString(out, "")); m != "" {
			t.Errorf("%s shows a duration as m:ss (%s):\n%s", what, m, out)
		}
	}
	if !regexp.MustCompile(`PASS  wait\s+\d\.\ds  \d\d:\d\d:\d\d`).MatchString(r.log(1)) {
		t.Errorf("the step's duration isn't N.Ns:\n%s", r.log(1))
	}

	// A failed lane says why once in its summary (the STOP that restates
	// the gate is the log's), and adds no result line after it.
	if strings.Count(one, "FAIL  config exists (exit 1)") != 2 || strings.Count(one, "STOP  gate failed: config exists") != 1 {
		t.Errorf("one lane: the stream says the failure, the summary once more:\n%s", one)
	}
	if strings.Contains(one, "[2] FAIL (exit") || strings.Contains(run, "FAIL (exit") || strings.Contains(run, "PASS (") {
		t.Errorf("a result line after the lane's own summary:\n%s\n%s", run, one)
	}
	// Status and .swim.log name the failure once too.
	if !strings.Contains(status, "FAIL  config exists") || strings.Contains(status, "STOP  gate failed") {
		t.Errorf("status: the failure once, no STOP restating it:\n%s", status)
	}
	if !strings.Contains(history, "| FAIL  config exists (exit 1)") || strings.Contains(history, "STOP  gate failed") {
		t.Errorf(".swim.log: the failure once:\n%s", history)
	}
	// Several lanes: one summary line each in the stream, the reasons in
	// the run's table.
	if strings.Count(run, "FAIL  config exists (exit 1)") != 2 || !strings.Contains(run, "[2] swim 2 summary: FAIL") || strings.Contains(run, "stages: ") {
		t.Errorf("two lanes: each lane's summary is one line, the table says why:\n%s", run)
	}
	// The round header names no unset env, and the lane's directory holds
	// only its own files.
	if strings.Contains(r.log(1), "<unset>") {
		t.Errorf("the round header names unset variables:\n%s", r.log(1))
	}
	if !strings.Contains(r.log(1), "| lane.1.sh") || strings.Contains(r.log(1), "| manifest.yml") {
		t.Errorf("the lane's directory isn't only its own files:\n%s", r.log(1))
	}
}
