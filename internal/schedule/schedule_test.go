package schedule

import (
	"strings"
	"testing"
	"time"
)

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	return loc
}

func TestParseTime(t *testing.T) {
	chi := zone(t, "America/Chicago")
	now := time.Date(2026, 10, 15, 9, 15, 0, 0, chi) // 09:15 CDT
	same := []string{"13:30", "13:30:00", "1330", "133000", "1:30pm", "1:30 pm", "1:30PM"}
	for _, s := range same {
		got, rolled, err := ParseTime(s, now, chi)
		if err != nil || rolled || !got.Equal(time.Date(2026, 10, 15, 13, 30, 0, 0, chi)) {
			t.Errorf("%q: %v %v %v", s, got, rolled, err)
		}
		if r := Reply([]int{1}, got, now, chi); r != "Lane 1 scheduled for 13:30:00(CDT) 18:30:00(UTC)" {
			t.Errorf("%q reply: %q", s, r)
		}
	}
	for s, want := range map[string]string{"930": "09:30", "9:30": "09:30", "1pm": "13:00", "12am": "00:00", "12pm": "12:00", "+2h": "11:15"} {
		got, _, err := ParseTime(s, now, chi)
		if err != nil || got.In(chi).Format("15:04") != want {
			t.Errorf("%q: %v %v, want %s", s, got, err, want)
		}
	}
	// A passed time is tomorrow's.
	got, rolled, err := ParseTime("06:00", now, chi)
	if err != nil || !rolled || !got.Equal(time.Date(2026, 10, 16, 6, 0, 0, 0, chi)) {
		t.Errorf("06:00: %v %v %v", got, rolled, err)
	}
	if r := Reply([]int{1, 3}, got, now, chi); r != "Lanes 1, 3 scheduled for 06:00:00(CDT) 11:00:00(UTC) tomorrow (2026-10-16)" {
		t.Errorf("reply: %q", r)
	}
	if got, _, err := ParseTime("tomorrow 06:00", now, chi); err != nil || got.In(chi).Day() != 16 {
		t.Errorf("tomorrow: %v %v", got, err)
	}
	if got, _, err := ParseTime("2026-10-20 13:30", now, chi); err != nil || Day(got, now, chi) != " on 2026-10-20" {
		t.Errorf("dated: %v %v", got, err)
	}
	// The UTC part carries its date when it differs.
	late, _, _ := ParseTime("21:00", now, chi)
	if f := Format(late, chi); f != "21:00:00(CDT) 2026-10-16 02:00:00(UTC)" {
		t.Errorf("format: %q", f)
	}
	for _, s := range []string{"25:00", "13:61", "1:30xm", "13", "13pm", "2026-10-01 13:30", "+", "+0s", "noon"} {
		if _, _, err := ParseTime(s, now, chi); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestDST(t *testing.T) {
	for _, tc := range []struct{ zone, gapDay, overlapDay string }{
		{"America/Chicago", "2027-03-14", "2026-11-01"},
		{"Europe/London", "2027-03-28", "2026-10-25"},
	} {
		loc := zone(t, tc.zone)
		now := time.Date(2026, 10, 15, 9, 0, 0, 0, loc)
		gapClock := "02:30"
		if tc.zone == "Europe/London" {
			gapClock = "01:30"
		}
		if _, _, err := ParseTime(tc.gapDay+" "+gapClock, now, loc); err == nil || !strings.Contains(err.Error(), "doesn't happen") {
			t.Errorf("%s gap: %v", tc.zone, err)
		}
		// Fall back: the first of the two.
		got, _, err := ParseTime(tc.overlapDay+" 01:30", now, loc)
		if err != nil {
			t.Fatalf("%s overlap: %v", tc.zone, err)
		}
		if again := got.Add(time.Hour); again.In(loc).Format("15:04") != "01:30" {
			t.Errorf("%s overlap took the second 01:30: %v", tc.zone, got)
		}
		// Daily across the gap: the first valid minute after, said.
		c, _ := ParseClock(gapClock)
		day, _ := time.ParseInLocation("2006-01-02", tc.gapDay, loc)
		next, shifted := Next(c, loc, day)
		if !shifted || next.In(loc).Format("2006-01-02") != tc.gapDay {
			t.Errorf("%s daily over the gap: %v %v", tc.zone, next, shifted)
		}
	}
}

func TestNext(t *testing.T) {
	chi := zone(t, "America/Chicago")
	c := Clock{6, 0, 0}
	at := time.Date(2026, 10, 15, 6, 0, 0, 0, chi)
	if n, _ := Next(c, chi, at); !n.Equal(time.Date(2026, 10, 16, 6, 0, 0, 0, chi)) {
		t.Errorf("next after 06:00: %v", n)
	}
	// 06:00 local across the November change stays 06:00 local.
	if n, _ := Next(c, chi, time.Date(2026, 11, 1, 7, 0, 0, 0, chi)); n.In(chi).Format("15:04") != "06:00" || n.UTC().Hour() != 12 {
		t.Errorf("after DST: %v", n)
	}
	if u := Until(at.Add(130*time.Minute), at); u != "in 2h10m" {
		t.Errorf("until: %q", u)
	}
	if u := Until(at, at.Add(2*time.Hour)); u != "2h late" {
		t.Errorf("late: %q", u)
	}
}

func TestLatest(t *testing.T) {
	chi := zone(t, "America/Chicago")
	c := Clock{6, 0, 0}
	for now, want := range map[time.Time]time.Time{
		time.Date(2026, 10, 15, 9, 0, 0, 0, chi): time.Date(2026, 10, 15, 6, 0, 0, 0, chi),
		time.Date(2026, 10, 15, 5, 0, 0, 0, chi): time.Date(2026, 10, 14, 6, 0, 0, 0, chi),
		time.Date(2026, 10, 15, 6, 0, 0, 0, chi): time.Date(2026, 10, 15, 6, 0, 0, 0, chi),
	} {
		if got, _ := Latest(c, chi, now); !got.Equal(want) {
			t.Errorf("Latest at %v = %v, want %v", now, got, want)
		}
	}
}
