// Package hub is swim's watch hub: one small HTTP server (swim hub) that
// many workers (swim watch, in enrolled directories) report to, so one page
// shows what every swim run is doing. Workers push snapshots, so they can
// sit behind NAT or firewalls; the hub only keeps the latest snapshot of
// each worker in memory (workers re-report every few seconds, so a
// restarted hub fills back up by itself).
//
//	POST   /api/enroll                  a worker joins: {name, host, root, version, key} -> {id, token}
//	POST   /api/report/{id}             its latest snapshot (404: enroll again)
//	DELETE /api/workers/{id}            it leaves
//	GET    /api/workers                 every worker, as JSON (for other services)
//	POST   /api/workers/{id}/approve    admin: approve it for secrets (DELETE: withdraw)
//	GET    /api/workers/{id}/key        admin: its public key, to encrypt a grant to
//	GET    /api/grants                  admin: every grant (names, never values)
//	PUT    /api/grants/{id}/{name}      admin: a secret encrypted to worker id's key
//	DELETE /api/grants/{id}/{name}      admin: revoke it
//	GET    /api/grants/{id}             the worker itself: its grants, still encrypted
//	GET    /                            the dashboard
//	GET    /w/{id}                      one worker (with controls, for a user logged in)
//
// Users and commands (users.go, commands.go, logs.go):
//
//	POST   /api/users                   admin: add a user {name, role} -> {token}
//	GET    /api/users                   admin: list them
//	DELETE /api/users/{name}            admin: remove one
//	GET    /login, POST /api/login      a user's token -> a session cookie
//	POST   /logout                      end it
//	POST   /api/workers/{id}/commands   user: send a command (role-checked)
//	GET    /api/workers/{id}/commands   worker: long-poll its commands (?after=SEQ&epoch=E&wait=25s)
//	POST   /api/workers/{id}/commands/{cid}       worker: ack one {outcome, detail}
//	PUT    /api/workers/{id}/logs/{run}/{lane}    worker: a log a command asked for
//	GET    /api/workers/{id}/logs/{run}/{lane}    user: read it (also /w/{id}/logs/{run}/{lane})
//
// Tokens (Authorization: Bearer <token>): the enroll token, shared, only
// enrolls (and reports, for workers from before per-worker tokens); each
// worker's own token, issued at enrollment, reports, fetches its grants
// and polls its commands; the admin token (kept in the state dir)
// approves, grants and manages users, and sends no commands; each user's
// own token (or the session cookie it logs in for) sends commands as
// that user's role allows. Viewing the pages and GET /api/workers needs
// no login. The hub relays commands; the worker decides whether to act.
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

	"github.com/gates-brightly/swimlane/internal/ui"
)

// Worker identifies one enrolled directory.
type Worker struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	Host    string `json:"host"`
	Root    string `json:"root"`
	Version string `json:"version,omitempty"`
	PID     int    `json:"pid,omitempty"`
	Key     string `json:"key,omitempty"` // its age public key, for secrets granted to it
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
	// A headless worker's idle lane holding a pending round: the script's
	// hash and its manifest's, which a run command from the hub pins.
	Script   string `json:"script_sha256,omitempty"`
	Manifest string `json:"manifest_sha256,omitempty"`
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
	// Headless is set when the worker runs with no terminal (swim watch
	// --headless): nobody is at it to answer or interrupt.
	Headless *HeadlessInfo `json:"headless,omitempty"`
}

// HeadlessInfo is how a headless worker runs.
type HeadlessInfo struct {
	Mode     string `json:"mode"`               // foreground, detached, systemd or launchd
	Yes      bool   `json:"yes,omitempty"`      // -y: it approves every guard in every batch
	Approved bool   `json:"approved,omitempty"` // --approved: it queues fully approved scripts by itself
	Since    string `json:"since,omitempty"`    // when it started (RFC 3339)
	// What it takes from the hub (swim.yml's headless: block), so the page
	// offers only those controls; the worker enforces it either way.
	Allow   []string `json:"allow,omitempty"`
	LogsOff bool     `json:"logs_off,omitempty"`
}

// Allows reports whether the worker accepts command kind from the hub.
func (h *HeadlessInfo) Allows(kind string) bool {
	if h == nil {
		return false
	}
	for _, a := range h.Allow {
		if a == kind {
			return !(kind == KindLog && h.LogsOff)
		}
	}
	return false
}

// Sanitize makes every text field safe to show (ui.Safe): a lane wrote
// most of it (rounds, steps, messages, log tails), and the hub can't trust
// a worker either. Worker builds it clean and the hub cleans what it's
// sent, so its pages, its JSON and the screens reading them never carry
// escape sequences, controls or bidi overrides.
func (s *Snapshot) Sanitize() {
	s.Worker.sanitize()
	s.Activity, s.At = ui.Safe(s.Activity), ui.Safe(s.At)
	for i := range s.Lanes {
		l := &s.Lanes[i]
		for _, f := range []*string{&l.State, &l.Round, &l.Job, &l.Run, &l.Step, &l.Reason, &l.Started, &l.Finished,
			&l.Message, &l.Progress, &l.MsgAt, &l.Cells, &l.Script, &l.Manifest} {
			*f = ui.Safe(*f)
		}
		l.Tail = safeAll(l.Tail)
	}
	for i := range s.Queue {
		b := &s.Queue[i]
		for _, f := range []*string{&b.ID, &b.State, &b.By, &b.Note, &b.Result} {
			*f = ui.Safe(*f)
		}
	}
	s.Events = safeAll(s.Events)
	if h := s.Headless; h != nil {
		h.Mode, h.Since = ui.Safe(h.Mode), ui.Safe(h.Since)
		h.Allow = safeAll(h.Allow)
	}
}

// sanitize cleans a worker's display fields (not its key).
func (w *Worker) sanitize() {
	w.ID, w.Name, w.Host, w.Root, w.Version = ui.Safe(w.ID), ui.Safe(w.Name), ui.Safe(w.Host), ui.Safe(w.Root), ui.Safe(w.Version)
}

func safeAll(lines []string) []string {
	if lines == nil {
		return nil
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = ui.Safe(l)
	}
	return out
}

// View is a worker as the hub serves it.
type View struct {
	Snapshot
	Seen     time.Time `json:"seen"`
	Stale    bool      `json:"stale"` // no report for 3 intervals
	KeyFP    string    `json:"key_fp,omitempty"`
	Approved bool      `json:"approved"`
	Grants   []string  `json:"grants,omitempty"`  // secrets granted to it, by name
	Fetched  string    `json:"fetched,omitempty"` // when it last fetched them
}

// Pending: it has a key and waits for the operator's approval.
func (v View) Pending() bool { return v.KeyFP != "" && !v.Approved }

// Server is the hub.
type Server struct {
	token string
	admin string // "" without state: nothing can be approved or granted
	dir   string // the state directory ("" keeps nothing)
	now   func() time.Time

	mu      sync.Mutex
	workers map[string]*View
	records map[string]*Record
	grants  map[string]map[string]Grant
	fetched map[string]time.Time

	// Users, sessions and commands (users.go, commands.go, logs.go).
	users    map[string]*User
	sessions map[string]*session
	commands map[string][]*Command
	nextSeq  map[string]int
	epoch    string
	wake     map[string]chan struct{} // closed when a worker gets a new command
	logs     *logCache
	secure   bool // served over TLS: session cookies are Secure
}

// New makes a hub that accepts workers presenting token, keeping nothing
// on disk (NewWithState keeps approvals and grants).
func New(token string) *Server {
	s := &Server{token: token, now: time.Now, workers: map[string]*View{},
		records: map[string]*Record{}, grants: map[string]map[string]Grant{}, fetched: map[string]time.Time{},
		users: map[string]*User{}, sessions: map[string]*session{}, commands: map[string][]*Command{}, nextSeq: map[string]int{},
		epoch: newToken()[:16], wake: map[string]chan struct{}{}}
	s.logs = newLogCache(LogMax, LogCacheMax, LogTTL, func() time.Time { return s.now() })
	return s
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
	mux.HandleFunc("POST /api/workers/{id}/approve", s.approveAPI)
	mux.HandleFunc("DELETE /api/workers/{id}/approve", s.approveAPI)
	mux.HandleFunc("GET /api/workers/{id}/key", s.keyAPI)
	mux.HandleFunc("GET /api/grants", s.grantsAPI)
	mux.HandleFunc("GET /api/grants/{id}", s.fetchAPI)
	mux.HandleFunc("PUT /api/grants/{id}/{name}", s.grantAPI)
	mux.HandleFunc("DELETE /api/grants/{id}/{name}", s.grantAPI)
	mux.HandleFunc("GET /w/{id}", s.workerPage)
	s.userRoutes(mux)
	s.commandRoutes(mux)
	mux.HandleFunc("GET /{$}", s.dashboard)
	return mux
}

func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	var wk Worker
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&wk); err != nil || wk.Root == "" {
		http.Error(w, "enroll needs {name, host, root}", http.StatusBadRequest)
		return
	}
	wk.ID = IDFor(wk.Host, wk.Root)
	wk.sanitize()
	if wk.Name == "" {
		wk.Name = wk.Host
	}
	got := bearer(r.Header)
	s.mu.Lock()
	defer s.mu.Unlock()
	// The enroll token or the admin's enrolls anyone; a worker's own token
	// enrolls it again (after a hub restart).
	if !tokenIs(got, s.token) && !s.isAdmin(got) && !s.isWorker(wk.ID, got) {
		http.Error(w, "bad or missing token", http.StatusUnauthorized)
		return
	}
	rec := s.records[wk.ID]
	if rec == nil {
		rec = &Record{}
		s.records[wk.ID] = rec
	}
	key := strings.TrimSpace(wk.Key)
	if rec.Key != key && rec.Approved {
		// A new key: the approval and the grants were for the old one.
		s.audit("%s (%s) enrolled with a new key %s: approval withdrawn, %d grants deleted", wk.ID, wk.Name, KeyFingerprint(key), len(s.grants[wk.ID]))
		rec.Approved, rec.ApprovedAt = false, ""
		delete(s.grants, wk.ID)
	}
	rec.Worker, rec.Key = wk, key
	rec.KeyFP = ""
	if key != "" {
		rec.KeyFP = KeyFingerprint(key)
	}
	token := got
	if !s.isWorker(wk.ID, got) {
		token = newToken()
		rec.TokenHash = hashToken(token)
	}
	if err := s.save(); err != nil {
		http.Error(w, "hub state: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if v, ok := s.workers[wk.ID]; ok {
		v.Worker = wk
	} else {
		s.workers[wk.ID] = &View{Snapshot: Snapshot{Worker: wk, Activity: "enrolled", At: s.now().UTC().Format(time.RFC3339),
			Lanes: []Lane{}, Queue: []Batch{}, Events: []string{}}, Seen: s.now()}
	}
	json.NewEncoder(w).Encode(map[string]string{"id": wk.ID, "token": token})
}

func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	_, known := s.workers[id]
	ok := s.authorized(r) || s.isWorker(id, bearer(r.Header))
	s.mu.Unlock()
	if !known {
		// A hub that forgot it (restarted without state): enroll again.
		http.Error(w, "unknown worker; enroll again", http.StatusNotFound)
		return
	}
	if !ok {
		http.Error(w, "bad or missing token", http.StatusUnauthorized)
		return
	}
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
	snap.Sanitize()
	snap.Worker.ID = id
	snap.Worker.Key = v.Key
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
	id := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()
	got := bearer(r.Header)
	if !s.authorized(r) && !s.isWorker(id, got) && !s.isAdmin(got) {
		http.Error(w, "bad or missing token", http.StatusUnauthorized)
		return
	}
	delete(s.workers, id)
	if rec := s.records[id]; rec != nil {
		s.audit("%s (%s) left; %d grants deleted", id, rec.Name, len(s.grants[id]))
	}
	delete(s.records, id)
	delete(s.grants, id)
	s.forgetCommands(id)
	s.save()
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
		if rec := s.records[cp.ID]; rec != nil {
			cp.KeyFP, cp.Approved = rec.KeyFP, rec.Approved
		}
		cp.Worker.Key = ""
		cp.Grants = s.grantNames(cp.ID)
		if t, ok := s.fetched[cp.ID]; ok {
			cp.Fetched = t.UTC().Format(time.RFC3339)
		}
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
