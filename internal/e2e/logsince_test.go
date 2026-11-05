package e2e

import (
	"strings"
	"testing"
)

// An AI catches up on what happened since the last run it read: swim log
// --since RUN is the project log from that run on, with each round's
// result, failed steps and job (for swim log JOB); --since takes a job, a
// duration back from now or a UTC time too.
func TestLogSince(t *testing.T) {
	r := newRepo(t)
	r.script(1, "one", `step "first" echo one-a`)
	r.mustSwim("run", "1")
	first := r.status().Get(1)

	r.script(1, "one again", `step "broken" false`)
	r.script(2, "two", `step "second" echo two-a`)
	r.swim("run", "1", "2")
	second := r.status().Get(1)
	if first.Run == "" || second.Run == "" || first.Run == second.Run {
		t.Fatalf("runs: %q %q", first.Run, second.Run)
	}

	out := r.mustSwim("log", "--since", second.Run)
	contains(t, "since the second run", out, "run="+second.Run, "fail         lane 1  job="+second.Job, "FAIL  broken", "pass         lane 2")
	if strings.Contains(out, first.Run) || strings.Contains(out, "# swim project log") {
		t.Errorf("--since %s shows lines from before it:\n%s", second.Run, out)
	}
	contains(t, "since the first run", r.mustSwim("log", "--since", first.Run), "run="+first.Run, "run="+second.Run)
	contains(t, "since a job", r.mustSwim("log", "--since", second.Job[:8]), "FAIL  broken")
	contains(t, "since an hour ago", r.mustSwim("log", "--since", "1h"), "run="+first.Run, "run="+second.Run)
	contains(t, "since a time", r.mustSwim("log", "--since", "2000-01-01T00:00:00Z"), "run="+first.Run)
	contains(t, "nothing since", r.mustSwim("log", "--since", "2999-01-01T00:00:00Z"), "nothing in .swim.log since 2999-01-01T00:00:00Z")

	for _, args := range [][]string{{"log", "--since", "yesterday"}, {"log", "1", "--since", "1h"}} {
		if out, code := r.swim(args...); code != 2 {
			t.Errorf("swim %s: exit %d, want 2 (usage)\n%s", strings.Join(args, " "), code, out)
		}
	}
}
