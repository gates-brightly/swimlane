package hub

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/gates-brightly/swimlane/internal/ui"
)

// Commands: what a hub user asks a headless worker to do. The hub only
// relays them: it keeps each one (durable, in its state), hands them to
// the worker when it polls (the worker pulls, so it can sit behind NAT),
// and shows the outcome the worker acknowledges. The worker checks every
// command against its own rules (swim.yml's headless.allow, the command's
// expiry and pin) and records it; the hub decides nothing beyond who may
// send what.

// Command kinds the hub relays (and KindAnswer, answers.go).
const (
	KindInterrupt = "interrupt"
	KindDrain     = "drain"
	KindRun       = "run"
	KindLog       = "log"
	KindTrash     = "trash"
)

// Outcomes a worker acknowledges.
const (
	OutcomeDone    = "done"
	OutcomeRefused = "refused"
	OutcomeExpired = "expired"
)

// Command lifetimes the hub gives; the worker caps them again with its own
// headless.command_ttl.
const (
	CommandTTL   = 10 * time.Minute
	InterruptTTL = 2 * time.Minute
	// MaxWait is the longest a worker's poll is held open.
	MaxWait = 25 * time.Second
	// keepCommands is how many commands the hub keeps per worker.
	keepCommands = 100
)

// Command is one request to a worker.
type Command struct {
	CID     string `json:"cid"`
	Seq     int    `json:"seq"`   // per worker, increasing
	Epoch   string `json:"epoch"` // the hub's (see state)
	Kind    string `json:"kind"`
	Lane    int    `json:"lane,omitempty"`
	Pin     Pin    `json:"pin"`
	By      string `json:"by"` // the hub user who sent it
	At      string `json:"at"`
	Expires string `json:"expires"`
	// Answer is an answer command's decision (and signature).
	Answer  *Answer `json:"answer,omitempty"`
	Outcome string  `json:"outcome,omitempty"` // done | refused | expired, once acknowledged
	Detail  string  `json:"detail,omitempty"`
	AckedAt string  `json:"acked_at,omitempty"`
}

// Pin is what the page showed when the command was sent; the worker
// refuses the command when it no longer matches.
type Pin struct {
	Run      string `json:"run,omitempty"`      // interrupt, log: the run shown
	SHA256   string `json:"sha256,omitempty"`   // run: the lane script's hash shown
	Manifest string `json:"manifest,omitempty"` // run: its execution manifest's hash
	Request  string `json:"request,omitempty"`  // answer: the question answered
	Evidence string `json:"evidence,omitempty"` // answer: the evidence hash it showed
	Job      string `json:"job,omitempty"`      // trash: the lane's job shown
}

// State is the command's state for a page: its outcome, else "sent" while
// it may still be picked up, "expired (never picked up)" after.
func (c Command) State(now time.Time) string {
	if c.Outcome != "" {
		return c.Outcome
	}
	if t, err := time.Parse(time.RFC3339, c.Expires); err == nil && now.After(t) {
		return "expired (never picked up)"
	}
	return "sent"
}

// pinRE: pins are hashes and run ids.
var pinRE = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func (s *Server) commandRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/workers/{id}/commands", s.sendCommand)
	mux.HandleFunc("GET /api/workers/{id}/commands", s.pollCommands)
	mux.HandleFunc("POST /api/workers/{id}/commands/{cid}", s.ackCommand)
	mux.HandleFunc("PUT /api/workers/{id}/logs/{run}/{lane}", s.putLog)
	mux.HandleFunc("GET /api/workers/{id}/logs/{run}/{lane}", s.getLog)
	mux.HandleFunc("GET /w/{id}/logs/{run}/{lane}", s.logPage)
	mux.HandleFunc("GET /w/{id}/answer/{req}", s.answerPage)
	mux.HandleFunc("GET /w/{id}/trash/{lane}", s.trashPage)
}

// Send queues a command for worker id from user by, after checking its
// kind and pin (an answer: that it names a question the worker waits
// on); the role check is the caller's.
func (s *Server) Send(id, by string, c Command) (*Command, error) {
	switch c.Kind {
	case KindInterrupt, KindLog:
		if c.Lane < 1 || c.Pin.Run == "" {
			return nil, fmt.Errorf("%s needs a lane and the run it's for", c.Kind)
		}
	case KindRun:
		if c.Lane < 1 || c.Pin.SHA256 == "" {
			return nil, fmt.Errorf("run needs a lane and the script hash it's for")
		}
	case KindTrash:
		if c.Lane < 1 || c.Pin.Job == "" {
			return nil, fmt.Errorf("trash needs a lane and the job it holds")
		}
	case KindDrain:
	case KindAnswer:
		if c.Pin.Request == "" {
			return nil, fmt.Errorf("answer needs the question it answers (pin.request)")
		}
	default:
		return nil, fmt.Errorf("unknown command %q (answer, interrupt, drain, run, log, trash)", c.Kind)
	}
	if c.Kind != KindAnswer {
		c.Answer = nil
	}
	for _, p := range []string{c.Pin.Run, c.Pin.SHA256, c.Pin.Manifest, c.Pin.Request, c.Pin.Evidence, c.Pin.Job} {
		if p != "" && !pinRE.MatchString(p) {
			return nil, fmt.Errorf("bad pin %q", p)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.records[id]
	if rec == nil {
		return nil, fmt.Errorf("no such worker")
	}
	if c.Kind == KindAnswer {
		if err := checkAnswer(s.workers[id], &c); err != nil {
			return nil, err
		}
	}
	now := s.now().UTC()
	ttl := CommandTTL
	if c.Kind == KindInterrupt {
		ttl = InterruptTTL
	}
	s.nextSeq[id]++
	cmd := &Command{CID: newToken()[:16], Seq: s.nextSeq[id], Epoch: s.epoch, Kind: c.Kind, Lane: c.Lane, Pin: c.Pin, Answer: c.Answer, By: by,
		At: now.Format(time.RFC3339), Expires: now.Add(ttl).Format(time.RFC3339)}
	list := append(s.commands[id], cmd)
	if len(list) > keepCommands {
		list = list[len(list)-keepCommands:]
	}
	s.commands[id] = list
	if err := s.save(); err != nil {
		return nil, err
	}
	s.audit("%s sent %s %s to %s (%s): seq %d %s", by, c.Kind, laneText(c.Lane), id, rec.Name, cmd.Seq, cmd.CID)
	if ch, ok := s.wake[id]; ok {
		close(ch)
		delete(s.wake, id)
	}
	cp := *cmd
	return &cp, nil
}

func laneText(n int) string {
	if n == 0 {
		return ""
	}
	return "swim " + strconv.Itoa(n)
}

// Commands are worker id's recent commands, newest first.
func (s *Server) Commands(id string) []Command {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.commands[id]
	out := make([]Command, 0, len(list))
	for i := len(list) - 1; i >= 0; i-- {
		out = append(out, *list[i])
	}
	return out
}

// forgetCommands drops a worker's commands (it left). s.mu is held.
func (s *Server) forgetCommands(id string) {
	delete(s.commands, id)
	if ch, ok := s.wake[id]; ok {
		close(ch)
		delete(s.wake, id)
	}
}

func (s *Server) sendCommand(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	form := isForm(r)
	var c Command
	if form {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		c.Kind = r.PostFormValue("kind")
		c.Lane, _ = strconv.Atoi(r.PostFormValue("lane"))
		c.Pin = Pin{Run: r.PostFormValue("run"), SHA256: r.PostFormValue("sha256"), Manifest: r.PostFormValue("manifest"),
			Request: r.PostFormValue("request"), Evidence: r.PostFormValue("evidence"), Job: r.PostFormValue("job")}
		if c.Kind == KindAnswer {
			c.Answer = &Answer{Decision: r.PostFormValue("decision"), Key: r.PostFormValue("key"), Sig: r.PostFormValue("sig")}
		}
	} else if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "a command needs {kind, lane, pin}", http.StatusBadRequest)
		return
	}
	// A viewer may fetch logs, and send a signed answer: the signature,
	// not the hub's login, is what the worker counts. A button answer (no
	// signature) rests on the login, so it takes an operator.
	role := RoleOperator
	if c.Kind == KindLog || c.Kind == KindAnswer && c.Answer.Signed() {
		role = RoleViewer
	}
	u, ok := s.requireUser(w, r, role)
	if !ok {
		return
	}
	cmd, err := s.Send(id, u.Name, c)
	if err != nil {
		code := http.StatusBadRequest
		if err.Error() == "no such worker" {
			code = http.StatusNotFound
		}
		http.Error(w, err.Error(), code)
		return
	}
	if form {
		to := "/w/" + url.PathEscape(id)
		if cmd.Kind == KindLog {
			to += "/logs/" + url.PathEscape(cmd.Pin.Run) + "/" + strconv.Itoa(cmd.Lane)
		}
		http.Redirect(w, r, to, http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(cmd)
}

// pending are worker id's commands not yet acknowledged, after seq after.
// s.mu is held.
func (s *Server) pending(id string, after int) []Command {
	out := []Command{}
	for _, c := range s.commands[id] {
		if c.Outcome == "" && c.Seq > after {
			out = append(out, *c)
		}
	}
	return out
}

// pollCommands hands a worker its commands after ?after=SEQ, holding the
// request up to ?wait= (at most MaxWait) until there is one. A worker that
// names another epoch (?epoch=) gets every unacknowledged one: its seq
// counts another sequence.
func (s *Server) pollCommands(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q := r.URL.Query()
	after, _ := strconv.Atoi(q.Get("after"))
	wait, _ := time.ParseDuration(q.Get("wait"))
	wait = min(max(wait, 0), MaxWait)
	s.mu.Lock()
	if !s.isWorker(id, bearer(r.Header)) {
		s.mu.Unlock()
		http.Error(w, "needs this worker's own token", http.StatusUnauthorized)
		return
	}
	if q.Get("epoch") != s.epoch {
		after = 0
	}
	cmds := s.pending(id, after)
	if len(cmds) == 0 && wait > 0 {
		ch, ok := s.wake[id]
		if !ok {
			ch = make(chan struct{})
			s.wake[id] = ch
		}
		s.mu.Unlock()
		t := time.NewTimer(wait)
		select {
		case <-ch:
		case <-t.C:
		case <-r.Context().Done():
		}
		t.Stop()
		s.mu.Lock()
		cmds = s.pending(id, after)
	}
	epoch := s.epoch
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"epoch": epoch, "commands": cmds})
}

// Ack is a worker's acknowledgement of a command.
type Ack struct {
	Outcome string `json:"outcome"`
	Detail  string `json:"detail,omitempty"`
}

func (s *Server) ackCommand(w http.ResponseWriter, r *http.Request) {
	id, cid := r.PathValue("id"), r.PathValue("cid")
	var a Ack
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&a); err != nil {
		http.Error(w, "an ack needs {outcome, detail}", http.StatusBadRequest)
		return
	}
	switch a.Outcome {
	case OutcomeDone, OutcomeRefused, OutcomeExpired:
	default:
		http.Error(w, "outcome must be done, refused or expired", http.StatusBadRequest)
		return
	}
	// The worker wrote it: one safe line, of a sane length.
	a.Detail = ui.Safe(a.Detail)
	if len(a.Detail) > 500 {
		a.Detail = a.Detail[:500] + "…"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.isWorker(id, bearer(r.Header)) {
		http.Error(w, "needs this worker's own token", http.StatusUnauthorized)
		return
	}
	var cmd *Command
	for _, c := range s.commands[id] {
		if c.CID == cid {
			cmd = c
		}
	}
	switch {
	case cmd == nil:
		http.Error(w, "no such command", http.StatusNotFound)
		return
	case cmd.Outcome != "":
		if cmd.Outcome != a.Outcome {
			http.Error(w, "already acknowledged: "+cmd.Outcome, http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	cmd.Outcome, cmd.Detail, cmd.AckedAt = a.Outcome, a.Detail, s.now().UTC().Format(time.RFC3339)
	if err := s.save(); err != nil {
		http.Error(w, "hub state: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit("%s acked %s %s (seq %d, by %s): %s %s", id, cmd.Kind, laneText(cmd.Lane), cmd.Seq, cmd.By, a.Outcome, a.Detail)
	w.WriteHeader(http.StatusNoContent)
}

var trashTmpl = template.Must(template.New("trash").Funcs(funcs).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>trash · {{.Name}} · swim hub</title>` + style + `</head><body>
<div><a href="/w/{{.ID}}">← {{.Name}}</a></div>
<h1>Trash swim {{.L.Lane}}?</h1>
<p>{{.L.Round}}</p>
<div class="muted mono">job {{.Job}} · {{.L.State}}</div>
<p>The worker moves this lane's script and its lane.{{.L.Lane}}.yml to <span class="mono">.swim/cleaned/</span> and archives its log. Nothing is deleted: <span class="mono">swim trash --restore STAMP</span> on the worker puts it back. If the lane holds another job by the time the worker takes this, it refuses and nothing moves.</p>
<form method="post" action="/api/workers/{{.ID}}/commands"><input type="hidden" name="csrf" value="{{.CSRF}}">
<input type="hidden" name="kind" value="trash"><input type="hidden" name="lane" value="{{.L.Lane}}"><input type="hidden" name="job" value="{{.Job}}">
<button class="danger">Trash swim {{.L.Lane}}</button> <a href="/w/{{.ID}}">cancel</a></form>
</body></html>`))

// trashPage confirms a trash: the lane, its job and round, and what the
// worker will do. Only its button sends the command.
func (s *Server) trashPage(w http.ResponseWriter, r *http.Request) {
	user, role, csrf := s.viewer(r)
	if user == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if role != RoleOperator {
		http.Error(w, "trashing a lane takes the operator role", http.StatusForbidden)
		return
	}
	v, ok := s.worker(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	n, _ := strconv.Atoi(r.PathValue("lane"))
	job := r.URL.Query().Get("job")
	var l *Lane
	for i := range v.Lanes {
		if v.Lanes[i].Lane == n {
			l = &v.Lanes[i]
		}
	}
	if l == nil || l.TrashJob == "" || l.TrashJob != job || !pinRE.MatchString(job) {
		http.Error(w, "that lane doesn't hold that job now (rewritten, trashed, or running): go back and look again", http.StatusConflict)
		return
	}
	d := struct {
		ID, Name, CSRF, Job string
		L                   *Lane
	}{ID: v.ID, Name: v.Name, CSRF: csrf, Job: job, L: l}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	trashTmpl.Execute(w, d)
}
