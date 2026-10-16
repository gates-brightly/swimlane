// Package schedule reads and says the times swim schedule takes: a wall
// clock time in a zone ("13:30", "1:30pm", "1330", "tomorrow 06:00",
// "2026-10-20 13:30", "+2h"), and the next daily occurrence of one. Pure
// functions: the caller passes now and the zone.
package schedule

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Forms lists what ParseTime accepts, for error messages.
const Forms = `13:30, 13:30:00, 1330, 133000, 9:30, 1:30pm, 1pm, 12am, tomorrow 06:00, 2026-10-20 13:30, +2h, +90m`

// Regular expressions, kept: the grammar of times people type (4pm,
// 16:30, 1330, 2026-10-20 9am) is what they describe best.
var (
	clockRE   = regexp.MustCompile(`^(\d{1,2})(?::(\d{2}))?(?::(\d{2}))?\s*([ap]\.?m\.?)?$`)
	compactRE = regexp.MustCompile(`^(\d{3,6})\s*([ap]\.?m\.?)?$`)
	dateRE    = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})[ T](.+)$`)
)

// Clock is a wall-clock time of day.
type Clock struct{ H, M, S int }

func (c Clock) String() string { return fmt.Sprintf("%02d:%02d:%02d", c.H, c.M, c.S) }

// ParseClock reads a time of day: 24-hour unless it says am or pm.
func ParseClock(s string) (Clock, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	bad := func(why string) (Clock, error) {
		return Clock{}, fmt.Errorf("%q isn't a time%s (use %s)", s, why, Forms)
	}
	var h, m, sec int
	var ampm string
	if mm := compactRE.FindStringSubmatch(s); mm != nil {
		d := mm[1]
		if len(d)%2 == 1 {
			d = "0" + d // 930 -> 0930
		}
		h, _ = strconv.Atoi(d[0:2])
		m, _ = strconv.Atoi(d[2:4])
		if len(d) == 6 {
			sec, _ = strconv.Atoi(d[4:6])
		}
		ampm = mm[2]
	} else if mm := clockRE.FindStringSubmatch(s); mm != nil {
		h, _ = strconv.Atoi(mm[1])
		if mm[2] == "" && mm[4] == "" {
			return bad(": an hour alone is ambiguous (write 13:00 or 1pm)")
		}
		m, _ = strconv.Atoi(or(mm[2], "0"))
		sec, _ = strconv.Atoi(or(mm[3], "0"))
		ampm = mm[4]
	} else {
		return bad("")
	}
	if ampm != "" {
		if h < 1 || h > 12 {
			return bad(": with am or pm the hour is 1 to 12")
		}
		h %= 12
		if strings.HasPrefix(ampm, "p") {
			h += 12
		}
	}
	if h > 23 || m > 59 || sec > 59 {
		return bad(": out of range")
	}
	return Clock{h, m, sec}, nil
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// At is the moment clock c happens on the date of day in loc. A clock
// that doesn't happen that day (the spring-forward gap) is an error naming
// the nearest valid times; one that happens twice (fall back) is the first.
func At(day time.Time, c Clock, loc *time.Location) (time.Time, error) {
	y, mo, d := day.In(loc).Date()
	t := time.Date(y, mo, d, c.H, c.M, c.S, 0, loc)
	if t.Hour() != c.H || t.Minute() != c.M {
		return time.Time{}, fmt.Errorf("%s doesn't happen on %04d-%02d-%02d in %s (the clocks go forward); use %s or %s",
			c, y, mo, d, loc, t.Add(-time.Hour).Format("15:04"), t.Format("15:04"))
	}
	// The first of two (an hour earlier has the same wall clock).
	if e := t.Add(-time.Hour); e.Hour() == c.H && e.Minute() == c.M {
		t = e
	}
	return t, nil
}

// ParseTime reads when to start: a time of day (today's, or tomorrow's
// when today's has passed: rolled says so), "tomorrow" and a time, a
// date and a time, or +duration from now. A dated time in the past is
// refused.
func ParseTime(s string, now time.Time, loc *time.Location) (t time.Time, rolled bool, err error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "+") {
		d, err := time.ParseDuration(s[1:])
		if err != nil || d <= 0 {
			return time.Time{}, false, fmt.Errorf("%q: a relative time is + and a duration, like +2h or +90m", s)
		}
		return now.Add(d).Truncate(time.Second), false, nil
	}
	if rest, ok := strings.CutPrefix(strings.ToLower(s), "tomorrow "); ok {
		c, err := ParseClock(rest)
		if err != nil {
			return time.Time{}, false, err
		}
		t, err = At(now.In(loc).AddDate(0, 0, 1), c, loc)
		return t, false, err
	}
	if m := dateRE.FindStringSubmatch(s); m != nil {
		day, err := time.ParseInLocation("2006-01-02", m[1], loc)
		if err != nil {
			return time.Time{}, false, fmt.Errorf("%q: %v", m[1], err)
		}
		c, err := ParseClock(m[2])
		if err != nil {
			return time.Time{}, false, err
		}
		if t, err = At(day, c, loc); err != nil {
			return time.Time{}, false, err
		}
		if !t.After(now) {
			return time.Time{}, false, fmt.Errorf("%s has passed", s)
		}
		return t, false, nil
	}
	c, err := ParseClock(s)
	if err != nil {
		return time.Time{}, false, err
	}
	if t, err = At(now, c, loc); err != nil {
		return time.Time{}, false, err
	}
	if !t.After(now) {
		t, err = At(now.In(loc).AddDate(0, 0, 1), c, loc)
		return t, true, err
	}
	return t, false, nil
}

// Next is the first daily occurrence of clock c in loc after after. A day
// whose clock doesn't happen (spring forward) runs at the first valid
// minute after it; shifted says so.
func Next(c Clock, loc *time.Location, after time.Time) (t time.Time, shifted bool) {
	day := after.In(loc)
	for i := 0; i < 3; i++ {
		t, err := At(day, c, loc)
		if err != nil {
			y, mo, d := day.Date()
			t = time.Date(y, mo, d, c.H, c.M, c.S, 0, loc) // Go moves it past the gap
			shifted = true
		}
		if t.After(after) {
			return t, shifted
		}
		day, shifted = day.AddDate(0, 0, 1), false
	}
	return after, false
}

// Format says when t is in loc and in UTC: "13:30:00(CDT) 18:30:00(UTC)",
// the UTC part with its date when that differs from loc's.
func Format(t time.Time, loc *time.Location) string {
	local, utc := t.In(loc), t.UTC()
	zone, off := local.Zone()
	if off == 0 && zone == "UTC" {
		return utc.Format("15:04:05") + "(UTC)"
	}
	s := fmt.Sprintf("%s(%s) ", local.Format("15:04:05"), zone)
	if local.Format("2006-01-02") != utc.Format("2006-01-02") {
		s += utc.Format("2006-01-02 ")
	}
	return s + utc.Format("15:04:05") + "(UTC)"
}

// Day says which day t is, from now, in loc: "" today, " tomorrow
// (2026-10-16)", else " on 2026-10-20".
func Day(t, now time.Time, loc *time.Location) string {
	d, today := t.In(loc).Format("2006-01-02"), now.In(loc).Format("2006-01-02")
	switch d {
	case today:
		return ""
	case now.In(loc).AddDate(0, 0, 1).Format("2006-01-02"):
		return " tomorrow (" + d + ")"
	}
	return " on " + d
}

// Lanes says "Lane 1" or "Lanes 1, 3".
func Lanes(ns []int) string {
	var parts []string
	for _, n := range ns {
		parts = append(parts, strconv.Itoa(n))
	}
	if len(ns) == 1 {
		return "Lane " + parts[0]
	}
	return "Lanes " + strings.Join(parts, ", ")
}

// Reply is the line swim schedule answers with: "Lane 1 scheduled for
// 13:30:00(CDT) 18:30:00(UTC)", with the day when it isn't today.
func Reply(lanes []int, t, now time.Time, loc *time.Location) string {
	return fmt.Sprintf("%s scheduled for %s%s", Lanes(lanes), Format(t, loc), Day(t, now, loc))
}

// Zone is the zone to read times in: name (an IANA name), else the local
// zone.
func Zone(name string) (*time.Location, error) {
	if name == "" {
		return time.Local, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("time zone %q: %v (use an IANA name like America/Chicago)", name, err)
	}
	return loc, nil
}

// Until says how far off t is: "in 2h10m", "now", or "2h10m late".
func Until(t, now time.Time) string {
	d := t.Sub(now).Round(time.Minute)
	switch {
	case d > 0:
		return "in " + short(d)
	case d > -time.Minute:
		return "now"
	}
	return short(-d) + " late"
}

// short is d in minutes: "2h10m", "2h", "45m".
func short(d time.Duration) string {
	s := strings.TrimSuffix(d.String(), "0s") // whole minutes: "2h10m", "2h0m"
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// Latest is the last daily occurrence of clock c in loc at or before now
// (shifted past a spring-forward gap as Next does).
func Latest(c Clock, loc *time.Location, now time.Time) (time.Time, bool) {
	t, shifted := Next(c, loc, now.AddDate(0, 0, -2))
	for {
		n, sh := Next(c, loc, t)
		if n.After(now) {
			return t, shifted
		}
		t, shifted = n, sh
	}
}
