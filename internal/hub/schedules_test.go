package hub

import (
	"strings"
	"testing"
	"time"
)

func TestScheduleStatus(t *testing.T) {
	now := time.Date(2026, 10, 23, 2, 30, 0, 0, time.UTC)
	up := View{Snapshot: Snapshot{Worker: Worker{Name: "box"}, Activity: "running swim 2"}, Seen: now}
	down := View{Snapshot: Snapshot{Worker: Worker{Name: "box"}}, Seen: now.Add(-3 * time.Hour), Stale: true}
	done := func(occ, result string) *ScheduleRun {
		return &ScheduleRun{Occurrence: occ, Batch: "q-1", State: "done", Result: result}
	}
	for _, c := range []struct {
		name string
		s    Schedule
		v    View
		want string
		why  string
	}{
		{"due and the worker is down", Schedule{Next: "2026-10-23T02:00:00Z", Last: done("2026-10-22", "passed")}, down, ScheduleMissed, "hasn't reported since"},
		{"due and not started", Schedule{Next: "2026-10-23T02:00:00Z"}, up, ScheduleMissed, "not started (running swim 2)"},
		{"within the grace", Schedule{Next: "2026-10-23T02:25:00Z", Last: done("2026-10-22", "passed")}, up, ScheduleOK, ""},
		{"last failed", Schedule{Next: "2026-10-24T02:00:00Z", Last: done("2026-10-23", "failed")}, up, ScheduleFailed, "2026-10-23 failed"},
		{"skipped after its last run", Schedule{Next: "2026-10-24T02:00:00Z", Last: done("2026-10-21", "passed"), Skipped: []string{"2026-10-22: no worker ran then"}}, up, ScheduleSkipped, "no worker ran then"},
		{"skipped before its last run", Schedule{Next: "2026-10-24T02:00:00Z", Last: done("2026-10-23", "passed"), Skipped: []string{"2026-10-22: overlap"}}, up, ScheduleOK, ""},
		{"expired", Schedule{Expired: true, Until: "2026-10-20"}, up, ScheduleExpired, "no longer runs"},
		{"paused", Schedule{Paused: "swim 1 changed"}, up, SchedulePaused, "swim 1 changed"},
		{"expiring", Schedule{Next: "2026-10-24T02:00:00Z", Until: "2026-10-25", Last: done("2026-10-23", "passed")}, up, ScheduleExpiring, "stops after 2026-10-25"},
		{"never ran", Schedule{Next: "2026-10-24T02:00:00Z"}, up, ScheduleWaiting, ""},
	} {
		got, why := ScheduleStatus(c.s, c.v, now)
		if got != c.want || !strings.Contains(why, c.why) {
			t.Errorf("%s: %s (%s), want %s (%s)", c.name, got, why, c.want, c.why)
		}
	}
}

// Schedules come in each report, and the hub lists them worst first.
func TestSchedulesAPI(t *testing.T) {
	s, _, worker, _, _ := commandHub(t)
	s.now = func() time.Time { return time.Date(2026, 10, 23, 2, 30, 0, 0, time.UTC) }
	worker.Report(Snapshot{Worker: Worker{Name: "billing", Host: "box", Root: "/r"}, Interval: 2, Schedules: []Schedule{
		{ID: "r-ok", Lanes: []int{1}, Cadence: "daily at 06:00:00 (UTC)", Next: "2026-10-23T06:00:00Z", Last: &ScheduleRun{Occurrence: "2026-10-22", State: "done", Result: "passed"}},
		{ID: "r-late", Lanes: []int{2}, Cadence: "daily at 02:00:00 (UTC)", Next: "2026-10-23T02:00:00Z", Status: "ok"},
	}})
	rows := s.Schedules()
	if len(rows) != 2 || rows[0].ID != "r-late" || rows[0].Status != ScheduleMissed || rows[1].Status != ScheduleOK || rows[0].Name != "billing" {
		t.Fatalf("schedules: %+v", rows)
	}
}
