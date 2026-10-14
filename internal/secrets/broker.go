package secrets

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
	"strconv"
	"strings"
	"sync"
	"time"
)

// The broker: a swim run decrypts its secrets once, at start, and serves
// them to its lanes on a private unix socket. Lanes find it through
// SockEnv and prove they belong to the run with TokenEnv; each lane
// process also sends its own token (LaneTokenEnv, the approval broker's),
// from which the launcher says which lane asks. What a request says about
// its lane is never believed.
const (
	SockEnv  = "SWIM_SECRETS"
	TokenEnv = "SWIM_SECRETS_TOKEN"
	// LaneTokenEnv is the lane process's token (approval.TokenEnv).
	LaneTokenEnv = "SWIM_APPROVAL_TOKEN"
)

// MaxRequest caps one request: a lane can't make the launcher buffer more.
const MaxRequest = 1 << 20

// Request is one call to the broker.
type Request struct {
	Token string `json:"token"`
	// LaneToken is the lane process's token (LaneTokenEnv). Without one a
	// request gets only unguarded secrets.
	LaneToken string `json:"lane_token,omitempty"`
	Op        string `json:"op"` // get | masks
	Name      string `json:"name,omitempty"`
	// Lane is the lane older clients said they were: ignored (the launcher
	// says, from LaneToken).
	Lane int    `json:"lane,omitempty"`
	How  string `json:"how,omitempty"` // get (swim secret get) | sdk
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

// Policy is how a guarded secret is released (swim.yml's secrets:, and a
// hub grant's own guard).
type Policy struct {
	// Guards are the flags that release it: every group must be approved,
	// by any one of its flags (swim.yml's guard: is one group; a hub
	// grant's guard another).
	Guards [][]string
	// Step is release: step: handed out once per lane process.
	Step bool
}

// Guarded reports whether the secret needs an approval.
func (p Policy) Guarded() bool { return len(p.Guards) > 0 }

// Flags lists every flag of every group.
func (p Policy) Flags() []string {
	var out []string
	for _, g := range p.Guards {
		out = append(out, g...)
	}
	return out
}

// Caller is the lane process asking, as the launcher knows it.
type Caller struct {
	Lane      int
	Cell, Job string
	// Key is the lane process (its token): a release is for one process.
	// "" when the request didn't say (no lane token).
	Key string
}

// Approval is how a guard flag was approved for a lane process.
type Approval struct {
	Flag string
	Via  string // the approve entry's method: prompt, auto, signature, flag, queue, ...
	// Entry is the approve entry's hash in the chain ("" if none).
	Entry string
}

// Hooks are the launcher's side of the broker. Each may be nil.
type Hooks struct {
	// Identify says which lane process sent a request with lane token tok
	// on conn, or why it isn't one of this run's.
	Identify func(conn net.Conn, tok string) (Caller, string)
	// Approved says whether flag was approved for the lane process c.
	Approved func(c Caller, flag string) (Approval, bool)
	// Used is told of every secret handed out; Released of a guarded one
	// (with the approvals that released it), Refused of one refused.
	Used     func(c Caller, s Secret, how string)
	Released func(c Caller, s Secret, by []Approval)
	Refused  func(c Caller, name string, flags []string, why string)
	// Check is swim plan --check: a guarded secret is never released.
	Check bool
}

// Server serves one run's vault.
type Server struct {
	ln       net.Listener
	dir      string
	token    string
	vault    *Vault
	policies map[string]Policy
	hooks    Hooks
	wg       sync.WaitGroup

	mu       sync.Mutex
	handed   map[string]map[string]int // lane process -> guarded secret -> gets
	released map[int]map[string]bool   // lane -> guarded secrets released to it (EnvName), for masks
	gen      int
}

// Serve starts the broker for vault; policies are the guarded secrets.
func Serve(vault *Vault, policies map[string]Policy, hooks Hooks) (*Server, error) {
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
	s := &Server{ln: ln, dir: dir, token: hex.EncodeToString(b), vault: vault, policies: policies, hooks: hooks,
		handed: map[string]map[string]int{}, released: map[int]map[string]bool{}}
	s.bump()
	s.wg.Add(1)
	go s.accept()
	return s, nil
}

// Env is what a lane needs to reach the broker.
func (s *Server) Env() []string {
	return []string{SockEnv + "=" + s.ln.Addr().String(), TokenEnv + "=" + s.token}
}

// GenPath is the broker's generation file, next to its socket at sock:
// rewritten whenever a guarded secret is released, so a long-lived masker
// (a lane's wrapper) knows to ask for masks again.
func GenPath(sock string) string { return filepath.Join(filepath.Dir(sock), "gen") }

// bump rewrites the generation file.
func (s *Server) bump() {
	s.gen++
	os.WriteFile(filepath.Join(s.dir, "gen"), []byte(strconv.Itoa(s.gen)+"\n"), 0o600)
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
			if err := json.NewDecoder(io.LimitReader(c, MaxRequest)).Decode(&req); err != nil {
				return
			}
			json.NewEncoder(c).Encode(s.answer(c, req))
		}()
	}
}

func (s *Server) answer(conn net.Conn, req Request) Response {
	if req.Token != s.token {
		return Response{Error: "not this run's lane (bad token)"}
	}
	var c Caller
	if req.LaneToken != "" && s.hooks.Identify != nil {
		var why string
		if c, why = s.hooks.Identify(conn, req.LaneToken); why != "" {
			if p := s.policy(req.Name); req.Op == "get" && p.Guarded() && s.hooks.Refused != nil {
				s.hooks.Refused(Caller{}, req.Name, p.Flags(), why)
			}
			return Response{Error: "refused: " + why}
		}
		c.Key = req.LaneToken
	}
	switch req.Op {
	case "get":
		sec, err := s.vault.Get(req.Name)
		if err != nil {
			return Response{Error: err.Error()}
		}
		if p := s.policy(sec.Name); p.Guarded() {
			by, why := s.release(c, sec.Name, p)
			if why != "" {
				if s.hooks.Refused != nil {
					s.hooks.Refused(c, sec.Name, p.Flags(), why)
				}
				return Response{Error: "refused: " + why}
			}
			if s.hooks.Released != nil {
				s.hooks.Released(c, sec, by)
			}
		}
		if s.hooks.Used != nil {
			s.hooks.Used(c, sec, req.How)
		}
		return Response{OK: true, Value: sec.Value, Scope: sec.Scope}
	case "masks":
		// Every unguarded secret (any lane may get them), and the guarded
		// ones released to this lane: never a guarded value the lane
		// couldn't get itself.
		var m []Mask
		s.mu.Lock()
		for _, sec := range s.vault.All() {
			if s.policy(sec.Name).Guarded() && (c.Key == "" || !s.released[c.Lane][EnvName(sec.Name)]) {
				continue
			}
			m = append(m, Mask{Name: sec.Name, Value: sec.Value})
		}
		s.mu.Unlock()
		return Response{OK: true, Masks: m}
	}
	return Response{Error: "unknown op " + req.Op}
}

func (s *Server) policy(name string) Policy { return PolicyFor(s.policies, name) }

// PolicyFor is name's guard in policies. A SWIM_SECRET_<NAME> stand-in is
// named in lower case (db_password): a guard on any spelling of its
// variable's name applies.
func PolicyFor(policies map[string]Policy, name string) Policy {
	if p, ok := policies[name]; ok {
		return p
	}
	for n, p := range policies {
		if EnvName(n) == EnvName(name) {
			return p
		}
	}
	return Policy{}
}

// release decides whether guarded secret name goes to lane process c:
// every guard group approved for it, in this run, and (release: step) not
// handed to it before. It returns the approvals, or why not.
func (s *Server) release(c Caller, name string, p Policy) ([]Approval, string) {
	flags := strings.Join(p.Flags(), " or ")
	switch {
	case s.hooks.Check:
		return nil, fmt.Sprintf("%s is guarded by %s, and swim plan --check never releases a guarded secret", name, flags)
	case c.Key == "" || s.hooks.Approved == nil:
		return nil, fmt.Sprintf("%s is guarded by %s, and the request didn't come from a lane of this run (no lane token)", name, flags)
	}
	var by []Approval
	for _, g := range p.Guards {
		found := false
		for _, f := range g {
			if a, ok := s.hooks.Approved(c, f); ok {
				a.Flag = f
				by = append(by, a)
				found = true
				break
			}
		}
		if !found {
			if len(g) == 1 {
				return nil, fmt.Sprintf("%s is guarded by %s, which isn't approved in this run", name, g[0])
			}
			return nil, fmt.Sprintf("%s is guarded by %s, none of which is approved in this run", name, strings.Join(g, " or "))
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := EnvName(name) // a stand-in's spelling and the store's are one secret
	if p.Step && s.handed[c.Key][key] > 0 {
		return nil, fmt.Sprintf("%s is released for one step (release: step), and this lane already got it", name)
	}
	if s.handed[c.Key] == nil {
		s.handed[c.Key] = map[string]int{}
	}
	s.handed[c.Key][key]++
	if !s.released[c.Lane][key] {
		if s.released[c.Lane] == nil {
			s.released[c.Lane] = map[string]bool{}
		}
		s.released[c.Lane][key] = true
		s.bump()
	}
	return by, ""
}

// Close stops the broker and removes its socket.
func (s *Server) Close() {
	s.ln.Close()
	s.wg.Wait()
	os.RemoveAll(s.dir)
}

// Call asks the broker named in env (nil, false when there is none), as
// the lane process env belongs to.
func Call(getenv func(string) string, req Request) (*Response, bool, error) {
	path := getenv(SockEnv)
	if path == "" {
		return nil, false, nil
	}
	req.Token = getenv(TokenEnv)
	req.LaneToken = getenv(LaneTokenEnv)
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
