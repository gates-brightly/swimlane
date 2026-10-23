package schedule

import (
	"testing"
	"time"
)

func TestCron(t *testing.T) {
	chi, _ := time.LoadLocation("America/Chicago")
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, chi)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, c := range []struct{ expr, after, next, latest string }{
		{"0 2 * * *", "2026-10-23 01:00", "2026-10-23 02:00", "2026-10-22 02:00"},
		{"@hourly", "2026-10-23 01:30", "2026-10-23 02:00", "2026-10-23 01:00"},
		{"*/15 9-17 * * mon-fri", "2026-10-23 17:50", "2026-10-26 09:00", "2026-10-23 17:45"}, // Fri 17:50
		{"30 3 1 * *", "2026-10-23 00:00", "2026-11-01 03:30", "2026-10-01 03:30"},
		{"0 0 * * 0", "2026-10-23 00:00", "2026-10-25 00:00", "2026-10-18 00:00"},
		{"0 0 * * 7", "2026-10-23 00:00", "2026-10-25 00:00", "2026-10-18 00:00"},
		{"0 6 13 * fri", "2026-10-23 07:00", "2026-10-30 06:00", "2026-10-23 06:00"}, // dom or dow
		{"0 4 * jan,jul *", "2026-10-23 00:00", "2027-01-01 04:00", "2026-07-31 04:00"},
	} {
		cr, err := ParseCron(c.expr)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		if got := cr.Next(chi, at(c.after)); !got.Equal(at(c.next)) {
			t.Errorf("%s next after %s: %s, want %s", c.expr, c.after, got.In(chi), c.next)
		}
		if got := cr.Latest(chi, at(c.after)); !got.Equal(at(c.latest)) {
			t.Errorf("%s latest at %s: %s, want %s", c.expr, c.after, got.In(chi), c.latest)
		}
	}
	for _, bad := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "5-1 * * * *", "*/0 * * * *", "x * * * *", "0 0 0 * *"} {
		if _, err := ParseCron(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	if c, _ := ParseCron("0 0 30 2 *"); !c.Next(chi, at("2026-01-01 00:00")).IsZero() {
		t.Error("Feb 30 has a next time")
	}
}
