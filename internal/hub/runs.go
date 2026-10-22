package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gates-brightly/swimlane/internal/chars"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/runrec"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// Finished runs a worker reports (PUT /api/workers/{id}/runs/{run}): the
// run's record (runrec: its lanes, their After graph and timings) and each
// lane's round (logparse.Doc: stages, steps, commands, output), masked of
// secrets by the worker and sanitized again here. The hub keeps them on
// disk under its state dir, one file a run (runs/<worker>/<run>.json),
// with an index in memory built at start. Retention drops runs that ended
// longer ago than the age limit, then the oldest until the total fits.

// Run limits.
const (
	RunMax                = 8 << 20 // one run's report
	DefaultRunRetain      = 30 * 24 * time.Hour
	DefaultRunRetainBytes = 2 << 30
)

// RunReport is one finished run as a worker sends it.
type RunReport struct {
	Run  runrec.Run     `json:"run"`
	Jobs []logparse.Doc `json:"jobs"` // the round each lane ran in it (lanes with none, such as skipped ones, have none)
}

// RunInfo is a kept run as listed.
type RunInfo struct {
	Worker   string    `json:"worker"`
	Run      string    `json:"run"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	Result   string    `json:"result"` // passed | failed | interrupted | skipped
	Lanes    []int     `json:"lanes"`
	Bytes    int64     `json:"bytes"`
}

// RunResult is a run's overall result from its lanes': failed over
// interrupted over passed; skipped when nothing ran.
func RunResult(r *runrec.Run) string {
	res := "skipped"
	for _, l := range r.Lanes {
		switch {
		case l.State == "failed":
			return "failed"
		case l.State == "interrupted":
			res = "interrupted"
		case l.State == "passed" && res == "skipped":
			res = "passed"
		}
	}
	return res
}

// Sanitize makes every text field safe to show (ui.Safe), as Snapshot's.
func (r *RunReport) Sanitize() {
	r.Run.Run = ui.Safe(r.Run.Run)
	for i := range r.Run.Lanes {
		l := &r.Run.Lanes[i]
		l.Job, l.Round, l.State, l.Reason = ui.Safe(l.Job), ui.Safe(l.Round), ui.Safe(l.State), ui.Safe(l.Reason)
		l.Locks = safeAll(l.Locks)
	}
	for i := range r.Jobs {
		d := &r.Jobs[i]
		for _, f := range []*string{&d.Schema, &d.Job, &d.Round, &d.StartedAt, &d.Run, &d.Result, &d.Duration} {
			*f = ui.Safe(*f)
		}
		d.Context, d.Stages, d.Handlers = safeMap(d.Context), safeMap(d.Stages), safeMap(d.Handlers)
		d.Failed = safeAll(d.Failed)
		for j := range d.Results {
			res := &d.Results[j]
			for _, f := range []*string{&res.Result, &res.Label, &res.Detail, &res.Stage, &res.Handler, &res.Clock, &res.Command, &res.Saved} {
				*f = ui.Safe(*f)
			}
			res.Output, res.Messages = safeAll(res.Output), safeAll(res.Messages)
		}
	}
}

func safeMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[ui.Safe(k)] = ui.Safe(v)
	}
	return out
}

type runStore struct {
	mu       sync.Mutex
	dir      string // "" keeps nothing
	retain   time.Duration
	maxBytes int64
	now      func() time.Time
	index    map[string]map[string]*RunInfo // worker -> run -> info
	total    int64
}

func newRunStore(now func() time.Time) *runStore {
	return &runStore{retain: DefaultRunRetain, maxBytes: DefaultRunRetainBytes, now: now, index: map[string]map[string]*RunInfo{}}
}

func (rs *runStore) path(worker, run string) string {
	return filepath.Join(rs.dir, worker, run+".json")
}

// open keeps runs under dir, indexing those already there.
func (rs *runStore) open(dir string) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	rs.dir = dir
	files, _ := filepath.Glob(filepath.Join(dir, "*", "*.json"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var rep RunReport
		if json.Unmarshal(data, &rep) != nil || rep.Run.Run+".json" != filepath.Base(f) {
			continue
		}
		rs.add(filepath.Base(filepath.Dir(f)), &rep, int64(len(data)))
	}
	rs.prune()
	return nil
}

// add indexes a run. rs.mu is held.
func (rs *runStore) add(worker string, rep *RunReport, size int64) {
	runs := rs.index[worker]
	if runs == nil {
		runs = map[string]*RunInfo{}
		rs.index[worker] = runs
	}
	if old := runs[rep.Run.Run]; old != nil {
		rs.total -= old.Bytes
	}
	info := &RunInfo{Worker: worker, Run: rep.Run.Run, Started: rep.Run.Started, Finished: rep.Run.Finished,
		Result: RunResult(&rep.Run), Lanes: []int{}, Bytes: size}
	for _, l := range rep.Run.Lanes {
		info.Lanes = append(info.Lanes, l.Lane)
	}
	runs[rep.Run.Run] = info
	rs.total += size
}

// drop forgets a run and deletes its file. rs.mu is held.
func (rs *runStore) drop(info *RunInfo) {
	os.Remove(rs.path(info.Worker, info.Run))
	delete(rs.index[info.Worker], info.Run)
	if len(rs.index[info.Worker]) == 0 {
		delete(rs.index, info.Worker)
		os.Remove(filepath.Join(rs.dir, info.Worker))
	}
	rs.total -= info.Bytes
}

// ended is when a run ended (when it started, if the record has no end).
func (i *RunInfo) ended() time.Time {
	if i.Finished.IsZero() {
		return i.Started
	}
	return i.Finished
}

// prune drops runs past the age limit, then the oldest until the total
// fits. rs.mu is held.
func (rs *runStore) prune() {
	var all []*RunInfo
	for _, runs := range rs.index {
		for _, info := range runs {
			all = append(all, info)
		}
	}
	sort.Slice(all, func(a, b int) bool { return all[a].ended().Before(all[b].ended()) })
	cutoff := rs.now().Add(-rs.retain)
	for _, info := range all {
		if (rs.retain > 0 && info.ended().Before(cutoff)) || (rs.maxBytes > 0 && rs.total > rs.maxBytes) {
			rs.drop(info)
		}
	}
}

// put keeps a run (replacing one with its id), then prunes.
func (rs *runStore) put(worker string, rep *RunReport, data []byte) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.dir == "" {
		return errNoState
	}
	p := rs.path(worker, rep.Run.Run)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		return err
	}
	rs.add(worker, rep, int64(len(data)))
	rs.prune()
	return nil
}

var errNoState = errors.New("this hub keeps no state (swim hub --state DIR): it can't keep runs")

// RetainRuns sets how long the hub keeps runs and how many bytes of them
// at most (0: no limit), and applies it now.
func (s *Server) RetainRuns(age time.Duration, bytes int64) {
	s.runs.mu.Lock()
	defer s.runs.mu.Unlock()
	s.runs.retain, s.runs.maxBytes = age, bytes
	s.runs.prune()
}

// Runs are worker id's kept runs, newest first.
func (s *Server) Runs(id string) []RunInfo {
	s.runs.mu.Lock()
	defer s.runs.mu.Unlock()
	out := []RunInfo{}
	for _, info := range s.runs.index[id] {
		out = append(out, *info)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Started.After(out[b].Started) })
	return out
}

// Run is one kept run of worker id.
func (s *Server) Run(id, run string) (*RunReport, bool) {
	s.runs.mu.Lock()
	defer s.runs.mu.Unlock()
	if s.runs.index[id][run] == nil {
		return nil, false
	}
	data, err := os.ReadFile(s.runs.path(id, run))
	if err != nil {
		return nil, false
	}
	var rep RunReport
	if json.Unmarshal(data, &rep) != nil {
		return nil, false
	}
	return &rep, true
}

// RunsBytes is how many bytes of runs the hub keeps.
func (s *Server) RunsBytes() int64 {
	s.runs.mu.Lock()
	defer s.runs.mu.Unlock()
	return s.runs.total
}

// putRun takes a finished run from the worker itself.
func (s *Server) putRun(w http.ResponseWriter, r *http.Request) {
	id, run := r.PathValue("id"), r.PathValue("run")
	s.mu.Lock()
	ok := s.isWorker(id, bearer(r.Header))
	s.mu.Unlock()
	if !ok {
		http.Error(w, "needs this worker's own token", http.StatusUnauthorized)
		return
	}
	if !chars.Name(run, 128) {
		http.Error(w, "a run id is letters, digits, '.', '_' and '-'", http.StatusBadRequest)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, RunMax))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, fmt.Sprintf("a run's report is at most %d MiB: send it without step output", RunMax>>20), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var rep RunReport
	if err := json.Unmarshal(data, &rep); err != nil {
		http.Error(w, "a run report is {run, jobs}: "+err.Error(), http.StatusBadRequest)
		return
	}
	if rep.Run.Run != run {
		http.Error(w, "the report's run is "+ui.Safe(rep.Run.Run)+", not "+run, http.StatusBadRequest)
		return
	}
	rep.Sanitize()
	if rep.Jobs == nil {
		rep.Jobs = []logparse.Doc{}
	}
	clean, _ := json.Marshal(&rep)
	if err := s.runs.put(id, &rep, clean); err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, errNoState) {
			code = http.StatusServiceUnavailable
		}
		http.Error(w, err.Error(), code)
		return
	}
	s.runs.mu.Lock()
	info := s.runs.index[id][run] // nil when retention dropped it at once
	s.runs.mu.Unlock()
	if info != nil {
		s.events.publish("run", id, *info)
	}
	s.mu.Lock()
	s.audit("%s reported run %s (%s; lanes %s)", id, run, RunResult(&rep.Run), strings.Trim(fmt.Sprint(laneNums(&rep.Run)), "[]"))
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func laneNums(r *runrec.Run) []int {
	out := []int{}
	for _, l := range r.Lanes {
		out = append(out, l.Lane)
	}
	return out
}
