package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/chain"
)

// stampLane writes a lane that records when it ran (UTC, RFC 3339).
func stampLane(r *repo, n int, name, extra string) {
	r.script(n, name, `run "stamp" bash -c 'date -u +%Y-%m-%dT%H:%M:%SZ > `+name+`.ran'`+extra)
}

func ranAt(t *testing.T, r *repo, name string) time.Time {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r.root, name+".ran"))
	if err != nil {
		t.Fatalf("%s didn't run: %v", name, err)
	}
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	return at
}

// A scheduled batch starts no earlier than its time: before it, swim
// status says when, swim run refuses the lane and swim all leaves it; a
// worker then starts it and records the start.
func TestScheduleStartsNoEarlier(t *testing.T) {
	r := newRepo(t, "")
	stampLane(r, 1, "one", "")
	out := r.mustSwim("schedule", "1", "+3s", "--tz", "UTC")
	contains(t, "reply", out, "Lane 1 scheduled for ", "(UTC)", "swim schedule rm q-", "warning: no swim watch is running here")
	l := r.status().Get(1)
	if l.ScheduledFor == "" || !strings.HasPrefix(l.Schedule, "q-") {
		t.Fatalf("status: scheduled_for %q schedule %q", l.ScheduledFor, l.Schedule)
	}
	at, _ := time.Parse(time.RFC3339, l.ScheduledFor)
	contains(t, "status", r.mustSwim("status"), "scheduled ", "(UTC)", l.Schedule)
	if out, code := r.swim("run", "1"); code == 0 || !strings.Contains(out, "swim 1 is scheduled for") {
		t.Errorf("swim run of a scheduled lane: exit %d\n%s", code, out)
	}
	contains(t, "list", r.mustSwim("schedule", "list"), l.Schedule, "swim 1", "(UTC)")

	r.mustSwim("watch", "--once", "--interval", "200ms")
	if ran := ranAt(t, r, "one"); ran.Before(at) {
		t.Errorf("started %s, before its time %s", ran, at)
	}
	if l := r.status().Get(1); l.State != "passed" || l.ScheduledFor != "" {
		t.Errorf("after: state %s scheduled_for %q", l.State, l.ScheduledFor)
	}
	contains(t, ".swim.log", r.mustSwim("log"), "schedule ", "schedule-start", "not_before=")
	links, _ := chain.Read(r.root)
	found := false
	for _, k := range links {
		if k.Type == "run" && k.ScheduledFor == at.UTC().Format(time.RFC3339) {
			found = true
		}
	}
	if !found {
		t.Error("the run's attestation doesn't record its scheduled time")
	}
	contains(t, "verify", r.mustSwim("verify"), "consistent  yes")
}

// A batch queued after a schedule, whose lane depends on a scheduled lane,
// is held until that batch has run; then it runs. Removing the schedule
// releases it, and it's skipped (its dependency never ran).
func TestScheduleHoldsDependents(t *testing.T) {
	r := newRepo(t, "")
	stampLane(r, 1, "one", "")
	stampLane(r, 2, "two", "")
	withHeader(t, r, 2, "# After: 1")
	r.mustSwim("schedule", "1", "+3s")
	r.mustSwim("queue", "add", "2")
	contains(t, "list", r.mustSwim("schedule", "list"), "held", "until #1")
	if l := r.status().Get(2); l.HeldBy == "" {
		t.Errorf("status lane 2 held_by: %q", l.HeldBy)
	}
	r.mustSwim("watch", "--once", "--interval", "200ms")
	if one, two := ranAt(t, r, "one"), ranAt(t, r, "two"); two.Before(one) {
		t.Errorf("swim 2 ran at %s, before swim 1 at %s", two, one)
	}
	if l := r.status().Get(2); l.State != "passed" {
		t.Errorf("swim 2: %s (%s)", l.State, l.Reason)
	}

	// Scheduled again, then removed: the dependent is released and skipped.
	stampLane(r, 1, "one again", "")
	stampLane(r, 2, "two again", "")
	withHeader(t, r, 2, "# After: 1")
	r.mustSwim("schedule", "1", "+1h")
	r.mustSwim("queue", "add", "2")
	contains(t, "rm", r.mustSwim("schedule", "rm", "3"), "removed", "released #4")
	r.swim("watch", "--once", "--interval", "200ms") // exits 1: the batch skipped its lane
	if l := r.status().Get(2); l.State != "skipped" || !strings.Contains(l.Reason, "swim 1's pending round has not passed") {
		t.Errorf("released dependent: %s (%s)", l.State, l.Reason)
	}
}

// Refused, never guessed: -y, a time that isn't one, a dated time past;
// swim queue add --at is the same command.
func TestScheduleRefusals(t *testing.T) {
	r := newRepo(t, "")
	stampLane(r, 1, "one", "")
	for _, args := range [][]string{{"schedule", "1", "13:30", "-y"}, {"schedule", "1", "25:00"}, {"schedule", "1", "13"}, {"schedule", "1", "2020-01-01", "13:30"}} {
		if out, code := r.swim(args...); code == 0 {
			t.Errorf("%v accepted:\n%s", args, out)
		}
	}
	contains(t, "--at", r.mustSwim("queue", "add", "1", "--at", "+1h"), "Lane 1 scheduled for")
}

// Moving a signed schedule's time in its file voids it: the worker refuses
// the batch rather than run it at a time nobody signed.
func TestScheduleTamperedRefused(t *testing.T) {
	r, _ := signedRepo(t)
	stampLane(r, 1, "one", "")
	r.mustSwim("schedule", "1", "+1h")
	files, _ := filepath.Glob(filepath.Join(r.root, ".swim", "queue", "*.queue"))
	if len(files) != 1 {
		t.Fatalf("entries: %v", files)
	}
	data, _ := os.ReadFile(files[0])
	if !strings.Contains(string(data), "queue_sig:") {
		t.Fatalf("not signed:\n%s", data)
	}
	early := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	lines := strings.Split(string(data), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "not_before:") {
			lines[i] = "not_before: \"" + early + "\""
		}
	}
	os.WriteFile(files[0], []byte(strings.Join(lines, "\n")), 0o644)
	out, code := r.swim("watch", "--once", "--interval", "200ms")
	if code == 0 {
		t.Errorf("watch with a refused batch: exit 0")
	}
	contains(t, "watch", out, "signature doesn't match its file")
	if _, err := os.Stat(filepath.Join(r.root, "one.ran")); err == nil {
		t.Error("the tampered batch ran")
	}
}
