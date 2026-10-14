// Package approval lets a lane ask the operator to approve a guarded step
// while swim run owns the terminal. Parallel lanes have no stdin, so the
// lane's `guard` (through `swim _approve`) sends the request over a unix
// socket to the launcher, which asks the operator, one request at a time,
// and sends the answer back. The socket's path reaches lanes as
// SWIM_APPROVAL.
package approval

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// EnvVar holds the socket path in a lane's environment.
const EnvVar = "SWIM_APPROVAL"

// Request is one approval a lane asks for.
type Request struct {
	Lane int    `json:"lane"`
	Job  string `json:"job,omitempty"`
	Flag string `json:"flag"`
	Desc string `json:"desc"`
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
}

// Asker asks the operator; it returns false when cancel closes first.
type Asker func(req Request, cancel <-chan struct{}) Response

// Server answers requests from the lanes of one run.
type Server struct {
	ln  net.Listener
	dir string
	ask Asker // called concurrently, once per waiting lane: it decides how to queue them

	mu        sync.Mutex
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
	s := &Server{ln: ln, dir: dir, ask: ask, cancel: make(chan struct{})}
	s.wg.Add(1)
	go s.accept()
	return s, nil
}

// Path is the socket's path, for SWIM_APPROVAL.
func (s *Server) Path() string { return s.ln.Addr().String() }

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

// Question is how an approval is put to the operator.
func Question(req Request) []string {
	return []string{
		fmt.Sprintf("swim %d requires human approval of task: %s", req.Lane, req.Desc),
		fmt.Sprintf("(guard %s; or set %s=1 to approve without asking)  Y/N [N]", req.Flag, req.Flag),
	}
}
