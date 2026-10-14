package secrets

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The broker: a swim run decrypts its secrets once, at start, and serves
// them to its lanes on a private unix socket. Lanes find it through
// SockEnv and prove they belong to the run with TokenEnv.
const (
	SockEnv  = "SWIM_SECRETS"
	TokenEnv = "SWIM_SECRETS_TOKEN"
)

// Request is one call to the broker.
type Request struct {
	Token string `json:"token"`
	Op    string `json:"op"` // get | masks
	Name  string `json:"name,omitempty"`
	Lane  int    `json:"lane,omitempty"`
	How   string `json:"how,omitempty"` // get (swim secret get) | step (_swim.<name>)
}

// Mask is a secret's value for masking, by name.
type Mask struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Response is the broker's answer.
type Response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Value string `json:"value,omitempty"`
	Scope string `json:"scope,omitempty"`
	Masks []Mask `json:"masks,omitempty"`
}

// Server serves one run's vault.
type Server struct {
	ln    net.Listener
	dir   string
	token string
	vault *Vault
	used  func(lane int, s Secret, how string)
	wg    sync.WaitGroup
}

// Serve starts the broker for vault; used is called (concurrently) for
// each secret a lane gets.
func Serve(vault *Vault, used func(lane int, s Secret, how string)) (*Server, error) {
	dir, err := os.MkdirTemp("", "swim-se-")
	if err != nil {
		return nil, err
	}
	os.Chmod(dir, 0o700)
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	b := make([]byte, 16)
	rand.Read(b)
	s := &Server{ln: ln, dir: dir, token: hex.EncodeToString(b), vault: vault, used: used}
	s.wg.Add(1)
	go s.accept()
	return s, nil
}

// Env is what a lane needs to reach the broker.
func (s *Server) Env() []string {
	return []string{SockEnv + "=" + s.ln.Addr().String(), TokenEnv + "=" + s.token}
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
			defer c.Close()
			c.SetDeadline(time.Now().Add(10 * time.Second))
			var req Request
			if err := json.NewDecoder(bufio.NewReader(c)).Decode(&req); err != nil {
				return
			}
			json.NewEncoder(c).Encode(s.answer(req))
		}()
	}
}

func (s *Server) answer(req Request) Response {
	if req.Token != s.token {
		return Response{Error: "not this run's lane (bad token)"}
	}
	switch req.Op {
	case "get":
		sec, err := s.vault.Get(req.Name)
		if err != nil {
			return Response{Error: err.Error()}
		}
		if s.used != nil {
			s.used(req.Lane, sec, req.How)
		}
		return Response{OK: true, Value: sec.Value, Scope: sec.Scope}
	case "masks":
		var m []Mask
		for _, sec := range s.vault.All() {
			m = append(m, Mask{Name: sec.Name, Value: sec.Value})
		}
		return Response{OK: true, Masks: m}
	}
	return Response{Error: "unknown op " + req.Op}
}

// Close stops the broker and removes its socket.
func (s *Server) Close() {
	s.ln.Close()
	s.wg.Wait()
	os.RemoveAll(s.dir)
}

// Call asks the broker named in env (nil, false when there is none).
func Call(getenv func(string) string, req Request) (*Response, bool, error) {
	path := getenv(SockEnv)
	if path == "" {
		return nil, false, nil
	}
	req.Token = getenv(TokenEnv)
	c, err := net.DialTimeout("unix", path, 5*time.Second)
	if err != nil {
		return nil, true, errors.New("the run's secrets broker is gone: " + err.Error())
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return nil, true, err
	}
	var resp Response
	if err := json.NewDecoder(bufio.NewReader(c)).Decode(&resp); err != nil {
		return nil, true, err
	}
	if !resp.OK {
		return &resp, true, errors.New(resp.Error)
	}
	return &resp, true, nil
}
