// Package hub is swim's watch hub: one small HTTP server (swim hub) that
// many workers (swim watch, in enrolled directories) report to, so one page
// shows what every swim run is doing. Workers push snapshots, so they can
// sit behind NAT or firewalls; the hub only keeps the latest snapshot of
// each worker in memory (workers re-report every few seconds, so a
// restarted hub fills back up by itself).
//
//	POST   /api/enroll           a worker joins: {name, host, root, version} -> {id}
//	POST   /api/report/{id}      its latest snapshot (404: enroll again)
//	DELETE /api/workers/{id}     it leaves
//	GET    /api/workers          every worker, as JSON (for other services)
//	GET    /                     the dashboard
//	GET    /w/{id}               one worker
//
// Writes need the hub's token (Authorization: Bearer <token>); the pages
// and GET /api/workers are read-only.
package hub

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Worker identifies one enrolled directory.
type Worker struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	Host    string `json:"host"`
	Root    string `json:"root"`
	Version string `json:"version,omitempty"`
	PID     int    `json:"pid,omitempty"`
}

// Lane is one lane as the worker sees it.
type Lane struct {
	Lane     int      `json:"lane"`
	State    string   `json:"state"`
	Round    string   `json:"round,omitempty"`
	Job      string   `json:"job,omitempty"`
	Run      string   `json:"run,omitempty"`
	Step     string   `json:"step,omitempty"`
	Reason   string   `json:"reason,omitempty"`
	Pass     int      `json:"pass"`
	Fail     int      `json:"fail"`
	Skip     int      `json:"skip"`
	Drift    int      `json:"drift"`
	Exit     *int     `json:"exit,omitempty"`
	Started  string   `json:"started,omitempty"`
	Finished string   `json:"finished,omitempty"`
	Tail     []string `json:"tail,omitempty"`     // the end of a running lane's log, its running step included
	Message  string   `json:"message,omitempty"`  // a running round's status message (swim msg)
	Progress string   `json:"progress,omitempty"` // its progress, "7/25"
	MsgAt    string   `json:"message_at,omitempty"`
	Cells    string   `json:"cells,omitempty"` // a matrix round's cells: "2/3 passed · failed: billing"
}

// Batch is one queue entry.
type Batch struct {
	Seq    int    `json:"seq"`
	ID     string `json:"id"`
	State  string `json:"state"`
	By     string `json:"by,omitempty"`
	Note   string `json:"note,omitempty"`
	Lanes  []int  `json:"lanes"`
	Result string `json:"result,omitempty"`
}

// Snapshot is a worker's report.
type Snapshot struct {
	Worker
	Activity string   `json:"activity"` // what swim watch is doing
	At       string   `json:"at"`
	Interval float64  `json:"interval_s"` // how often it reports
	Lanes    []Lane   `json:"lanes"`
	Queue    []Batch  `json:"queue"`
	Events   []string `json:"events"` // the end of .swim.log
}

// View is a worker as the hub serves it.
type View struct {
	Snapshot
	Seen  time.Time `json:"seen"`
	Stale bool      `json:"stale"` // no report for 3 intervals
}

// Server is the hub.
type Server struct {
	token string
	now   func() time.Time

	mu      sync.Mutex
	workers map[string]*View
}

// New makes a hub that accepts workers presenting token.
func New(token string) *Server {
	return &Server{token: token, now: time.Now, workers: map[string]*View{}}
}

// IDFor is a worker's id: stable for one host and directory.
func IDFor(host, root string) string {
	s := sha256.Sum256([]byte(host + "\x00" + root))
	return hex.EncodeToString(s[:])[:12]
}

func (s *Server) authorized(r *http.Request) bool {
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return s.token != "" && subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

// Handler serves the hub.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/enroll", s.enroll)
	mux.HandleFunc("POST /api/report/{id}", s.report)
	mux.HandleFunc("DELETE /api/workers/{id}", s.leave)
	mux.HandleFunc("GET /api/workers", s.list)
	mux.HandleFunc("GET /w/{id}", s.workerPage)
	mux.HandleFunc("GET /{$}", s.dashboard)
	return mux
}

func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "bad or missing token", http.StatusUnauthorized)
		return
	}
	var wk Worker
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&wk); err != nil || wk.Root == "" {
		http.Error(w, "enroll needs {name, host, root}", http.StatusBadRequest)
		return
	}
	wk.ID = IDFor(wk.Host, wk.Root)
	if wk.Name == "" {
		wk.Name = wk.Host
	}
	s.mu.Lock()
	if v, ok := s.workers[wk.ID]; ok {
		v.Worker = wk
	} else {
		s.workers[wk.ID] = &View{Snapshot: Snapshot{Worker: wk, Activity: "enrolled", At: s.now().UTC().Format(time.RFC3339),
			Lanes: []Lane{}, Queue: []Batch{}, Events: []string{}}, Seen: s.now()}
	}
	s.mu.Unlock()
	json.NewEncoder(w).Encode(map[string]string{"id": wk.ID})
}

func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "bad or missing token", http.StatusUnauthorized)
		return
	}
	id := r.PathValue("id")
	var snap Snapshot
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&snap); err != nil {
		http.Error(w, "bad snapshot: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.workers[id]
	if !ok {
		http.Error(w, "unknown worker; enroll again", http.StatusNotFound)
		return
	}
	snap.Worker.ID = id
	if snap.Name == "" {
		snap.Name = v.Name
	}
	// JSON readers get lists, never null.
	if snap.Lanes == nil {
		snap.Lanes = []Lane{}
	}
	if snap.Queue == nil {
		snap.Queue = []Batch{}
	}
	if snap.Events == nil {
		snap.Events = []string{}
	}
	v.Snapshot, v.Seen = snap, s.now()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) leave(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "bad or missing token", http.StatusUnauthorized)
		return
	}
	s.mu.Lock()
	delete(s.workers, r.PathValue("id"))
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// Workers returns every worker, by name, with staleness worked out.
func (s *Server) Workers() []View {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var out []View
	for _, v := range s.workers {
		cp := *v
		every := time.Duration(cp.Interval * float64(time.Second))
		if every < 2*time.Second {
			every = 2 * time.Second
		}
		cp.Stale = now.Sub(cp.Seen) > 3*every+5*time.Second
		out = append(out, cp)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Name != out[b].Name {
			return out[a].Name < out[b].Name
		}
		return out[a].ID < out[b].ID
	})
	return out
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"schema": "swim.hub/v1", "workers": s.Workers()})
}
