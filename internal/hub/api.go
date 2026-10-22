package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/runrec"
)

// The read API: what the hub knows, as JSON, for its pages and other
// tools. Viewing needs no login unless the hub is private (Private); a
// job's round and its log, which can hold step output, always need a hub
// user (a session or a user's token).
//
//	GET /api/health                                        no login, no data: the hub answers
//	GET /api/enrollments                                   the namespace tabs and every worker (?ns=NS: one namespace's)
//	GET /api/enrollments/{id}                              one worker's latest snapshot
//	GET /api/enrollments/{id}/runs                         its kept runs, newest first (?before=RUN&limit=N; next: the RUN for the page after)
//	GET /api/enrollments/{id}/runs/{run}                   a run's record and a summary of each lane's round in it
//	GET /api/enrollments/{id}/runs/{run}/jobs/{lane}       user: a lane's round in it (stages, steps, output under hub.logs: stream)
//	GET /api/enrollments/{id}/runs/{run}/jobs/{lane}/log   user: its log, as text (Range honored), when it's on the hub
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
}

func (s *Server) apiRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/health", s.healthAPI)
	mux.HandleFunc("GET /api/enrollments", s.enrollmentsAPI)
	mux.HandleFunc("GET /api/enrollments/{id}", s.enrollmentAPI)
	mux.HandleFunc("GET /api/enrollments/{id}/runs", s.runsAPI)
	mux.HandleFunc("GET /api/enrollments/{id}/runs/{run}", s.runAPI)
	mux.HandleFunc("GET /api/enrollments/{id}/runs/{run}/jobs/{lane}", s.jobAPI)
	mux.HandleFunc("GET /api/enrollments/{id}/runs/{run}/jobs/{lane}/log", s.jobLogAPI)
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
			Exit: d.Exit, Duration: d.Duration, Counts: d.Counts, Stages: d.Stages, Failed: failed})
	}
	writeJSON(w, map[string]any{"schema": APISchema, "result": RunResult(&rep.Run), "run": rep.Run, "jobs": jobs})
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
	if _, ok := s.requireUser(w, r, RoleViewer); !ok {
		return
	}
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
			writeJSON(w, map[string]any{"schema": APISchema, "job": d, "lane": laneOf(&rep.Run, n)})
			return
		}
	}
	http.Error(w, "that lane has no round in this run", http.StatusNotFound)
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
	data, at, ok := s.logs.get(logKey(r.PathValue("id"), r.PathValue("run"), n))
	if !ok {
		http.Error(w, "that log isn't on the hub: ask the worker for it (a log command)", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	http.ServeContent(w, r, "", at, bytes.NewReader(data))
}
