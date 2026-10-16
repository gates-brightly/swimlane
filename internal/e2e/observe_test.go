package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// swim observe: a step run six times quick and once slow is one slow
// observation, recorded beside the logs at run end (the chain still
// verifies), shown by swim observe and swim report; one that can't be
// recorded never fails the run.
func TestObservations(t *testing.T) {
	r := newRepo(t, "")
	r.write("swim.yml", "observe:\n  slow_min_s: 0.5\n", 0o644)
	r.script(1, "nap", `run "nap" bash -c 'sleep ${NAP:-0.1}'
run --retry 2 --backoff 0.1s "flaky call" bash -c 'n=$(cat tries 2>/dev/null || echo 0); echo $((n+1)) > tries; [ "$n" -ge 1 ]'`)
	for i := 0; i < 6; i++ {
		os.Remove(filepath.Join(r.root, "tries"))
		r.mustSwim("run", "1", "--plain")
	}
	contains(t, "quick runs", r.mustSwim("observe"), "retried", `"flaky call" passed on attempt 2`)
	if out := r.mustSwim("observe"); strings.Contains(out, "slow") {
		t.Errorf("slow before any slow run:\n%s", out)
	}
	r.mustSwim("report", "--earmark") // the reader is caught up
	os.Remove(filepath.Join(r.root, "tries"))
	r.env = append(r.env, "NAP=2")
	r.mustSwim("run", "1", "--plain")
	out := r.mustSwim("observe")
	contains(t, "observe", out, "slow", `"nap" took 2.`, "usually 0.1s", "(p90 0.1s, 6 runs)")
	contains(t, "observe 1", r.mustSwim("observe", "1"), "slow")
	contains(t, "observe --yaml", r.mustSwim("observe", "--yaml"), "schema: swim.observations/v1", "kind: slow", "lane: 1")
	contains(t, "report", r.mustSwim("report"), "observations  2 (swim observe)", "slow", `"nap" took 2.`)
	contains(t, "verify", r.mustSwim("verify"), "consistent  yes")
	if log := r.log(1); strings.Contains(log, "usually") {
		t.Errorf("an observation reached the lane log:\n%s", log)
	}

	// Observations that can't be written: the run passes, .swim.log warns.
	dir := filepath.Join(r.root, ".swim", "observations")
	os.RemoveAll(dir)
	os.WriteFile(dir, []byte("not a directory"), 0o644)
	r.env = append(r.env, "NAP=0.1")
	os.Remove(filepath.Join(r.root, "tries"))
	if out, code := r.swim("run", "1", "--plain"); code != 0 {
		t.Fatalf("the run failed:\n%s", out)
	}
	contains(t, ".swim.log", r.mustSwim("log"), "warn", "observations not recorded")
}

// A finally lane gets the observations of the lanes it waited for
// (SWIM_OBSERVATIONS), for a write-up; swim observe check finds a
// citation the run doesn't have.
func TestObservationsForFinally(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "call", `run --retry 2 --backoff 0.1s "flaky call" bash -c 'n=$(cat tries 2>/dev/null || echo 0); echo $((n+1)) > tries; [ "$n" -ge 1 ]'`)
	r.script(2, "write up", `run "read the observations" bash -c 'cat "$SWIM_OBSERVATIONS"'`)
	withHeader(t, r, 2, "# Finally: 1")
	r.mustSwim("run", "1", "2", "--plain")
	contains(t, "finally lane", r.log(2), "PASS  read the observations", "schema: swim.observations/v1", "kind: retried", "id: retried-1-1")

	r.write("writeup.md", "The call was flaky (retried-1-1), and slow too (slow-1-9).\n", 0o644)
	out, code := r.swim("observe", "check", "writeup.md")
	if code != 1 {
		t.Errorf("check: exit %d\n%s", code, out)
	}
	contains(t, "check", out, "cites 2 observations", "not in the run: slow-1-9")
	r.write("writeup.md", "The call was flaky (retried-1-1).\n", 0o644)
	contains(t, "check ok", r.mustSwim("observe", "check", "writeup.md"), "each one is there")
}
