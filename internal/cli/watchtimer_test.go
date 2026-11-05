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
	secs := func(f float64) *float64 { return &f }
	for _, c := range []struct {
		name  string
		lane  *status.Lane
		state string
		want  string
	}{
		{"running", &status.Lane{State: status.Running, StartedAt: at(65 * time.Second)}, "running", "1m 5s"},
		{"waiting", &status.Lane{State: status.Waiting, StartedAt: at(10 * time.Second)}, "waiting", "10s"},
		// The clock stops while the round waits for the operator: 2m run,
		// 30s answered before, 20s of a question still open.
		{"asking", &status.Lane{State: status.Running, StartedAt: at(2 * time.Minute), PausedS: secs(30), PausedAt: now.Add(-20 * time.Second).Format(time.RFC3339Nano)}, "running", "1m 10s"},
		{"passed", &status.Lane{State: "passed", StartedAt: at(4 * time.Hour), FinishedAt: at(3 * time.Hour)}, "passed", "3h ago"},
		{"ready", &status.Lane{State: "passed", FinishedAt: at(time.Hour)}, "ready", ""},
		{"never ran", nil, "empty", ""},
	} {
		if got := laneTimer(c.lane, c.state, now); got != c.want {
			t.Errorf("%s: laneTimer = %q, want %q", c.name, got, c.want)
		}
	}
	running := &queue.Entry{State: queue.Running, Created: *at(time.Hour), Worker: &queue.Worker{Claimed: *at(90 * time.Second)}}
	if got := queueTimer(running, queue.Running, now); got != "1m 30s" {
		t.Errorf("running batch: %q", got)
	}
	waiting := &queue.Entry{State: queue.Queued, Created: *at(3 * time.Minute)}
	if got := queueTimer(waiting, queue.Queued, now); got != "3m ago" {
		t.Errorf("waiting batch: %q", got)
	}
}

// A finished lane's row says how long its round took, whatever its end;
// a pending one, how its lane's last round ended and took; a failed one,
// its first failed step with its why.
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
		{"passed", &status.Lane{State: status.Passed, DurationS: secs(252)}, status.Passed, "took 4m 12s"},
		{"failed", &status.Lane{State: status.Failed, DurationS: secs(3)}, status.Failed, "took 3.0s"},
		{"failed, with its first step's why", &status.Lane{State: status.Failed, DurationS: secs(3),
			FailedSteps: []string{"FAIL  go test (exit 1): --- FAIL: TestX: x_test.go:4: got 1", "STOP  gate failed: go test"}},
			status.Failed, "took 3.0s" + "FAIL  go test (exit 1): --- FAIL: TestX: x_test.go:4: got 1"},
		{"interrupted, no duration", &status.Lane{State: status.Interrupted, StartedAt: at(2 * time.Hour), FinishedAt: at(time.Hour)}, status.Interrupted, "took 1h 0m"},
		{"skipped", &status.Lane{State: status.Skipped, DurationS: secs(0.4)}, status.Skipped, "took 0.4s"},
		{"pending after a failure", &status.Lane{State: status.Failed, DurationS: secs(65)}, "ready", "last round failed, took 1m 5s"},
		{"pending, never ran", &status.Lane{State: status.Empty}, "ready", ""},
		{"running", &status.Lane{State: status.Running, StartedAt: at(time.Minute)}, status.Running, ""},
		{"no times", &status.Lane{State: status.Passed}, status.Passed, ""},
	} {
		got := laneTook(c.lane, c.state) + laneRowDetail(c.lane, c.state) // by state: one of them, or both
		if got != c.want {
			t.Errorf("%s: took/detail = %q, want %q", c.name, got, c.want)
		}
	}
}
