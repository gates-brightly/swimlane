package cli

import (
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/queue"
	"github.com/gates-brightly/swimlane/internal/status"
)

func TestWatchTimers(t *testing.T) {
	now := time.Date(2026, 10, 15, 18, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *string { s := now.Add(-d).Format(time.RFC3339); return &s }
	for _, c := range []struct {
		name  string
		lane  *status.Lane
		state string
		want  string
	}{
		{"running", &status.Lane{State: status.Running, StartedAt: at(65 * time.Second)}, "running", "1m5s"},
		{"waiting", &status.Lane{State: status.Waiting, StartedAt: at(10 * time.Second)}, "waiting", "10s"},
		{"passed", &status.Lane{State: "passed", StartedAt: at(4 * time.Hour), FinishedAt: at(3 * time.Hour)}, "passed", "3h ago"},
		{"pending", &status.Lane{State: "passed", FinishedAt: at(time.Hour)}, "pending", ""},
		{"never ran", nil, "idle", ""},
	} {
		if got := laneTimer(c.lane, c.state, now); got != c.want {
			t.Errorf("%s: laneTimer = %q, want %q", c.name, got, c.want)
		}
	}
	running := &queue.Entry{State: queue.Running, Created: *at(time.Hour), Worker: &queue.Worker{Claimed: *at(90 * time.Second)}}
	if got := queueTimer(running, queue.Running, now); got != "1m30s" {
		t.Errorf("running batch: %q", got)
	}
	waiting := &queue.Entry{State: queue.Queued, Created: *at(3 * time.Minute)}
	if got := queueTimer(waiting, queue.Queued, now); got != "3m ago" {
		t.Errorf("waiting batch: %q", got)
	}
	if got := queueTimer(waiting, "scheduled", now); got != "" {
		t.Errorf("scheduled batch: %q", got)
	}
}

// A finished lane's row says how long its round took, whatever its end;
// a pending one, how its lane's last round ended and took.
func TestWatchRowsSayHowLongTheLastRoundTook(t *testing.T) {
	now := time.Date(2026, 10, 15, 18, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *string { s := now.Add(-d).Format(time.RFC3339); return &s }
	secs := func(f float64) *float64 { return &f }
	for _, c := range []struct {
		name  string
		lane  *status.Lane
		state string
		want  string
	}{
		{"passed", &status.Lane{State: status.Passed, DurationS: secs(252)}, status.Passed, "took 4m12s"},
		{"failed", &status.Lane{State: status.Failed, DurationS: secs(3)}, status.Failed, "took 3s"},
		{"interrupted, no duration", &status.Lane{State: status.Interrupted, StartedAt: at(2 * time.Hour), FinishedAt: at(time.Hour)}, status.Interrupted, "took 1h0m"},
		{"skipped", &status.Lane{State: status.Skipped, DurationS: secs(0.4)}, status.Skipped, "took 0s"},
		{"pending after a failure", &status.Lane{State: status.Failed, DurationS: secs(65)}, "pending", "last run failed, took 1m5s"},
		{"pending, never ran", &status.Lane{State: status.Idle}, "pending", ""},
		{"running", &status.Lane{State: status.Running, StartedAt: at(time.Minute)}, status.Running, ""},
		{"no times", &status.Lane{State: status.Passed}, status.Passed, ""},
	} {
		if got := laneRowDetail(c.lane, c.state); got != c.want {
			t.Errorf("%s: laneRowDetail = %q, want %q", c.name, got, c.want)
		}
	}
}
