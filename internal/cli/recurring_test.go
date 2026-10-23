package cli

import (
	"reflect"
	"testing"
	"time"
)

// A cron schedule's occurrences: keyed by minute, latest and next in its
// zone, and the ones nobody made between the last handled and now.
func TestRecurringCronOccurrences(t *testing.T) {
	r := &recurring{ID: "r-c", Cron: "0 */6 * * *", Zone: "UTC", First: "2026-10-20T00:00"}
	now := time.Date(2026, 10, 21, 13, 5, 0, 0, time.UTC)
	occ, _ := r.latest(now)
	if k := r.key(occ); k != "2026-10-21T12:00" {
		t.Errorf("latest: %s", k)
	}
	if n := r.next(now); !n.Equal(time.Date(2026, 10, 21, 18, 0, 0, 0, time.UTC)) {
		t.Errorf("next: %s", n)
	}
	r.Last = "2026-10-20T18:00"
	if got := missedDays(r, "2026-10-21T12:00", time.UTC); !reflect.DeepEqual(got, []string{"2026-10-21T00:00", "2026-10-21T06:00"}) {
		t.Errorf("missed: %v", got)
	}
	if r.cadence() != "cron 0 */6 * * * (UTC)" || r.untilText() != "until removed" || r.expired(now) {
		t.Errorf("cadence %q, until %q, expired %v", r.cadence(), r.untilText(), r.expired(now))
	}
	r.Until = "2026-10-20"
	if !r.expired(now) {
		t.Error("past its --until, not expired")
	}
	d := &recurring{Clock: "06:00:00", Zone: "UTC"}
	if occ, _ := d.latest(now); d.key(occ) != "2026-10-21" || d.cadence() != "daily at 06:00:00 (UTC)" {
		t.Errorf("daily: %s %s", d.key(occ), d.cadence())
	}
}
