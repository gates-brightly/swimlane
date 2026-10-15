package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/fsutil"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/queue"
	"github.com/gates-brightly/swimlane/internal/schedule"
	"github.com/gates-brightly/swimlane/internal/sign"
)

// A recurring schedule (swim schedule ... --daily) is a template, never
// claimed itself: each day a worker makes that day's occurrence, an
// ordinary scheduled batch, from it. It is pinned like a batch (each
// occurrence checks its lanes still hold the scripts and manifests it
// pinned, and pauses it if not), and expires like a standing approval.

const recurringPrefix = "r-"

// defaultRecurring is how long a recurring schedule lasts unless --until
// or --for says otherwise.
const defaultRecurring = 30 * 24 * time.Hour

type recurring struct {
	Schema   string       `yaml:"schema"`
	ID       string       `yaml:"id"`
	Created  string       `yaml:"created"`
	By       string       `yaml:"by"`
	Note     string       `yaml:"note,omitempty"`
	Lanes    []queue.Lane `yaml:"lanes"`
	Approve  []string     `yaml:"approve,omitempty"`
	Parallel *int         `yaml:"parallel,omitempty"`
	Clock    string       `yaml:"clock"` // wall clock in Zone, 06:00:00
	Zone     string       `yaml:"zone"`
	First    string       `yaml:"first"`            // the first occurrence's date (in Zone)
	Until    string       `yaml:"until"`            // the last date it may run
	Last     string       `yaml:"last,omitempty"`   // the last occurrence handled (made or skipped)
	Paused   string       `yaml:"paused,omitempty"` // why, while paused
	// The scheduler's signature over Statement.
	QueuedKey string `yaml:"queued_key,omitempty"`
	QueueSig  string `yaml:"queue_sig,omitempty"`

	path string
}

func recurringDir(root string) string { return filepath.Join(queue.Dir(root), "recurring") }

// Statement is what the scheduler signs: the lanes as pinned, the
// approvals, the time and the expiry.
func (r *recurring) Statement() string {
	var b strings.Builder
	fmt.Fprintf(&b, "swim recurring v1\nid: %s\n", r.ID)
	for _, l := range r.Lanes {
		fmt.Fprintf(&b, "lane: %d %s %s %s\n", l.Lane, l.Job, l.SHA256, l.Manifest)
	}
	fmt.Fprintf(&b, "approve: %s\ndaily: %s %s\nfirst: %s\nuntil: %s\n", strings.Join(r.Approve, ","), r.Clock, r.Zone, r.First, r.Until)
	return b.String()
}

func (r *recurring) save() error {
	r.Schema = "swim.recurring/v1"
	data, err := yaml.Marshal(r)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return err
	}
	return fsutil.WriteAtomic(r.path, data, 0o644)
}

func loadRecurring(root string) ([]*recurring, error) {
	files, _ := filepath.Glob(filepath.Join(recurringDir(root), recurringPrefix+"*.yml"))
	var out []*recurring
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var r recurring
		if err := yaml.Unmarshal(data, &r); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		r.path = f
		out = append(out, &r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func findRecurring(root, id string) (*recurring, error) {
	rs, err := loadRecurring(root)
	if err != nil {
		return nil, err
	}
	var hits []*recurring
	for _, r := range rs {
		if r.ID == id || strings.HasPrefix(r.ID, id) && len(id) >= 6 {
			hits = append(hits, r)
		}
	}
	switch len(hits) {
	case 0:
		return nil, fmt.Errorf("no recurring schedule %q (swim schedule list)", id)
	case 1:
		return hits[0], nil
	}
	return nil, fmt.Errorf("%q matches %d recurring schedules; give more of the id", id, len(hits))
}

func (r *recurring) loc() *time.Location {
	loc, err := schedule.Zone(r.Zone)
	if err != nil {
		return time.UTC
	}
	return loc
}

func (r *recurring) laneNums() []int {
	var ns []int
	for _, l := range r.Lanes {
		ns = append(ns, l.Lane)
	}
	return ns
}

// at says the daily time in its zone and UTC, today's.
func (r *recurring) at(now time.Time) string {
	c, _ := schedule.ParseClock(r.Clock)
	t, _ := schedule.Next(c, r.loc(), now)
	return schedule.Format(t, r.loc())
}

// line is the template's row in swim schedule list.
func (r *recurring) line(now time.Time) string {
	s := fmt.Sprintf("%-29s %s  daily at %s, until %s", r.ID, lanesText(r.laneNums()), r.at(now), r.Until)
	if r.Paused != "" {
		return s + " · paused: " + r.Paused
	}
	c, _ := schedule.ParseClock(r.Clock)
	if next, _ := schedule.Next(c, r.loc(), now); next.In(r.loc()).Format("2006-01-02") <= r.Until {
		return s + " · next " + schedule.Until(next, now)
	}
	return s + " · expired"
}

func recurringAdd(root string, cfg *config.Config, lanes []int, when string, loc *time.Location, f scheduleFlags) error {
	if f.until != "" && f.forD != "" {
		return usagef("give --until DATE or --for D, not both")
	}
	c, err := schedule.ParseClock(when)
	if err != nil {
		return usagef("--daily takes a time of day: %v", err)
	}
	now := scheduleNow()
	first, _ := schedule.Next(c, loc, now)
	// It expires, as standing approvals do, and never later than their
	// cap: the approvals it carries are approvals for every day.
	lk, _ := lookup.Load(root)
	max := time.Duration(0)
	if lk != nil {
		max = lk.MaxStanding
	}
	var until time.Time
	switch {
	case f.until != "":
		d, err := time.ParseInLocation("2006-01-02", f.until, loc)
		if err != nil {
			return usagef("--until %q: a date, 2026-11-14", f.until)
		}
		until = d.AddDate(0, 0, 1).Add(-time.Second)
	case f.forD != "":
		d, err := lookup.ParseLifetime(f.forD)
		if err != nil || d <= 0 {
			return usagef("--for %q: a duration like 30d or 72h", f.forD)
		}
		until = now.Add(d)
	default:
		d := defaultRecurring
		if max > 0 && max < d {
			d = max
		}
		until = now.Add(d)
	}
	if max > 0 && until.After(now.Add(max)) {
		return fmt.Errorf("a recurring schedule lasts at most %s here (lookup.yml standing.max), as standing approvals do: --until %s at the latest", lk.Standing.Max, now.Add(max).In(loc).Format("2006-01-02"))
	}
	if until.Before(first) {
		return fmt.Errorf("it would expire (%s) before it first runs (%s)", until.In(loc).Format("2006-01-02"), first.In(loc).Format("2006-01-02"))
	}
	pinned, approve, _, err := pinBatch(root, cfg, lanes, f.approve)
	if err != nil {
		return err
	}
	r := &recurring{ID: recurringPrefix + strings.TrimPrefix(queue.NewID(), "q-"), Created: now.UTC().Format(time.RFC3339), By: operator(),
		Note: f.note, Lanes: pinned, Approve: approve, Parallel: parallelFlag(f.parallel), Clock: c.String(), Zone: loc.String(),
		First: first.In(loc).Format("2006-01-02"), Until: until.In(loc).Format("2006-01-02")}
	r.path = filepath.Join(recurringDir(root), r.ID+".yml")
	warn := ""
	if key, err := sign.LoadKey(or(f.keyPath, cfg.SigningKey)); err == nil && sign.Available() {
		if sig, err := sign.Sign(key, []byte(r.Statement())); err == nil {
			r.QueuedKey, r.QueueSig = key.Public, sig
			if lk != nil {
				if u, ok := lk.Who(key.Public); ok {
					r.By = u
				}
			}
		} else {
			warn = "not signed (" + err.Error() + ")"
		}
	} else if f.keyPath != "" {
		return err
	}
	if err := r.save(); err != nil {
		return err
	}
	history.Log(root, history.Entry{Event: history.Schedule, Detail: fmt.Sprintf("%s: %s  daily=%s  zone=%s  first=%s  until=%s  approve=%s",
		r.ID, lanesText(r.laneNums()), r.Clock, r.Zone, r.First, r.Until, strings.Join(r.Approve, ","))})
	fmt.Printf("%s scheduled daily at %s, first %s, until %s\n", schedule.Lanes(r.laneNums()), schedule.Format(first, loc), r.First, r.Until)
	fmt.Printf("  %s · swim schedule pause|resume|rm %s\n", r.ID, r.ID)
	if warn != "" {
		fmt.Println("  warning: " + warn)
	}
	if !workerRunning(root) {
		fmt.Println("  warning: no swim watch is running here; nothing will start it (swim watch, or swim watch --headless --detach)")
	}
	return nil
}

// materializeRecurring makes each recurring schedule's latest due
// occurrence, once, as a scheduled batch; swim watch calls it before it
// claims. No catch-up and no stacking: days missed (no worker) are
// recorded as superseded, a day due while the last one still runs as
// overlap, and a template whose lanes changed is paused.
func materializeRecurring(root string, cfg *config.Config) {
	rs, err := loadRecurring(root)
	if err != nil {
		return
	}
	now := scheduleNow()
	for _, r := range rs {
		if r.Paused != "" {
			continue
		}
		loc := r.loc()
		today := now.In(loc).Format("2006-01-02")
		if today > r.Until {
			expireRecurring(root, r)
			continue
		}
		c, err := schedule.ParseClock(r.Clock)
		if err != nil {
			continue
		}
		occ, shifted := schedule.Latest(c, loc, now)
		date := occ.In(loc).Format("2006-01-02")
		if date < r.First || date <= r.Last || date > r.Until {
			continue
		}
		// One worker makes it: the marker is created exclusively.
		marker := filepath.Join(recurringDir(root), r.ID+"."+date+".made")
		f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			continue
		}
		f.Close()
		superseded := missedDays(r, date, loc)
		r.Last = date
		if len(superseded) > 0 {
			history.Log(root, history.Entry{Event: history.ScheduleSkipped, Detail: fmt.Sprintf("%s: skipped: superseded (no worker then): %s", r.ID, strings.Join(superseded, ", "))})
		}
		if why := recurringBlocked(root, cfg, r); why != "" {
			if strings.HasPrefix(why, "overlap") {
				history.Log(root, history.Entry{Event: history.ScheduleSkipped, Detail: fmt.Sprintf("%s: %s skipped: %s", r.ID, date, why)})
			} else {
				r.Paused = why
				history.Log(root, history.Entry{Event: history.SchedulePaused, Detail: fmt.Sprintf("%s: %s skipped, paused: %s", r.ID, date, why)})
			}
			r.save()
			continue
		}
		o := queueOpts{approve: r.Approve, note: r.Note, parallel: r.Parallel, notBefore: occ.UTC().Format(time.RFC3339), zone: r.Zone, from: r.ID, occurrence: date}
		e := &queue.Entry{By: r.By, Note: o.note, Parallel: o.parallel, Lanes: r.Lanes, Approve: r.Approve,
			NotBefore: o.notBefore, Zone: o.zone, From: o.from, Occurrence: o.occurrence, ID: queue.NewID()}
		if r.QueueSig != "" {
			// The schedule's signature vouches for the occurrence (standing
			// approvals of whoever scheduled it apply), as long as the
			// occurrence is what it signed.
			e.QueuedKey, e.QueueSig, e.FromStatement = r.QueuedKey, r.QueueSig, r.Statement()
		}
		if err := queue.Add(root, e); err != nil {
			continue
		}
		detail := fmt.Sprintf("%s: %s  not_before=%s  zone=%s  from=%s %s", e.ID, lanesText(r.laneNums()), e.NotBefore, e.Zone, r.ID, date)
		if shifted {
			detail += "  (the clocks skipped " + r.Clock + " that day: at the first valid minute after)"
		}
		history.Log(root, history.Entry{Event: history.Schedule, Detail: detail})
		r.save()
	}
}

// missedDays are the occurrences between the last one handled and date
// that nobody made (no worker ran then).
func missedDays(r *recurring, date string, loc *time.Location) []string {
	from := r.Last
	if from == "" {
		d, _ := time.ParseInLocation("2006-01-02", r.First, loc)
		from = d.AddDate(0, 0, -1).Format("2006-01-02")
	}
	var out []string
	d, _ := time.ParseInLocation("2006-01-02", from, loc)
	for {
		d = d.AddDate(0, 0, 1)
		s := d.Format("2006-01-02")
		if s >= date {
			return out
		}
		out = append(out, s)
	}
}

// recurringBlocked says why today's occurrence can't be made: the last
// one still waits or runs (overlap), the template's signature or a lane's
// pinned script or manifest no longer matches (it pauses).
func recurringBlocked(root string, cfg *config.Config, r *recurring) string {
	entries, _ := queue.List(root, false)
	for _, e := range entries {
		if e.From == r.ID && (e.State == queue.Queued || e.State == queue.Running) {
			return fmt.Sprintf("overlap: %s (#%d, %s) hasn't finished", e.ID, e.Seq, e.Occurrence)
		}
	}
	if r.QueueSig != "" && sign.Verify(r.QueuedKey, []byte(r.Statement()), r.QueueSig) != nil {
		return "its signature doesn't match its file (edited after it was scheduled)"
	}
	return recurringDrift(root, cfg, r)
}

// recurringDrift says which pinned lane changed ("" when none did).
func recurringDrift(root string, cfg *config.Config, r *recurring) string {
	for _, l := range r.Lanes {
		cur, _, err := pinLane(root, cfg, l.Lane)
		switch {
		case err != nil:
			return fmt.Sprintf("swim %d: %v", l.Lane, err)
		case cur.SHA256 != l.SHA256:
			return fmt.Sprintf("%s changed since it was scheduled", lanesText([]int{l.Lane}))
		case l.Manifest != "" && cur.Manifest != l.Manifest:
			return fmt.Sprintf("swim %d's manifest (what it sources, its sidecar, the runner or swim.yml's policy) changed since it was scheduled", l.Lane)
		}
	}
	return ""
}

func expireRecurring(root string, r *recurring) {
	done := filepath.Join(recurringDir(root), "done")
	os.MkdirAll(done, 0o755)
	if err := os.Rename(r.path, filepath.Join(done, filepath.Base(r.path))); err == nil {
		history.Log(root, history.Entry{Event: history.ScheduleExpired, Detail: fmt.Sprintf("%s: %s expired (until %s); schedule it again to renew", r.ID, lanesText(r.laneNums()), r.Until)})
	}
}

func recurringRm(root, id string) error {
	r, err := findRecurring(root, id)
	if err != nil {
		return err
	}
	if err := os.Remove(r.path); err != nil {
		return err
	}
	history.Log(root, history.Entry{Event: history.ScheduleRm, Detail: fmt.Sprintf("%s: %s, daily at %s", r.ID, lanesText(r.laneNums()), r.Clock)})
	fmt.Printf("removed %s: %s, daily at %s (an occurrence already queued stays: swim schedule rm it too)\n", r.ID, lanesText(r.laneNums()), r.at(scheduleNow()))
	return nil
}

func scheduleSetPaused(pause bool, args []string) error {
	if len(args) != 1 {
		return usagef("usage: swim schedule pause|resume ID")
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	r, err := findRecurring(root, args[0])
	if err != nil {
		return err
	}
	if pause {
		r.Paused = "paused by " + operator()
		history.Log(root, history.Entry{Event: history.SchedulePaused, Detail: r.ID + ": " + r.Paused})
		fmt.Printf("paused %s: no occurrence is made until swim schedule resume %s\n", r.ID, r.ID)
		return r.save()
	}
	if why := recurringDrift(root, cfg, r); why != "" {
		return fmt.Errorf("can't resume %s: %s; schedule it again to pin the lanes as they are now (swim schedule rm %s)", r.ID, why, r.ID)
	}
	r.Paused = ""
	history.Log(root, history.Entry{Event: history.ScheduleResumed, Detail: r.ID})
	fmt.Printf("resumed %s: daily at %s, until %s\n", r.ID, r.at(scheduleNow()), r.Until)
	return r.save()
}

// refuseRecurring refuses to rewrite a lane a recurring schedule runs
// (swim stub, swim new): that would pause it. With force it does, and
// says so.
func refuseRecurring(root string, n int, verb string, force bool) error {
	rs, _ := loadRecurring(root)
	for _, r := range rs {
		if r.Paused != "" {
			continue
		}
		for _, l := range r.Lanes {
			if l.Lane != n {
				continue
			}
			if !force {
				return fmt.Errorf("swim %d runs daily on recurring schedule %s; %s it with --force (which pauses that schedule), or swim schedule rm %s first", n, r.ID, verb, r.ID)
			}
			r.Paused = fmt.Sprintf("swim %s --force on swim %d", verb, n)
			if err := r.save(); err != nil {
				return err
			}
			history.Log(root, history.Entry{Event: history.SchedulePaused, Detail: r.ID + ": " + r.Paused})
			fmt.Fprintf(os.Stderr, "swim: paused recurring schedule %s (swim schedule resume %s once swim %d holds its round again)\n", r.ID, r.ID, n)
		}
	}
	return nil
}

// recurringWarnings are swim doctor's: paused schedules, and ones that
// expire within three days.
func recurringWarnings(root string) []string {
	rs, _ := loadRecurring(root)
	now := scheduleNow()
	var out []string
	for _, r := range rs {
		switch {
		case r.Paused != "":
			out = append(out, fmt.Sprintf("%s (%s) is paused: %s", r.ID, lanesText(r.laneNums()), r.Paused))
		case r.Until <= now.In(r.loc()).AddDate(0, 0, 3).Format("2006-01-02"):
			out = append(out, fmt.Sprintf("%s (%s) expires after %s; schedule it again to renew", r.ID, lanesText(r.laneNums()), r.Until))
		}
	}
	return out
}

// parallelFlag is --parallel N (nil when not given or not a number).
func parallelFlag(s string) *int {
	if n, err := strconv.Atoi(s); err == nil && n >= 0 {
		return &n
	}
	return nil
}
