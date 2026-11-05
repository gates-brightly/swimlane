// Package approval lets a lane ask the operator to approve a guarded step
// while swim run owns the terminal. Parallel lanes have no stdin, so the
// lane's `guard` (through `swim _approve`) sends the request over a unix
// socket to the launcher, which asks the operator, one request at a time,
// and sends the answer back. Both ends are always the same swim binary: a
// lane program never speaks this protocol itself, and it may change in
// any release.
//
// Transport. The socket is mode 0600 in a 0700 temp directory; its path
// reaches lanes as SWIM_APPROVAL. Each lane process (each matrix cell)
// gets its own random token, SWIM_APPROVAL_TOKEN, registered with what the
// launcher knows of it (lane, cell, round id, run, manifest, declared
// guards), bound to the process once it starts and revoked when it exits.
// Where the OS names a socket's peer (Linux SO_PEERCRED, macOS
// LOCAL_PEERPID), a request must come from the lane's process or a
// descendant: a token read from another lane's environ, or used by a
// process that left its lane's tree, is refused. Elsewhere the token alone
// decides. One request per connection: one JSON line of at most
// MaxRequest bytes, sent within 10 seconds; a larger, malformed or slower
// one is declined and the connection closed.
//
// Requests. With no op, ask for a guard: the lane's Lane and Job are
// ignored for what the launcher registered for the token, and its Desc is
// shown labelled as the lane's ("the lane says: ..."), control characters
// removed. FlagSet says the lane's environment has FLAG=1. Op "consume"
// uses an approved answer's request id, once, with the token it was given
// to; under swim run the launcher records the approve entry in the chain
// then (Server.Record) and answers its hash, so a lane never writes its
// own approval. Op "whoami" says which lane, cell, round id and run the
// token is for, so swim's hidden commands (_start, _mark, _stage, _finish)
// can't report for another lane. Op "record" takes a round's chain entry:
// the launcher signs it under the lane, round id, run and manifest it
// registered, so a lane needs no key.
//
// How the launcher decides, from what it saw when the run started:
//  1. FlagSet: approved only if the launcher's own environment (the one
//     swim run started with, or a queued batch's --approve) has FLAG=1; a
//     flag set inside the lane, by an env file or anything it runs, is
//     declined with that reason.
//  2. The operator: a pre-answer in the live view, or the prompt (the
//     terminal, or swim watch's screen), when someone can be asked. A
//     guard the script doesn't declare is refused before this.
//  3. Otherwise a dry run: Approved false with Dry true, and Why saying
//     what didn't approve it.
//
// Every question is declined when the lane hangs up, the run is stopping,
// or the round's Timeout runs out. Refusals (an unknown or revoked token,
// a process outside the lane, an undeclared guard, a reused or foreign
// id) are shown in the live view and logged in .swim.log. When the run's
// last lane exits the launcher appends the run's attestation, which swim
// verify checks every approve entry against
// (docs/reference/trust-model.md).
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
	"strings"
	"sync"
	"sync/atomic"
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
// token (they stay on the wire for older lanes). Manifest is the server's
// alone.
type Request struct {
	Lane     int    `json:"lane"`
	Job      string `json:"job,omitempty"`
	Manifest string `json:"-"`
	Flag     string `json:"flag"`
	Desc     string `json:"desc"`
	// Token identifies the lane process (TokenEnv). Op is "" to ask,
	// "consume" to use up the approval with request id ID, "whoami" to
	// learn which lane and run the token is for, or "record" to have
	// the launcher record Entry.
	Token string       `json:"token,omitempty"`
	Op    string       `json:"op,omitempty"`
	ID    string       `json:"id,omitempty"`
	Entry *chain.Entry `json:"entry,omitempty"`
	// FlagSet: the lane's environment has Flag=1. Under swim run that
	// approves only if swim run's own environment set it.
	FlagSet bool `json:"flag_set,omitempty"`
	// RawDesc is the description as the lane sent it, for the chain; Desc
	// is made safe to show (Clean).
	RawDesc string `json:"-"`
}

// Response is the operator's answer.
type Response struct {
	Approved bool   `json:"approved"`
	By       string `json:"by,omitempty"`  // who answered (the operator's user name)
	Why      string `json:"why,omitempty"` // for a decline without an answer: "the run is stopping", ...
	Pre      bool   `json:"pre,omitempty"` // answered ahead of time in the live view (pre-approved / pre-declined)
	At       string `json:"at,omitempty"`  // when a pre-answer was given (hh:mm:ss)
	// ID is the approval's request id, to consume once (approved only).
	// Lane and Job are who the launcher says asked.
	ID   string `json:"id,omitempty"`
	Lane int    `json:"lane,omitempty"`
	Job  string `json:"job,omitempty"`
	Run  string `json:"run,omitempty"`
	// Dry: no one was asked (nobody can be, under swim run); the guard is a
	// dry run, and Why (if set) says why no signed approval counted.
	Dry bool `json:"dry,omitempty"`
	// How is the APPROVED line's detail when the launcher decided
	// ("approved by ... (signed)", "FLAG=1"); "" to build it from By, Auto,
	// Via, Pre and At.
	How string `json:"how,omitempty"`
	// Hash is the approve entry the launcher recorded as the approval was
	// consumed ("" if it leaves recording to the lane).
	Hash string `json:"hash,omitempty"`
	// Grant is what the launcher records once the approval is consumed.
	Grant *Grant `json:"-"`
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
	Job      string // "" if the launcher doesn't know it yet
	Run      string
	Manifest string
	Guards   []string // the flags its script declares or uses
	// AnyGuard: its script names a guard's flag at run time (guard "$F"),
	// so it may ask for a flag Guards doesn't list.
	AnyGuard bool
	// PID is the lane's process: requests must come from it or its
	// descendants (where the platform can tell, package peer). AwaitPID
	// registers a process not started yet (SetPID names it); 0 checks the
	// token alone (a process swim itself runs for the lane).
	PID int
	// Token is the lane process's token (set by Register).
	Token string
}

// AwaitPID is Caller.PID for a lane process about to start.
const AwaitPID = -1

// MaxRequest caps one request: a lane can't make the launcher buffer more.
const MaxRequest = 1 << 20

// readTimeout is how long a connection may take to send its request
// (nanoseconds; atomic, since a test shortens it while the server runs).
var readTimeout atomic.Int64

func init() { readTimeout.Store(int64(10 * time.Second)) }

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
	// Chain, if set, records an entry a lane process sends (op record)
	// and returns its hash; an error refuses it. Without it, refused.
	Chain func(c Caller, e chain.Entry) (string, error)

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
	c.Token = tok
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
		return "lane " + fmt.Sprint(c.Lane) + "'s process never started"
	}
	ok, known := peer.FromLane(conn, c.PID)
	switch {
	case !known:
		return "can't tell which process sent it"
	case !ok:
		p, _ := peer.PID(conn)
		return fmt.Sprintf("it came from process %d, outside lane %d's lane (its token was used by another process)", p, c.Lane)
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
		req.Desc = fmt.Sprintf("%s (sent as lane %d)", req.Desc, req.Lane)
	}
	req.Lane, req.Job, req.Manifest = c.Lane, c.Job, c.Manifest
	// A flag the lane has set is checked against swim run's own
	// environment, which names it: the operator set it, declared or not.
	declared := req.Flag == ConfirmFlag || req.FlagSet || c.AnyGuard
	for _, g := range c.Guards {
		declared = declared || g == req.Flag
	}
	if !declared {
		return c, req, fmt.Sprintf("guard %s isn't declared in lane %d's script (list it in # Guards:)", req.Flag, c.Lane)
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

// whoami says which lane, job and run the token is for.
func (s *Server) whoami(req Request) Response {
	c, ok := s.caller(req.Token)
	if !ok {
		return Response{Why: "not a lane of this run (no or an unknown token)"}
	}
	return Response{Approved: true, Lane: c.Lane, Job: c.Job, Run: c.Run}
}

// record has the launcher record the entry a lane process sends.
func (s *Server) record(req Request) Response {
	c, ok := s.caller(req.Token)
	switch {
	case !ok:
		return Response{Why: "not a lane of this run (no or an unknown token)"}
	case req.Entry == nil || s.Chain == nil:
		return Response{Why: "nothing to record"}
	}
	hash, err := s.Chain(c, *req.Entry)
	if err != nil {
		return Response{Why: err.Error()}
	}
	return Response{Approved: true, Lane: c.Lane, Run: c.Run, Hash: hash}
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
	// the read timeout: a lane can't make the launcher buffer or wait
	// without limit. Anything else is declined.
	c.SetReadDeadline(time.Now().Add(time.Duration(readTimeout.Load())))
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
	case "record":
		json.NewEncoder(c).Encode(s.record(req))
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

// Record has the launcher record e in the chain for the lane process with
// token, and returns the entry's hash.
func Record(path, token string, e chain.Entry) (string, error) {
	resp, err := Ask(path, Request{Op: "record", Token: token, Entry: &e}, nil, time.Now().Add(30*time.Second))
	if err != nil {
		return "", err
	}
	if !resp.Approved {
		return "", errors.New(resp.Why)
	}
	return resp.Hash, nil
}

// Whoami asks the server which lane, job and run token is for.
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
// guard and the manifest; then what the lane says the
// guarded step does, labelled as the lane's. Everything the lane supplied
// goes through ui.Safe (Clean): escape sequences, carriage returns,
// backspaces or bidi controls can't hide or rewrite what the operator
// approves. (The chain keeps the text as given.)
func Question(req Request) []string {
	flag := Clean(req.Flag)
	who := fmt.Sprintf("lane %d", req.Lane)
	q := []string{who + " requires human approval of guard " + flag}
	var facts []string
	if req.Job != "" {
		facts = append(facts, "round-id "+req.Job)
	}
	if req.Manifest != "" {
		facts = append(facts, "manifest "+short12(req.Manifest))
	}
	if len(facts) > 0 {
		q = append(q, "  "+strings.Join(facts, " · "))
	}
	q = append(q, "  the lane says: "+Clean(req.Desc))
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
