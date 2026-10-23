package hub

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/gates-brightly/swimlane/internal/ui"
)

// Recurring schedules on the hub: each worker reports its schedules (the
// templates swim watch makes occurrences from), and the hub says how each
// stands, chiefly whether an occurrence that was due didn't run: the
// worker was down when it came, or it was skipped. That is what a nightly
// job's owner most needs to hear.

// MaxSchedules is how many schedules a report carries.
const MaxSchedules = 200

// ScheduleGrace is how late an occurrence may start before the hub calls
// it missed: the worker makes it at its next poll, and it may wait for
// another batch's lanes.
const ScheduleGrace = 10 * time.Minute

// Schedule is one recurring schedule, as its worker reports it.
type Schedule struct {
	ID      string   `json:"id"`
	Lanes   []int    `json:"lanes"`
	Rounds  []string `json:"rounds,omitempty"` // each lane's round title, in Lanes' order
	Cadence string   `json:"cadence"`          // "daily at 02:00:00 (UTC)", "cron 0 */6 * * * (UTC)"
	By      string   `json:"by,omitempty"`
	Note    string   `json:"note,omitempty"`
	// Next is the next occurrence not yet made (RFC 3339): one that is due
	// now if the worker hasn't made it yet. "" if it never runs again.
	Next    string `json:"next,omitempty"`
	Until   string `json:"until,omitempty"`   // the last date it may run ("": until removed)
	Expired bool   `json:"expired,omitempty"` // past Until: it no longer runs
	Paused  string `json:"paused,omitempty"`  // why, while paused
	// Last is the newest occurrence made (its batch, and its run once it
	// ran); LastSuccess when an occurrence last passed.
	Last        *ScheduleRun `json:"last,omitempty"`
	LastSuccess string       `json:"last_success,omitempty"`
	// Skipped are its latest occurrences nobody ran: "2026-10-21: why".
	Skipped []string `json:"skipped,omitempty"`

	// Status and Why are the hub's verdict (ScheduleStatus).
	Status string `json:"status,omitempty"`
	Why    string `json:"why,omitempty"`
}

// ScheduleRun is one occurrence a schedule made.
type ScheduleRun struct {
	Occurrence string `json:"occurrence"`
	Batch      string `json:"batch"`
	State      string `json:"state"`            // queued, running, done
	Result     string `json:"result,omitempty"` // passed, failed, interrupted, error
	Run        string `json:"run,omitempty"`
	Finished   string `json:"finished,omitempty"`
}

func (s *Schedule) sanitize() {
	for _, f := range []*string{&s.ID, &s.Cadence, &s.By, &s.Note, &s.Next, &s.Until, &s.Paused, &s.LastSuccess} {
		*f = ui.Safe(*f)
	}
	s.Rounds, s.Skipped = safeAll(s.Rounds), safeAll(s.Skipped)
	if len(s.Skipped) > 10 {
		s.Skipped = s.Skipped[len(s.Skipped)-10:]
	}
	if r := s.Last; r != nil {
		for _, f := range []*string{&r.Occurrence, &r.Batch, &r.State, &r.Result, &r.Run, &r.Finished} {
			*f = ui.Safe(*f)
		}
	}
	s.Status, s.Why = "", ""
}

// Schedule statuses, worst first.
const (
	ScheduleMissed   = "missed"   // an occurrence was due and didn't start
	ScheduleFailed   = "failed"   // its last occurrence failed
	ScheduleSkipped  = "skipped"  // its last occurrence was skipped (overlap, or no worker then)
	ScheduleExpired  = "expired"  // it no longer runs
	SchedulePaused   = "paused"   // it makes no occurrences until resumed
	ScheduleExpiring = "expiring" // it stops within three days
	ScheduleRunning  = "running"
	ScheduleOK       = "ok"
	ScheduleWaiting  = "waiting" // hasn't run yet
)

// ScheduleStatus is how schedule s of worker v stands at now, and why.
func ScheduleStatus(s Schedule, v View, now time.Time) (string, string) {
	if s.Expired {
		return ScheduleExpired, "it expired after " + s.Until + ": it no longer runs; schedule it again to renew"
	}
	if s.Paused != "" {
		return SchedulePaused, s.Paused
	}
	if next, err := time.Parse(time.RFC3339, s.Next); err == nil && now.After(next.Add(ScheduleGrace)) {
		late := shortDur(now.Sub(next))
		if v.Stale {
			return ScheduleMissed, fmt.Sprintf("due %s ago, and %s hasn't reported since %s: is its swim watch running?", late, v.Name, v.Seen.UTC().Format(time.RFC3339))
		}
		return ScheduleMissed, fmt.Sprintf("due %s ago and not started (%s)", late, v.Activity)
	}
	lastOcc := ""
	if s.Last != nil {
		lastOcc = s.Last.Occurrence
	}
	if n := len(s.Skipped); n > 0 && s.Skipped[n-1] > lastOcc {
		return ScheduleSkipped, s.Skipped[n-1]
	}
	if r := s.Last; r != nil {
		switch {
		case r.State == "running":
			return ScheduleRunning, "occurrence " + r.Occurrence + " is running"
		case r.State == "done" && r.Result != "" && r.Result != "passed":
			return ScheduleFailed, "occurrence " + r.Occurrence + " " + r.Result
		}
	}
	if s.Until != "" {
		if end, err := time.Parse("2006-01-02", s.Until); err == nil && end.Before(now.AddDate(0, 0, 3)) {
			return ScheduleExpiring, "it stops after " + s.Until + "; schedule it again to renew"
		}
	}
	if s.Last == nil {
		return ScheduleWaiting, "it hasn't run yet"
	}
	return ScheduleOK, ""
}

// shortDur writes d for people: 40m, 2h05m, 3d4h.
func shortDur(d time.Duration) string {
	m := int(d.Minutes())
	switch {
	case m < 60:
		return fmt.Sprintf("%dm", m)
	case m < 24*60:
		return fmt.Sprintf("%dh%02dm", m/60, m%60)
	}
	return fmt.Sprintf("%dd%dh", m/(24*60), m%(24*60)/60)
}

// ScheduleRow is a schedule on the hub's Schedules page: its worker's
// id, name and namespace with it.
type ScheduleRow struct {
	Schedule
	Worker    string `json:"worker"`
	Name      string `json:"name"`
	Host      string `json:"host"`
	Namespace string `json:"namespace,omitempty"`
}

// Schedules are every worker's schedules with their status, worst first.
func (s *Server) Schedules() []ScheduleRow {
	var out []ScheduleRow
	for _, v := range s.Workers() {
		for _, sc := range v.Schedules {
			out = append(out, ScheduleRow{Schedule: sc, Worker: v.ID, Name: v.Name, Host: v.Host, Namespace: v.Namespace})
		}
	}
	rank := map[string]int{ScheduleMissed: 0, ScheduleFailed: 1, ScheduleSkipped: 2, ScheduleExpired: 3, SchedulePaused: 4, ScheduleExpiring: 5}
	sort.SliceStable(out, func(a, b int) bool {
		ra, oka := rank[out[a].Status]
		rb, okb := rank[out[b].Status]
		if !oka {
			ra = 9
		}
		if !okb {
			rb = 9
		}
		if ra != rb {
			return ra < rb
		}
		return out[a].Next < out[b].Next
	})
	return out
}

func (s *Server) schedulesAPI(w http.ResponseWriter, r *http.Request) {
	if !s.viewable(w, r) {
		return
	}
	rows := s.Schedules()
	if rows == nil {
		rows = []ScheduleRow{}
	}
	writeJSON(w, map[string]any{"schema": APISchema, "schedules": rows, "grace_s": ScheduleGrace.Seconds()})
}
