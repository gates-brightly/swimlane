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
// A request without a known token, or for a guard its script doesn't
// declare, is refused. The lane's description is shown, labelled as the
// lane's. Each approval carries a request id that the lane must consume,
// once, before it records the approval.
package approval

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
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
	// Token identifies the lane process (TokenEnv). Op is "" to ask, or
	// "consume" to use up the approval with request id ID.
	Token string `json:"token,omitempty"`
	Op    string `json:"op,omitempty"`
	ID    string `json:"id,omitempty"`
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
	Observed map[string]string `json:"observed,omitempty"`
}

// Caller is a lane process the launcher started, registered under its
// token.
type Caller struct {
	Lane     int
	Cell     string // a matrix cell or job, "" for the lane itself
	Job      string // "" if the launcher doesn't know it yet
	Manifest string
	Guards   []string // the flags its script declares or uses
}

// issued is an approval handed out and not yet consumed.
type issued struct {
	token, flag string
	consumed    bool
}

// Asker asks the operator; it returns false when cancel closes first.
type Asker func(req Request, cancel <-chan struct{}) Response

// Server answers requests from the lanes of one run.
type Server struct {
	ln  net.Listener
	dir string
	ask Asker // called concurrently, once per waiting lane: it decides how to queue them
	// Refused, if set, is told of each request refused without asking: no
	// or an unknown token, a guard the script doesn't declare, a reused
	// response.
	Refused func(c Caller, req Request, why string)

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

func newID() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// caller is the registered lane for a token.
func (s *Server) caller(tok string) (Caller, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.callers[tok]
	return c, ok && tok != ""
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
	req.Desc = Clean(req.Desc)
	if req.Lane != c.Lane {
		req.Desc = fmt.Sprintf("%s (sent as swim %d)", req.Desc, req.Lane)
	}
	req.Lane, req.Job, req.Cell, req.Manifest = c.Lane, c.Job, c.Cell, c.Manifest
	declared := req.Flag == ConfirmFlag
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
	return Response{Approved: true, ID: req.ID, Lane: c.Lane, Job: c.Job}
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
	if err := json.NewDecoder(bufio.NewReader(c)).Decode(&req); err != nil {
		return
	}
	if req.Op == "consume" {
		json.NewEncoder(c).Encode(s.consume(req))
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
		s.issued[resp.ID] = &issued{token: req.Token, flag: req.Flag}
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
	c, err := net.DialTimeout("unix", path, 2*time.Second)
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

// Consume uses up approval id (from an approved Response) for guard flag,
// with the lane process's token: refused when another lane asked for it,
// it was for another guard, or it was already used.
func Consume(path, token, id, flag string) error {
	resp, err := Ask(path, Request{Op: "consume", Token: token, ID: id, Flag: flag}, nil, time.Now().Add(10*time.Second))
	if err != nil {
		return err
	}
	if !resp.Approved {
		return errors.New(resp.Why)
	}
	return nil
}

// Question is how an approval is put to the operator. The first lines are
// swim's own facts: the lane and job (the launcher's, under swim run), the
// guard, the manifest and the evidence hash; then what the lane says the
// guarded step does, labelled as the lane's.
func Question(req Request) []string {
	who := fmt.Sprintf("swim %d", req.Lane)
	if req.Cell != "" {
		who += " [" + req.Cell + "]"
	}
	q := []string{who + " requires human approval of guard " + req.Flag}
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
			q = append(q, "  "+l)
		}
	}
	return append(q, fmt.Sprintf("(or set %s=1 to approve without asking)  Y/N [N]", req.Flag))
}

// Clean drops control characters (escape sequences, carriage returns) from
// text a lane supplies, so it can't redraw or hide part of the question.
func Clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0 {
			return -1
		}
		return r
	}, s)
}

func short12(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
