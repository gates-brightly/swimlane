package hub

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Across every worker: Activity, the runs of the last day (or week, or
// hour), filterable by status, repo, trigger and worker, newest first;
// and Jobs, one row a job (a worker's lane running one round title: a lane
// rewritten for another job is another row), with its last result, its
// success rate and its recent durations.

// Run statuses as Activity and Jobs say them: a run's result, but a run
// that passed with a change only dry-run is "dry run".
const StatusDryRun = "dry run"

// ActivityRow is one run on the Activity page.
type ActivityRow struct {
	RunInfo
	Status    string `json:"status"`
	Name      string `json:"name"`
	Host      string `json:"host"`
	Namespace string `json:"namespace,omitempty"`
}

// runStatus is info's result, "dry run" for a pass with a change dry-run.
func runStatus(info RunInfo) string {
	if info.Result == "passed" {
		for _, l := range info.LaneResults {
			if l.State == "passed" && len(l.DryRun) > 0 {
				return StatusDryRun
			}
		}
	}
	return info.Result
}

// laneStatus is how lane l ended, "dry run" for a pass with a dry run.
func laneStatus(l LaneResult) string {
	if l.State == "passed" && len(l.DryRun) > 0 {
		return StatusDryRun
	}
	return l.State
}

// allRuns are every worker's kept runs, with who ran them.
func (s *Server) allRuns() []ActivityRow {
	views := map[string]View{}
	for _, v := range s.Workers() {
		views[v.ID] = v
	}
	s.runs.mu.Lock()
	var out []ActivityRow
	for id, runs := range s.runs.index {
		v := views[id]
		for _, info := range runs {
			out = append(out, ActivityRow{RunInfo: *info, Status: runStatus(*info), Name: v.Name, Host: v.Host, Namespace: v.Namespace})
		}
	}
	s.runs.mu.Unlock()
	sort.Slice(out, func(a, b int) bool { return runEnd(out[a].RunInfo).After(runEnd(out[b].RunInfo)) })
	return out
}

func runEnd(i RunInfo) time.Time {
	if i.Finished.IsZero() {
		return i.Started
	}
	return i.Finished
}

// ActivityFilter narrows Activity.
type ActivityFilter struct {
	Since     time.Duration // how far back (0: no limit)
	Status    string        // failed (failed or interrupted), passed, dry run, or "" for any
	Namespace string        // one repo ("-": runs of workers with none)
	Trigger   string        // schedule, hub, queue, cli
	Worker    string
}

// Activity are the runs filter lets through, newest first.
func (s *Server) Activity(f ActivityFilter) []ActivityRow {
	now := s.now()
	out := []ActivityRow{}
	for _, r := range s.allRuns() {
		if f.Since > 0 && now.Sub(runEnd(r.RunInfo)) > f.Since {
			continue
		}
		switch f.Status {
		case "":
		case "failed":
			if r.Status != "failed" && r.Status != "interrupted" {
				continue
			}
		default:
			if r.Status != f.Status {
				continue
			}
		}
		if f.Namespace != "" && nsKey(r.Namespace) != f.Namespace {
			continue
		}
		if f.Trigger != "" && (r.Trigger == nil || r.Trigger.Kind != f.Trigger) {
			continue
		}
		if f.Worker != "" && r.Worker != f.Worker {
			continue
		}
		out = append(out, r)
	}
	return out
}

// JobRow is one job on the Jobs page.
type JobRow struct {
	Worker    string `json:"worker"`
	Name      string `json:"name"`
	Host      string `json:"host"`
	Namespace string `json:"namespace,omitempty"`
	Lane      int    `json:"lane"`
	Round     string `json:"round"`
	Runs      int    `json:"runs"`   // kept runs it ran in
	Passed    int    `json:"passed"` // of them, passed (not dry-run)
	// Last is its newest run: when it ended, how, its exit code.
	LastRun    string    `json:"last_run"`
	LastStatus string    `json:"last_status"`
	LastExit   *int      `json:"last_exit,omitempty"`
	LastAt     time.Time `json:"last_at"`
	LastPass   time.Time `json:"last_pass,omitempty"`
	// Recent are its last runs' durations and statuses, oldest first.
	Recent []JobPoint `json:"recent"`
}

// JobPoint is one run of a job, for its trend.
type JobPoint struct {
	Run      string    `json:"run"`
	At       time.Time `json:"at"`
	Status   string    `json:"status"`
	Duration float64   `json:"duration_s"`
}

// JobTrend is how many runs a job's trend shows.
const JobTrend = 20

// Jobs are every worker's jobs from their kept runs, failing ones first,
// then by repo, worker and lane.
func (s *Server) Jobs(ns string) []JobRow {
	rows := map[string]*JobRow{}
	runs := s.allRuns()
	for i := len(runs) - 1; i >= 0; i-- { // oldest first
		r := runs[i]
		if ns != "" && nsKey(r.Namespace) != ns {
			continue
		}
		for _, l := range r.LaneResults {
			if l.State == "skipped" {
				continue // it didn't run: no result of its own
			}
			k := r.Worker + "\x00" + strconv.Itoa(l.Lane) + "\x00" + l.Round
			j := rows[k]
			if j == nil {
				j = &JobRow{Worker: r.Worker, Name: r.Name, Host: r.Host, Namespace: r.Namespace, Lane: l.Lane, Round: l.Round}
				rows[k] = j
			}
			st, at := laneStatus(l), runEnd(r.RunInfo)
			j.Runs++
			if st == "passed" {
				j.Passed++
				j.LastPass = at
			}
			j.LastRun, j.LastStatus, j.LastExit, j.LastAt = r.Run, st, l.Exit, at
			j.Recent = append(j.Recent, JobPoint{Run: r.Run, At: at, Status: st, Duration: l.Duration})
			if len(j.Recent) > JobTrend {
				j.Recent = j.Recent[1:]
			}
		}
	}
	out := []JobRow{}
	for _, j := range rows {
		out = append(out, *j)
	}
	bad := func(j JobRow) int {
		switch j.LastStatus {
		case "failed", "interrupted":
			return 0
		case StatusDryRun:
			return 1
		}
		return 2
	}
	sort.Slice(out, func(a, b int) bool {
		x, y := out[a], out[b]
		if bad(x) != bad(y) {
			return bad(x) < bad(y)
		}
		if x.Namespace != y.Namespace {
			return x.Namespace < y.Namespace
		}
		if x.Name != y.Name {
			return x.Name < y.Name
		}
		if x.Lane != y.Lane {
			return x.Lane < y.Lane
		}
		return x.LastAt.After(y.LastAt)
	})
	return out
}

// sinceParam reads ?since=24h|7d|30d (default def; "all": no limit).
func sinceParam(v string, def time.Duration) time.Duration {
	switch {
	case v == "":
		return def
	case v == "all":
		return 0
	case strings.HasSuffix(v, "d"):
		if n, err := strconv.Atoi(strings.TrimSuffix(v, "d")); err == nil && n > 0 {
			return time.Duration(n) * 24 * time.Hour
		}
	default:
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

func (s *Server) activityAPI(w http.ResponseWriter, r *http.Request) {
	if !s.viewable(w, r) {
		return
	}
	q := r.URL.Query()
	rows := s.Activity(ActivityFilter{Since: sinceParam(q.Get("since"), 24*time.Hour), Status: q.Get("status"),
		Namespace: q.Get("ns"), Trigger: q.Get("trigger"), Worker: q.Get("worker")})
	limit := 500
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n < limit {
		limit = n
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	writeJSON(w, map[string]any{"schema": APISchema, "runs": rows, "more": more})
}

func (s *Server) jobsAPI(w http.ResponseWriter, r *http.Request) {
	if !s.viewable(w, r) {
		return
	}
	writeJSON(w, map[string]any{"schema": APISchema, "jobs": s.Jobs(r.URL.Query().Get("ns"))})
}
