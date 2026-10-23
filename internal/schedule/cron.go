package schedule

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Cron is a five-field cron expression, evaluated in a time zone:
// minute hour day-of-month month day-of-week. Each field is *, a number,
// a range a-b, a list a,b,c, or any of those with a step (*/15, 1-30/2).
// Months and weekdays take names (jan, mon), weekday 0 and 7 are Sunday,
// and @hourly, @daily (@midnight), @weekly, @monthly and @yearly
// (@annually) stand for their usual expressions. As in cron, when both
// day-of-month and day-of-week are restricted, a day matching either runs.
type Cron struct {
	Expr                string
	min, hour, dom, mon []int
	dow                 []int
	domStar, dowStar    bool
}

var cronMacros = map[string]string{
	"@hourly": "0 * * * *", "@daily": "0 0 * * *", "@midnight": "0 0 * * *",
	"@weekly": "0 0 * * 0", "@monthly": "0 0 1 * *", "@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *",
}

var monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
var dayNames = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

// ParseCron reads a cron expression.
func ParseCron(expr string) (*Cron, error) {
	e := strings.TrimSpace(expr)
	if m, ok := cronMacros[strings.ToLower(e)]; ok {
		e = m
	}
	f := strings.Fields(e)
	if len(f) != 5 {
		return nil, fmt.Errorf("cron %q: want 5 fields (minute hour day-of-month month day-of-week), or @hourly, @daily, @weekly, @monthly", expr)
	}
	c := &Cron{Expr: strings.Join(strings.Fields(strings.TrimSpace(expr)), " ")}
	var err error
	specs := []struct {
		name   string
		lo, hi int
		names  map[string]int
		out    *[]int
		star   *bool
	}{
		{"minute", 0, 59, nil, &c.min, nil},
		{"hour", 0, 23, nil, &c.hour, nil},
		{"day of month", 1, 31, nil, &c.dom, &c.domStar},
		{"month", 1, 12, monthNames, &c.mon, nil},
		{"day of week", 0, 7, dayNames, &c.dow, &c.dowStar},
	}
	for i, s := range specs {
		var star bool
		if *s.out, star, err = cronField(f[i], s.lo, s.hi, s.names); err != nil {
			return nil, fmt.Errorf("cron %q: %s: %v", expr, s.name, err)
		}
		if s.star != nil {
			*s.star = star
		}
	}
	for i, d := range c.dow {
		if d == 7 {
			c.dow[i] = 0
		}
	}
	return c, nil
}

// cronField reads one field into its sorted values; star: it starts with
// * (*, */N), which cron counts as unrestricted for the day rule.
func cronField(s string, lo, hi int, names map[string]int) ([]int, bool, error) {
	seen := map[int]bool{}
	for _, part := range strings.Split(s, ",") {
		step := 1
		if base, st, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(st)
			if err != nil || n < 1 {
				return nil, false, fmt.Errorf("bad step %q", st)
			}
			part, step = base, n
		}
		a, b := lo, hi
		switch {
		case part == "*":
		case strings.Contains(part, "-"):
			x, y, _ := strings.Cut(part, "-")
			var err error
			if a, err = cronValue(x, lo, hi, names); err != nil {
				return nil, false, err
			}
			if b, err = cronValue(y, lo, hi, names); err != nil {
				return nil, false, err
			}
			if a > b {
				return nil, false, fmt.Errorf("range %s runs backwards", part)
			}
		default:
			v, err := cronValue(part, lo, hi, names)
			if err != nil {
				return nil, false, err
			}
			a = v
			if step == 1 {
				b = v
			}
		}
		for v := a; v <= b; v += step {
			seen[v] = true
		}
	}
	out := make([]int, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Ints(out)
	return out, strings.HasPrefix(s, "*"), nil
}

func cronValue(s string, lo, hi int, names map[string]int) (int, error) {
	if v, ok := names[strings.ToLower(s)]; ok {
		return v, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q isn't a number", s)
	}
	if v < lo || v > hi {
		return 0, fmt.Errorf("%d is out of range %d-%d", v, lo, hi)
	}
	return v, nil
}

// dayOK reports whether c runs on day d.
func (c *Cron) dayOK(d time.Time) bool {
	if !contains(c.mon, int(d.Month())) {
		return false
	}
	dom, dow := contains(c.dom, d.Day()), contains(c.dow, int(d.Weekday()))
	switch {
	case c.domStar && c.dowStar:
		return true
	case c.domStar:
		return dow
	case c.dowStar:
		return dom
	}
	return dom || dow
}

func contains(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// cronHorizon is how far Next and Latest look: an expression that never
// matches in it (Feb 30) has no occurrence.
const cronHorizon = 5 * 366

// Next is the first time after after that c runs, in loc (zero if none
// within five years). A minute a clock change skips runs at the first
// valid time after it, as Go normalizes it; a repeated hour runs once.
func (c *Cron) Next(loc *time.Location, after time.Time) time.Time {
	a := after.In(loc)
	day := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, loc)
	for i := 0; i < cronHorizon; i++ {
		d := day.AddDate(0, 0, i)
		if !c.dayOK(d) {
			continue
		}
		for _, h := range c.hour {
			for _, m := range c.min {
				t := time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, loc)
				if t.After(after) {
					return t
				}
			}
		}
	}
	return time.Time{}
}

// Latest is the last time at or before now that c ran, in loc (zero if
// none within five years).
func (c *Cron) Latest(loc *time.Location, now time.Time) time.Time {
	n := now.In(loc)
	day := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
	for i := 0; i < cronHorizon; i++ {
		d := day.AddDate(0, 0, -i)
		if !c.dayOK(d) {
			continue
		}
		for hi := len(c.hour) - 1; hi >= 0; hi-- {
			for mi := len(c.min) - 1; mi >= 0; mi-- {
				t := time.Date(d.Year(), d.Month(), d.Day(), c.hour[hi], c.min[mi], 0, 0, loc)
				if !t.After(now) {
					return t
				}
			}
		}
	}
	return time.Time{}
}
