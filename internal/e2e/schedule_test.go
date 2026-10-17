package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

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
	long(t)
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
	long(t)
	r := newRepo(t, "")
	stampLane(r, 1, "one", "")
	stampLane(r, 2, "two", "")
	withHeader(t, r, 2, "# After: 1")
	r.mustSwim("schedule", "1", "+3s")
	r.mustSwim("queue", "add", "2")
	contains(t, "list", r.mustSwim("schedule", "list"), "held", "until #1")
	// swim plan: the scheduled lane, and the lane waiting for it.
	contains(t, "plan", r.mustSwim("plan"), "[@]", "swim 1", "scheduled for ", "[=]", "held: swim 1's pending round has not passed: it is scheduled for")
	var plan struct {
		Lanes []struct {
			Lane   int    `yaml:"lane"`
			Action string `yaml:"action"`
		} `yaml:"lanes"`
	}
	yaml.Unmarshal([]byte(r.mustSwim("plan", "--yaml")), &plan)
	actions := map[int]string{}
	for _, l := range plan.Lanes {
		actions[l.Lane] = l.Action
	}
	if actions[1] != "scheduled" || actions[2] != "held" {
		t.Errorf("plan --yaml actions: %v", actions)
	}
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
	if l := r.status().Get(2); l.State != "skipped" || !strings.Contains(l.Reason, "swim 1's scheduled batch q-") || !strings.Contains(l.Reason, "was removed") {
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

// soon is a time of day a few seconds from now, in UTC (for --tz UTC).
func soon(d time.Duration) string { return time.Now().UTC().Add(d).Format("15:04:05") }

// watchUntil runs swim watch until cond holds, then stops it.
func watchUntil(t *testing.T, r *repo, cond func() bool) string {
	t.Helper()
	w := r.cmd(bin, "watch", "--interval", "200ms", "--no-tui")
	var out strings.Builder
	w.Stdout, w.Stderr = &out, &out
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for !cond() && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	w.Process.Signal(os.Interrupt)
	w.Wait()
	if !cond() {
		t.Fatalf("watch: condition not met\n%s", out.String())
	}
	return out.String()
}

// A daily schedule makes today's occurrence once, a scheduled batch the
// worker runs at its time; a second worker makes nothing more.
func TestRecurringDaily(t *testing.T) {
	long(t)
	r := newRepo(t, "")
	stampLane(r, 1, "one", "")
	out := r.mustSwim("schedule", "1", soon(3*time.Second), "--daily", "--tz", "UTC")
	contains(t, "reply", out, "Lane 1 scheduled daily at ", "(UTC), first ", ", until ", "swim schedule pause|resume|rm r-")
	contains(t, "list", r.mustSwim("schedule", "list"), "r-", "daily at", "· next ")
	watchUntil(t, r, func() bool { return len(doneEntries(t, r)) == 1 })
	done := doneEntries(t, r)
	if !strings.HasPrefix(done[0].From, "r-") || done[0].Occurrence == "" || done[0].Result.Result != "passed" {
		t.Errorf("occurrence: %+v %+v", done[0], done[0].Result)
	}
	ranAt(t, r, "one")
	r.mustSwim("watch", "--once")
	if n := len(doneEntries(t, r)); n != 1 {
		t.Errorf("today's occurrence made %d times", n)
	}
	contains(t, ".swim.log", r.mustSwim("log"), "daily=", "from=r-")
}

// Editing a scheduled lane pauses its recurring schedule instead of
// running the new script on the old approvals; resume refuses until it's
// scheduled again. swim stub on it is refused.
func TestRecurringPausesOnChange(t *testing.T) {
	long(t)
	r := newRepo(t, "")
	stampLane(r, 1, "one", "")
	r.mustSwim("schedule", "1", soon(2*time.Second), "--daily", "--tz", "UTC")
	if out, code := r.swim("stub", "1", "idle"); code == 0 || !strings.Contains(out, "runs daily on recurring schedule") {
		t.Errorf("stub of a scheduled lane: exit %d\n%s", code, out)
	}
	stampLane(r, 1, "one edited", "")
	time.Sleep(2500 * time.Millisecond)
	r.mustSwim("watch", "--once")
	if n := len(doneEntries(t, r)); n != 0 {
		t.Fatalf("an occurrence ran on a changed lane (%d)", n)
	}
	list := r.mustSwim("schedule", "list")
	contains(t, "list", list, "paused: swim 1 changed since it was scheduled")
	id := strings.Fields(list[strings.Index(list, "r-"):])[0]
	if out, code := r.swim("schedule", "resume", id); code == 0 || !strings.Contains(out, "schedule it again") {
		t.Errorf("resume over a change: exit %d\n%s", code, out)
	}
	contains(t, "doctor", must(r.swim("doctor")), "[schedule-recurring]", "is paused")
	contains(t, "rm", r.mustSwim("schedule", "rm", id), "removed "+id)
}

// It expires as standing approvals do: 30 days by default, never past
// lookup.yml's standing.max; -y is refused.
func TestRecurringExpiry(t *testing.T) {
	r := newRepo(t, "")
	stampLane(r, 1, "one", "")
	day := func(d int) string { return time.Now().UTC().AddDate(0, 0, d).Format("2006-01-02") }
	contains(t, "default", r.mustSwim("schedule", "1", "06:00", "--daily", "--tz", "UTC"), "until "+day(30))
	r.write("lookup.yml", "standing: {max: 7d}\n", 0o644)
	contains(t, "capped", r.mustSwim("schedule", "1", "07:00", "--daily", "--tz", "UTC"), "until "+day(7))
	if out, code := r.swim("schedule", "1", "08:00", "--daily", "--until", day(20)); code == 0 || !strings.Contains(out, "standing.max") {
		t.Errorf("past standing.max: exit %d\n%s", code, out)
	}
	if out, code := r.swim("schedule", "1", "08:00", "--daily", "-y"); code == 0 {
		t.Errorf("-y on a recurring schedule accepted:\n%s", out)
	}
}

// A signed daily schedule's occurrences carry its signature: they run as
// signed batches (standing approvals of whoever scheduled it apply), and
// one edited off what was signed would be refused.
func TestRecurringSigned(t *testing.T) {
	long(t)
	r, _ := signedRepo(t)
	stampLane(r, 1, "one", "")
	r.mustSwim("schedule", "1", soon(3*time.Second), "--daily", "--tz", "UTC")
	watchUntil(t, r, func() bool { return len(doneEntries(t, r)) == 1 })
	e := doneEntries(t, r)[0]
	if e.QueueSig == "" || e.FromStatement == "" || e.Result.Result != "passed" {
		t.Errorf("occurrence: sig %v statement %v result %+v", e.QueueSig != "", e.FromStatement != "", e.Result)
	}
	if e.Statement() != e.FromStatement {
		t.Error("the occurrence isn't what its schedule signed")
	}
}
