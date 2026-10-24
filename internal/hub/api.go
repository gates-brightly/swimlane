package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/runrec"
)

// The read API: what the hub knows, as JSON, for its pages and other
// tools. Viewing needs no login unless the hub is private (Private); a
// job's round and its log, which can hold step output, always need a hub
// user (a session or a user's token).
//
//	GET /api/health                                        no login, no data: the hub answers
//	GET /api/me                                            who's logged in (user, role, the session's CSRF token)
//	GET /api/enrollments                                   the namespace tabs and every worker (?ns=NS: one namespace's)
//	GET /api/enrollments/{id}                              one worker's latest snapshot
//	GET /api/enrollments/{id}/config                       user: its effective configuration, read only (masked)
//	GET /api/enrollments/{id}/runs                         its kept runs, newest first (?lane=N: those lane N was in; ?before=RUN&limit=N; next: the RUN for the page after)
//	GET /api/enrollments/{id}/runs/{run}                   a run's record and a summary of each lane's round in it
//	GET /api/enrollments/{id}/runs/{run}/jobs/{lane}       a lane's round in it (stages, steps, exit codes; a user also its output, under hub.logs: stream or once fetched)
//	GET /api/enrollments/{id}/runs/{run}/jobs/{lane}/log   user: its log, as text (Range honored): streamed (hub.logs: stream, as it runs) or asked for
//	GET /api/enrollments/{id}/commands                     user: its recent commands and their outcomes (commands.go)
//	GET /api/enrollments/{id}/questions/{req}              user: a guard waiting for an answer, and the statement to sign (answers.go)
//	GET /api/activity                                      every worker's runs, newest first (?since=24h|7d|all&status=failed|passed|dry run&ns=NS&trigger=hub|queue|cli&worker=ID)
//	GET /api/jobs                                          every job (a worker's lane and round title) with its last result, success rate and recent durations (?ns=NS)
//	GET /api/events                                        server-sent events (events.go)

// APISchema identifies the read API's documents.
const APISchema = "swim.hub/v2"

// Run list page sizes.
const (
	DefaultRunsPage = 50
	MaxRunsPage     = 200
)

// JobSummary is a lane's round in a run, without its steps.
type JobSummary struct {
	Lane      int               `json:"lane"`
	Job       string            `json:"job"`
	Round     string            `json:"round"`
	StartedAt string            `json:"started_at"`
	Result    string            `json:"result"`
	Exit      *int              `json:"exit,omitempty"`
	Duration  string            `json:"duration,omitempty"`
	Counts    logparse.Counts   `json:"counts"`
	Stages    map[string]string `json:"stages"`
	Failed    []string          `json:"failed_steps"`
	// DryRun are its guarded steps that ran as dry runs (SKIP "dry run:
	// ..."), label and why: a lane that passed with one didn't make its
	// change, and the hub shows it amber, not green.
	DryRun []string `json:"dry_run,omitempty"`
}

// DryRuns are doc's guarded steps that ran as dry runs: "label: why".
func DryRuns(d *logparse.Doc) []string {
	var out []string
	for _, r := range d.Results {
		if r.Result == logparse.Skip && strings.HasPrefix(r.Detail, "dry run") {
			out = append(out, r.Label+": "+r.Detail)
		}
	}
	return out
}

func (s *Server) apiRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/health", s.healthAPI)
	mux.HandleFunc("GET /api/me", s.meAPI)
	mux.HandleFunc("GET /api/enrollments", s.enrollmentsAPI)
	mux.HandleFunc("GET /api/enrollments/{id}", s.enrollmentAPI)
	mux.HandleFunc("GET /api/enrollments/{id}/config", s.configAPI)
	mux.HandleFunc("GET /api/activity", s.activityAPI)
	mux.HandleFunc("GET /api/jobs", s.jobsAPI)
	mux.HandleFunc("GET /api/enrollments/{id}/runs", s.runsAPI)
	mux.HandleFunc("GET /api/enrollments/{id}/runs/{run}", s.runAPI)
	mux.HandleFunc("GET /api/enrollments/{id}/runs/{run}/jobs/{lane}", s.jobAPI)
	mux.HandleFunc("GET /api/enrollments/{id}/runs/{run}/jobs/{lane}/log", s.jobLogAPI)
	mux.HandleFunc("GET /api/enrollments/{id}/questions/{req}", s.questionAPI)
	mux.HandleFunc("GET /api/events", s.eventsAPI)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// viewable writes the refusal when r may not view the hub.
func (s *Server) viewable(w http.ResponseWriter, r *http.Request) bool {
	if !s.canView(r) {
		http.Error(w, "this hub is private: log in first (/login), or send a user's token", http.StatusUnauthorized)
		return false
	}
	return true
}

func (s *Server) healthAPI(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"schema": APISchema, "ok": true})
}

func (s *Server) enrollmentsAPI(w http.ResponseWriter, r *http.Request) {
	if !s.viewable(w, r) {
		return
	}
	views := s.Workers()
	ns := nsQuery(r)
	tabs := tabs(views, ns)
	if r.URL.Query().Has("ns") {
		views = inNamespace(views, ns)
	}
	if views == nil {
		views = []View{}
	}
	writeJSON(w, map[string]any{"schema": APISchema, "namespaces": tabs, "enrollments": views})
}

func (s *Server) enrollmentAPI(w http.ResponseWriter, r *http.Request) {
	if !s.viewable(w, r) {
		return
	}
	v, ok := s.worker(r.PathValue("id"))
	if !ok {
		http.Error(w, "no such enrollment", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"schema": APISchema, "enrollment": v})
}

func (s *Server) runsAPI(w http.ResponseWriter, r *http.Request) {
	if !s.viewable(w, r) {
		return
	}
	id := r.PathValue("id")
	runs := s.Runs(id)
	if _, known := s.worker(id); !known && len(runs) == 0 {
		http.Error(w, "no such enrollment", http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	if n, err := strconv.Atoi(q.Get("lane")); err == nil {
		// One lane's history: the runs it was in.
		var only []RunInfo
		for _, info := range runs {
			if info.HasLane(n) {
				only = append(only, info)
			}
		}
		runs = only
	}
	if runs == nil {
		runs = []RunInfo{}
	}
	if before := q.Get("before"); before != "" {
		for i, info := range runs {
			if info.Run == before {
				runs = runs[i+1:]
				break
			}
		}
	}
	limit := DefaultRunsPage
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
		limit = min(n, MaxRunsPage)
	}
	next := ""
	if len(runs) > limit {
		runs, next = runs[:limit], runs[limit-1].Run
	}
	writeJSON(w, map[string]any{"schema": APISchema, "runs": runs, "next": next})
}

// keptRun is the run r names, writing the 404 when there's none.
func (s *Server) keptRun(w http.ResponseWriter, r *http.Request) (*RunReport, bool) {
	rep, ok := s.Run(r.PathValue("id"), r.PathValue("run"))
	if !ok {
		http.Error(w, "no such run on the hub", http.StatusNotFound)
	}
	return rep, ok
}

func (s *Server) runAPI(w http.ResponseWriter, r *http.Request) {
	if !s.viewable(w, r) {
		return
	}
	rep, ok := s.keptRun(w, r)
	if !ok {
		return
	}
	jobs := []JobSummary{}
	for _, d := range rep.Jobs {
		failed := d.Failed
		if failed == nil {
			failed = []string{}
		}
		jobs = append(jobs, JobSummary{Lane: d.Lane, Job: d.Job, Round: d.Round, StartedAt: d.StartedAt, Result: d.Result,
			Exit: d.Exit, Duration: d.Duration, Counts: d.Counts, Stages: d.Stages, Failed: failed, DryRun: DryRuns(&d)})
	}
	writeJSON(w, map[string]any{"schema": APISchema, "result": RunResult(&rep.Run), "run": rep.Run, "jobs": jobs, "trigger": rep.Trigger})
}

// jobLane is the lane r names, writing the 404 when it isn't a number.
func jobLane(w http.ResponseWriter, r *http.Request) (int, bool) {
	n, err := strconv.Atoi(r.PathValue("lane"))
	if err != nil {
		http.Error(w, "a job is a lane number", http.StatusNotFound)
	}
	return n, err == nil
}

func (s *Server) jobAPI(w http.ResponseWriter, r *http.Request) {
	// Anyone who may view the hub sees a job's steps, their commands and
	// exit codes; their output, which can hold anything a step printed,
	// only a hub user.
	if !s.viewable(w, r) {
		return
	}
	user := s.isUser(r)
	n, ok := jobLane(w, r)
	if !ok {
		return
	}
	rep, ok := s.keptRun(w, r)
	if !ok {
		return
	}
	for _, d := range rep.Jobs {
		if d.Lane == n {
			from := ""
			if !user {
				for i := range d.Results {
					d.Results[i].Output = nil
				}
				writeJSON(w, map[string]any{"schema": APISchema, "job": d, "lane": laneOf(&rep.Run, n), "output_hidden": true})
				return
			}
			if !hasOutput(&d) && s.fetchedOutput(r.PathValue("id"), r.PathValue("run"), n, &d) {
				from = "log" // the output came from a log the hub asked the worker for
			}
			writeJSON(w, map[string]any{"schema": APISchema, "job": d, "lane": laneOf(&rep.Run, n), "output_from": from})
			return
		}
	}
	http.Error(w, "that lane has no round in this run", http.StatusNotFound)
}

// hasOutput reports whether any of d's steps carry output.
func hasOutput(d *logparse.Doc) bool {
	for _, res := range d.Results {
		if len(res.Output) > 0 {
			return true
		}
	}
	return false
}

// fetchedOutput fills d's steps' output from lane n's log for run, when a
// hub user asked the worker for it (a log command: the worker masked it):
// a run sent without step output (hub.logs: request) shows it once
// fetched. It reports whether it found the round.
func (s *Server) fetchedOutput(id, run string, n int, d *logparse.Doc) bool {
	data, _, ok := s.logs.get(logKey(id, run, n))
	if !ok {
		return false
	}
	var round *logparse.Round
	for _, rd := range logparse.RoundsIn(string(data)) {
		if rd.Run == run {
			round = rd
		}
	}
	if round == nil {
		return false
	}
	got := round.Doc(n).Results
	if len(got) != len(d.Results) {
		return false
	}
	for i := range got {
		if got[i].Label != d.Results[i].Label {
			return false
		}
	}
	for i := range got {
		d.Results[i].Output = safeAll(got[i].Output)
	}
	return true
}

// laneOf is lane n's part in run r (nil if it had none).
func laneOf(r *runrec.Run, n int) *runrec.Lane {
	for i := range r.Lanes {
		if r.Lanes[i].Lane == n {
			return &r.Lanes[i]
		}
	}
	return nil
}

func (s *Server) jobLogAPI(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireUser(w, r, RoleViewer); !ok {
		return
	}
	n, ok := jobLane(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if f, fi, ok := s.streamedLog(r.PathValue("id"), r.PathValue("run"), n); ok {
		defer f.Close()
		http.ServeContent(w, r, "", fi.ModTime(), f)
		return
	}
	data, at, ok := s.logs.get(logKey(r.PathValue("id"), r.PathValue("run"), n))
	if !ok {
		w.Header().Del("Content-Type")
		http.Error(w, "that log isn't on the hub: ask the swimmer for it (a log command)", http.StatusNotFound)
		return
	}
	http.ServeContent(w, r, "", at, bytes.NewReader(data))
}

// WorkerConfig is worker id's configuration as its last report gave it
// (masked of secrets), and when that report came.
func (s *Server) WorkerConfig(id string) (string, time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.workers[id]
	if !ok {
		return "", time.Time{}, false
	}
	return v.Config, v.Seen, true
}

// configAPI is a worker's configuration, read only, for a hub user: it
// names paths, runners and env defaults, as a log might.
func (s *Server) configAPI(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireUser(w, r, RoleViewer); !ok {
		return
	}
	text, at, ok := s.WorkerConfig(r.PathValue("id"))
	if !ok {
		http.Error(w, "no such enrollment", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"schema": APISchema, "config": text, "at": at.UTC().Format(time.RFC3339)})
}
