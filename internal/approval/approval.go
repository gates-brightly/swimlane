// Package approval lets a lane ask the operator to approve a guarded step
// while swim run owns the terminal. Parallel lanes have no stdin, so the
// lane's `guard` (through `swim _approve`) sends the request over a unix
// socket to the launcher, which asks the operator, one request at a time,
// and sends the answer back. The socket's path reaches lanes as
// SWIM_APPROVAL.
//
// The launcher, not the request, says who is asking: each lane process
// gets its own token (SWIM_APPROVAL_TOKEN), registered with the lane, job
// and manifest it was started for, and the question is built from those.
// A request without a known token, from a process outside the lane's
// process tree, or for a guard its script doesn't declare, is refused. The
// lane's description is shown, labelled as the lane's. Each approval
// carries a request id that the lane must consume, once, before it records
// the approval; under swim run the launcher records it in the chain then
// (Server.Record), so a lane never writes its own approval.
//
// The same socket tells swim's hidden commands which lane they run for
// (op "whoami"), so a lane can't report for another.
package approval

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/peer"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// EnvVar holds the socket path in a lane's environment; TokenEnv the lane
// process's token.
const (
	EnvVar   = "SWIM_APPROVAL"
	TokenEnv = "SWIM_APPROVAL_TOKEN"
)

// ConfirmFlag is the guard flag of a confirm question from a lane in
// another language (swim's own, never declared in a script).
const ConfirmFlag = "SWIM_CONFIRM"

// Request is one approval a lane asks for. Lane and Job are what the lane
// says; the server replaces them with what the launcher registered for its
// token (they stay on the wire for older lanes). Cell and Manifest are the
// server's alone.
type Request struct {
	Lane     int    `json:"lane"`
	Job      string `json:"job,omitempty"`
	Cell     string `json:"-"`
	Manifest string `json:"-"`
	Flag     string `json:"flag"`
	Desc     string `json:"desc"`
	// Token identifies the lane process (TokenEnv). Op is "" to ask,
	// "consume" to use up the approval with request id ID, or "whoami" to
	// learn which lane, cell and run the token is for.
	Token string `json:"token,omitempty"`
	Op    string `json:"op,omitempty"`
	ID    string `json:"id,omitempty"`
	// FlagSet: the lane's environment has Flag=1. Under swim run that
	// approves only if swim run's own environment set it.
	FlagSet bool `json:"flag_set,omitempty"`
	// RawDesc is the description as the lane sent it, for the chain; Desc
	// is made safe to show (Clean).
	RawDesc string `json:"-"`
	// Observed are the round's Observe: facts, read by the launcher as it
	// asks (shown with the question).
	Observed map[string]string `json:"-"`
	// Evidence is what the round's evidence steps showed this run (the plan
	// being approved), shown with the question; EvidenceRequired: the
	// guard's rule needs evidence-bound approval, so an answer given ahead
	// of time (before the plan existed) doesn't count.
	Evidence         string `json:"evidence,omitempty"`
	EvidenceSHA      string `json:"evidence_sha256,omitempty"`
	EvidenceRequired bool   `json:"evidence_required,omitempty"`
}

// Response is the operator's answer.
type Response struct {
	Approved bool   `json:"approved"`
	By       string `json:"by,omitempty"`   // who answered (the operator's user name)
	Auto     bool   `json:"auto,omitempty"` // approved by -y, not at a prompt
	Via      string `json:"via,omitempty"`  // what approved it when Auto ("swim run -y", "queue q-... -y")
	Why      string `json:"why,omitempty"`  // for a decline without an answer: "the run is stopping", ...
	Pre      bool   `json:"pre,omitempty"`  // answered ahead of time in the live view (pre-approved / pre-declined)
	At       string `json:"at,omitempty"`   // when a pre-answer was given (hh:mm:ss)
	// ID is the approval's request id, to consume once (approved only).
	// Lane and Job are who the launcher says asked; Observed the Observe:
	// facts when it was approved.
	ID       string            `json:"id,omitempty"`
	Lane     int               `json:"lane,omitempty"`
	Job      string            `json:"job,omitempty"`
	Cell     string            `json:"cell,omitempty"`
	Run      string            `json:"run,omitempty"`
	Observed map[string]string `json:"observed,omitempty"`
	// Dry: no one was asked (nobody can be, under swim run); the guard is a
	// dry run, and Why (if set) says why no signed approval counted.
	Dry bool `json:"dry,omitempty"`
	// How is the APPROVED line's detail when the launcher decided
	// ("approved by ... (signed)", "FLAG=1"); "" to build it from By, Auto,
	// Via, Pre and At.
	How string `json:"how,omitempty"`
	// Warns are WARN lines for the lane's log (evidence that changed).
	Warns []Warn `json:"warns,omitempty"`
	// Hash is the approve entry the launcher recorded as the approval was
	// consumed ("" if it leaves recording to the lane).
	Hash string `json:"hash,omitempty"`
	// Grant is what the launcher records once the approval is consumed.
	Grant *Grant `json:"-"`
}

// Warn is a WARN line for the lane's log, with an optional diff under it.
type Warn struct {
	Label  string   `json:"label"`
	Detail string   `json:"detail,omitempty"`
	Diff   []string `json:"diff,omitempty"`
}

// Grant is an approval the launcher records when its lane uses it: the
// approve entry (from the launcher's own facts) and the .swim.log detail.
type Grant struct {
	Entry   chain.Entry
	History string
}

// Caller is a lane process the launcher started, registered under its
// token.
type Caller struct {
	Lane     int
	Cell     string // a matrix cell or job, "" for the lane itself
	Job      string // "" if the launcher doesn't know it yet
	Run      string
	Manifest string
	Guards   []string // the flags its script declares or uses
	// PID is the lane's process: requests must come from it or its
	// descendants (where the platform can tell, package peer). AwaitPID
	// registers a process not started yet (SetPID names it); 0 checks the
	// token alone (a process swim itself runs for the lane).
	PID int
}

// AwaitPID is Caller.PID for a lane process about to start.
const AwaitPID = -1

// MaxRequest caps one request: a lane can't make the launcher buffer more.
const MaxRequest = 1 << 20

// ReadTimeout is how long a connection may take to send its request.
var ReadTimeout = 10 * time.Second

// issued is an approval handed out and not yet consumed.
type issued struct {
	token, flag string
	consumed    bool
	grant       *Grant
}

// Asker asks the operator; it returns false when cancel closes first.
type Asker func(req Request, cancel <-chan struct{}) Response

// Server answers requests from the lanes of one run.
type Server struct {
	ln  net.Listener
	dir string
	ask Asker // called concurrently, once per waiting lane: it decides how to queue them
	// Refused, if set, is told of each request refused without asking: no
	// or an unknown token, a process outside the lane, a guard the script
	// doesn't declare, a reused response.
	Refused func(c Caller, req Request, why string)
	// Record, if set, records an approval as its lane consumes it (the
	// approve entry's hash); an error refuses the approval.
	Record func(c Caller, g *Grant) (string, error)

	mu        sync.Mutex
	callers   map[string]Caller
	issued    map[string]*issued
	cancelled bool
	cancel    chan struct{}
	wg        sync.WaitGroup
}

// Listen starts a server whose socket lives in a fresh, private temp dir
// (unix socket paths are short: a repo path could be too long).
func Listen(ask Asker) (*Server, error) {
	dir, err := os.MkdirTemp("", "swim-ap-")
	if err != nil {
		return nil, err
	}
	os.Chmod(dir, 0o700)
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	os.Chmod(filepath.Join(dir, "s"), 0o600)
	s := &Server{ln: ln, dir: dir, ask: ask, cancel: make(chan struct{}), callers: map[string]Caller{}, issued: map[string]*issued{}}
	s.wg.Add(1)
	go s.accept()
	return s, nil
}

// Path is the socket's path, for SWIM_APPROVAL.
func (s *Server) Path() string { return s.ln.Addr().String() }

// Register records a lane process the launcher is about to start and
// returns its token, for TokenEnv.
func (s *Server) Register(c Caller) string {
	tok := newID()
	s.mu.Lock()
	s.callers[tok] = c
	s.mu.Unlock()
	return tok
}

// SetPID names the process a token registered with AwaitPID was given to,
// once it has started.
func (s *Server) SetPID(tok string, pid int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.callers[tok]; ok {
		c.PID = pid
		s.callers[tok] = c
	}
}

// Revoke forgets a token: its lane process has exited, so nothing may ask
// with it again (a process it left behind included).
func (s *Server) Revoke(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.callers, tok)
}

func newID() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// caller is the registered lane for a token (waiting briefly for the
// pid of one just starting).
func (s *Server) caller(tok string) (Caller, bool) {
	for i := 0; ; i++ {
		s.mu.Lock()
		c, ok := s.callers[tok]
		s.mu.Unlock()
		if !ok || tok == "" {
			return Caller{}, false
		}
		if c.PID != AwaitPID || i >= 250 {
			return c, true
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// from checks that conn comes from c's lane: its process or one of its
// descendants. Refused when the platform can tell and it doesn't, or the
// lane never started; allowed where the platform can't tell (the token
// alone).
func from(conn net.Conn, c Caller) string {
	if c.PID == 0 || !peer.Supported {
		return ""
	}
	if c.PID == AwaitPID {
		return "swim " + fmt.Sprint(c.Lane) + "'s process never started"
	}
	ok, known := peer.FromLane(conn, c.PID)
	switch {
	case !known:
		return "can't tell which process sent it"
	case !ok:
		p, _ := peer.PID(conn)
		return fmt.Sprintf("it came from process %d, outside swim %d's lane (its token was used by another process)", p, c.Lane)
	}
	return ""
}

// refuse declines req without asking, and says so.
func (s *Server) refuse(c Caller, req Request, why string) Response {
	if s.Refused != nil {
		s.Refused(c, req, why)
	}
	return Response{Why: why}
}

// check refuses a request that doesn't come from a registered lane process
// or names a guard its script doesn't declare; otherwise it returns req
// with the launcher's facts in place of the lane's.
func (s *Server) check(req Request) (Caller, Request, string) {
	c, ok := s.caller(req.Token)
	if !ok {
		return c, req, "the request didn't come from a lane of this run (no or an unknown token)"
	}
	req.RawDesc, req.Desc = req.Desc, Clean(req.Desc)
	if req.Lane != c.Lane {
		req.Desc = fmt.Sprintf("%s (sent as swim %d)", req.Desc, req.Lane)
	}
	req.Lane, req.Job, req.Cell, req.Manifest = c.Lane, c.Job, c.Cell, c.Manifest
	// A flag the lane has set is checked against swim run's own
	// environment, which names it: the operator set it, declared or not.
	declared := req.Flag == ConfirmFlag || req.FlagSet
	for _, g := range c.Guards {
		declared = declared || g == req.Flag
	}
	if !declared {
		return c, req, fmt.Sprintf("guard %s isn't declared in swim %d's script (list it in # Guards:)", req.Flag, c.Lane)
	}
	return c, req, ""
}

// consume uses up approval id for the lane process with token tok and
// guard flag, once.
func (s *Server) consume(req Request) Response {
	c, ok := s.caller(req.Token)
	if !ok {
		return s.refuse(c, req, "the request didn't come from a lane of this run (no or an unknown token)")
	}
	s.mu.Lock()
	is := s.issued[req.ID]
	why := ""
	switch {
	case is == nil:
		why = "no such approval"
	case is.token != req.Token:
		why = "the approval was given to another lane"
	case is.flag != req.Flag:
		why = "the approval was for guard " + is.flag
	case is.consumed:
		why = "the approval was already used"
	default:
		is.consumed = true
	}
	s.mu.Unlock()
	if why != "" {
		return s.refuse(c, req, "approval "+short(req.ID)+": "+why)
	}
	resp := Response{Approved: true, ID: req.ID, Lane: c.Lane, Job: c.Job}
	if is.grant != nil && s.Record != nil {
		hash, err := s.Record(c, is.grant)
		if err != nil {
			return s.refuse(c, req, "approval "+short(req.ID)+": couldn't record it: "+err.Error())
		}
		resp.Hash = hash
	}
	return resp
}

// whoami says which lane, cell, job and run the token is for.
func (s *Server) whoami(req Request) Response {
	c, ok := s.caller(req.Token)
	if !ok {
		return Response{Why: "not a lane of this run (no or an unknown token)"}
	}
	return Response{Approved: true, Lane: c.Lane, Cell: c.Cell, Job: c.Job, Run: c.Run}
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serve(c)
		}()
	}
}

func (s *Server) serve(c net.Conn) {
	defer c.Close()
	var req Request
	// One request per connection, of at most MaxRequest bytes, sent within
	// ReadTimeout: a lane can't make the launcher buffer or wait without
	// limit. Anything else is declined.
	c.SetReadDeadline(time.Now().Add(ReadTimeout))
	lr := &io.LimitedReader{R: c, N: MaxRequest + 1}
	if err := json.NewDecoder(lr).Decode(&req); err != nil {
		why := "malformed request"
		var ne net.Error
		switch {
		case lr.N <= 0:
			why = fmt.Sprintf("request too large (over %d bytes)", MaxRequest)
		case errors.As(err, &ne) && ne.Timeout():
			why = "the request took too long to arrive"
		}
		json.NewEncoder(c).Encode(Response{Why: why})
		return
	}
	c.SetReadDeadline(time.Time{})
	if req.Token != "" {
		if caller, ok := s.caller(req.Token); ok {
			if why := from(c, caller); why != "" {
				resp := s.refuse(caller, req, why)
				json.NewEncoder(c).Encode(resp)
				return
			}
		}
	}
	switch req.Op {
	case "consume":
		json.NewEncoder(c).Encode(s.consume(req))
		return
	case "whoami":
		json.NewEncoder(c).Encode(s.whoami(req))
		return
	}
	who, req, why := s.check(req)
	if why != "" {
		resp := s.refuse(who, req, why)
		resp.Lane, resp.Job = who.Lane, who.Job // a refusal is the true lane's too
		json.NewEncoder(c).Encode(resp)
		return
	}
	// A lane that gives up (it was stopped, or its round timed out) closes
	// the connection; stop asking for it.
	gone := make(chan struct{})
	go func() {
		buf := make([]byte, 1)
		c.Read(buf)
		close(gone)
	}()
	var resp Response
	select {
	case <-gone:
		return
	default:
	}
	if s.isCancelled() {
		resp = Response{Why: "the run is stopping"}
	} else {
		cancel := make(chan struct{})
		stop := make(chan struct{})
		go func() {
			select {
			case <-gone:
			case <-s.cancel:
			case <-stop:
				return
			}
			close(cancel)
		}()
		resp = s.ask(req, cancel)
		close(stop)
		if !resp.Approved && resp.Why == "" && s.isCancelled() {
			resp.Why = "the run is stopping"
		}
	}
	resp.Lane, resp.Job, resp.ID = req.Lane, req.Job, ""
	if resp.Approved {
		resp.ID = newID()
		s.mu.Lock()
		s.issued[resp.ID] = &issued{token: req.Token, flag: req.Flag, grant: resp.Grant}
		s.mu.Unlock()
	}
	json.NewEncoder(c).Encode(resp)
}

func (s *Server) isCancelled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelled
}

// CancelAll declines the question being asked and every later one (the
// run is stopping).
func (s *Server) CancelAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.cancelled {
		s.cancelled = true
		close(s.cancel)
	}
}

// Close stops the server and removes its socket.
func (s *Server) Close() {
	s.CancelAll()
	s.ln.Close()
	s.wg.Wait()
	os.RemoveAll(s.dir)
}

// ErrNoAnswer is returned when the wait ends without an answer.
var ErrNoAnswer = errors.New("no answer")

// Ask sends req to the server at path and waits for the answer. It gives
// up, declining, when stopped() reports true (polled every 200ms) or the
// deadline (if not zero) passes; why says which.
func Ask(path string, req Request, stopped func() bool, deadline time.Time) (Response, error) {
	c, err := dial(path)
	if err != nil {
		return Response{}, fmt.Errorf("approval: %w", err)
	}
	defer c.Close()
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return Response{}, fmt.Errorf("approval: %w", err)
	}
	done := make(chan Response, 1)
	go func() {
		var resp Response
		if json.NewDecoder(bufio.NewReader(c)).Decode(&resp) == nil {
			done <- resp
		}
		close(done)
	}()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case resp, ok := <-done:
			if !ok {
				return Response{Why: "swim run went away"}, ErrNoAnswer
			}
			return resp, nil
		case <-tick.C:
			if stopped != nil && stopped() {
				return Response{Why: "the lane is stopping"}, ErrNoAnswer
			}
			if !deadline.IsZero() && time.Now().After(deadline) {
				return Response{Why: "the round's Timeout ran out"}, ErrNoAnswer
			}
		}
	}
}

// dial connects to the server, retrying for a few seconds while it is
// busy: every hidden command of every lane asks it (whoami), and a burst
// can fill the socket's backlog (EAGAIN). A socket that's gone fails at
// once.
func dial(path string) (net.Conn, error) {
	end := time.Now().Add(5 * time.Second)
	for {
		c, err := net.DialTimeout("unix", path, 2*time.Second)
		if err == nil || time.Now().After(end) {
			return c, err
		}
		if _, serr := os.Stat(path); serr != nil {
			return nil, err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Consume uses up approval id (from an approved Response) for guard flag,
// with the lane process's token: refused when another lane asked for it,
// it was for another guard, or it was already used. It returns the approve
// entry the launcher recorded ("" if it left that to the lane).
func Consume(path, token, id, flag string) (string, error) {
	resp, err := Ask(path, Request{Op: "consume", Token: token, ID: id, Flag: flag}, nil, time.Now().Add(10*time.Second))
	if err != nil {
		return "", err
	}
	if !resp.Approved {
		return "", errors.New(resp.Why)
	}
	return resp.Hash, nil
}

// Whoami asks the server which lane, cell, job and run token is for.
func Whoami(path, token string) (Response, error) {
	resp, err := Ask(path, Request{Op: "whoami", Token: token}, nil, time.Now().Add(10*time.Second))
	if err != nil {
		return resp, err
	}
	if !resp.Approved {
		return resp, errors.New(resp.Why)
	}
	return resp, nil
}

// Question is how an approval is put to the operator. The first lines are
// swim's own facts: the lane and job (the launcher's, under swim run), the
// guard, the manifest and the evidence hash; then what the lane says the
// guarded step does, labelled as the lane's. Everything the lane supplied
// goes through ui.Safe (Clean): escape sequences, carriage returns,
// backspaces or bidi controls can't hide or rewrite what the operator
// approves. (The chain keeps the text as given.)
func Question(req Request) []string {
	flag := Clean(req.Flag)
	who := fmt.Sprintf("swim %d", req.Lane)
	if req.Cell != "" {
		who += " [" + Clean(req.Cell) + "]"
	}
	q := []string{who + " requires human approval of guard " + flag}
	var facts []string
	if req.Job != "" {
		facts = append(facts, "job "+req.Job)
	}
	if req.Manifest != "" {
		facts = append(facts, "manifest "+short12(req.Manifest))
	}
	if req.EvidenceSHA != "" {
		facts = append(facts, "evidence "+short12(req.EvidenceSHA))
	}
	if len(facts) > 0 {
		q = append(q, "  "+strings.Join(facts, " · "))
	}
	q = append(q, "  the lane says: "+Clean(req.Desc))
	if len(req.Observed) > 0 {
		var keys []string
		for k := range req.Observed {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		q = append(q, "  observed now (checked again before the step):")
		for _, k := range keys {
			q = append(q, "    "+k+" = "+Clean(req.Observed[k]))
		}
	}
	if req.Evidence != "" {
		lines := strings.Split(strings.TrimRight(req.Evidence, "\n"), "\n")
		q = append(q, "evidence (what it will do):")
		for i, l := range lines {
			if i == 20 {
				q = append(q, fmt.Sprintf("  ... %d more lines (.swim/evidence/)", len(lines)-20))
				break
			}
			q = append(q, "  "+ui.Safe(l))
		}
	}
	return append(q, fmt.Sprintf("(or set %s=1 to approve without asking)  Y/N [N]", flag))
}

// Clean makes text a lane supplies safe to show (ui.Safe): no escape
// sequences, control characters or bidi overrides, so it can't redraw or
// hide part of the question.
func Clean(s string) string { return ui.Safe(s) }

func short12(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
