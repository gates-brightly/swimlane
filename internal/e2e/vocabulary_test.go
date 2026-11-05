package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/status"
)

// retired are the words swim no longer uses for a lane's state or for
// what it names (docs/reference/concepts.md has the words it does use).
var retired = []struct {
	why string
	re  *regexp.Regexp
}{
	{"an old state word", regexp.MustCompile(`(?i)\b(pending|idle|done|finished|held|scheduled|stale)\b`)},
	{"an old state word", regexp.MustCompile(`\b(blocked|locked)\b`)}, // BLOCKED stays a step marker
	{"the watcher, not a swimmer", regexp.MustCompile(`(?i)\bswimmers?\b`)},
	{"the pinned copy", regexp.MustCompile(`(?i)\bstaged cop(y|ies)\b`)},
	{"lane N, not swim N", regexp.MustCompile(`\bswim \d+([^.\d]|$)`)},
	{"a round id, not a job", regexp.MustCompile(`\bjob[=:]|\bjob [0-9a-f]{6}|# Job:|SWIM_JOB`)},
	{"laneN.log, not agentN.log", regexp.MustCompile(`\bagent\d+\.`)},
}

// One word per thing, in every view: bare swim, swim status, swim watch
// --plain --once, a run's output and summary, a lane log and .swim.log,
// for a repo with a lane in every state, say each state with its word
// (status.States) and never a retired one.
func TestOneWordPerThing(t *testing.T) {
	r := newRepo(t)
	r.shortLane(1, "# Round: passes", "stage check\nstep \"ok\" true")
	r.shortLane(2, "# Round: fails", "stage check\nstep \"no\" false")
	r.shortLane(3, "# Round: after the failure\n# After: 2", "stage check\nstep \"ok\" true")
	r.shortLane(4, "# Round: written, not run", "stage check\nstep \"ok\" true")
	r.shortLane(5, "# Round: in a batch", "stage check\nstep \"ok\" true")
	r.shortLane(6, "# Round: archived", "stage check\nstep \"ok\" true")
	r.shortLane(7, "# Round: interrupted", "stage check\nstep \"ok\" true")
	r.shortLane(8, "# Round: running", "stage check\nstep \"ok\" true")
	r.shortLane(9, "# Round: waits on lane 8\n# After: 8", "stage check\nstep \"ok\" true")

	var views []string
	view := func(what, out string) {
		views = append(views, what+"\n"+out)
	}
	run, _ := r.swim("run", "1", "2", "3", "6", "--plain")
	view("swim run", run)
	r.mustSwim("archive", "6", "shipped")
	r.mustSwim("queue", "5")
	now := status.Now()
	set := func(n int, fn func(l *status.Lane)) {
		if err := status.Update(r.root, n, 9, fn); err != nil {
			t.Fatal(err)
		}
	}
	set(7, func(l *status.Lane) {
		l.State, l.Round, l.StartedAt, l.FinishedAt, l.ExitCode = status.Interrupted, "interrupted", status.Str(now), status.Str(now), status.Int(130)
	})
	view("bare swim", r.mustSwim())
	view("swim status", r.mustSwim("status"))
	view("swim status 6", r.mustSwim("status", "6"))

	watch, _ := r.swim("watch", "--plain", "--once")
	view("swim watch --plain --once", watch)

	// A round running now, and one waiting on it: this process stands in
	// for their lanes.
	set(8, func(l *status.Lane) {
		l.State, l.Round, l.PID, l.StartedAt = status.Running, "running", os.Getpid(), status.Str(time.Now().UTC().Format(time.RFC3339))
	})
	set(9, func(l *status.Lane) {
		l.State, l.Round, l.PID, l.WaitingOn = status.Waiting, "waits on lane 8", os.Getpid(), []int{8}
	})
	view("bare swim, running", r.mustSwim())
	view("swim status, running", r.mustSwim("status"))
	set(8, func(l *status.Lane) { l.State, l.PID = status.Interrupted, 0 })
	set(9, func(l *status.Lane) { l.State, l.PID = status.Skipped, 0 })

	view("swim log 1", r.mustSwim("log", "1"))
	view("swim log 2", r.mustSwim("log", "2"))
	view("lane2.log", readFile(t, r, filepath.Join(".swim", "logs", "lane2.log")))
	view("swim log", r.mustSwim("log"))
	view(".swim.log", readFile(t, r, ".swim.log"))

	all := strings.Join(views, "\n")
	for _, w := range status.States {
		if !regexp.MustCompile(`\b` + w + `\b`).MatchString(all) {
			t.Errorf("no view says %q", w)
		}
	}
	queuePath := regexp.MustCompile(`\.swim/queue/done/\S*`) // a file path, not a word
	for _, v := range views {
		what, text, _ := strings.Cut(v, "\n")
		for i, line := range strings.Split(queuePath.ReplaceAllString(text, ""), "\n") {
			for _, rw := range retired {
				if m := rw.re.FindString(line); m != "" {
					t.Errorf("%s, line %d: %q (%s):\n%s", what, i+1, m, rw.why, line)
				}
			}
		}
	}
	if t.Failed() {
		t.Logf("views:\n%s", all)
	}
}
