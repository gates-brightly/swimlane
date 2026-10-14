package hub

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// What the hub keeps on disk (swim hub --state DIR): the workers it knows,
// each one's public key (age), whether the operator approved it, a hash of
// its token, and the secrets granted to it, encrypted to its key by the
// operator before they reach the hub. The hub never holds a secret's value
// or a key that could decrypt one.

// StateSchema identifies the state file.
const StateSchema = "swim.hub-state/v1"

// Record is one worker the hub knows.
type Record struct {
	Worker
	Key        string `json:"key,omitempty"`    // its age public key (age1...)
	KeyFP      string `json:"key_fp,omitempty"` // KeyFingerprint(Key)
	Approved   bool   `json:"approved"`
	ApprovedAt string `json:"approved_at,omitempty"`
	TokenHash  string `json:"token_hash"`
}

// Grant is one secret granted to a worker, encrypted to its key.
type Grant struct {
	Ciphertext string `json:"ciphertext"` // age, armored
	KeyFP      string `json:"key_fp"`     // the key it's encrypted to
	At         string `json:"at"`
	By         string `json:"by,omitempty"`
}

type state struct {
	Schema  string                      `json:"schema"`
	Workers map[string]*Record          `json:"workers"`
	Grants  map[string]map[string]Grant `json:"grants"` // worker id -> secret name -> grant
}

// KeyFingerprint is a short, stable name for a worker's public key.
func KeyFingerprint(key string) string {
	s := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(s[:])[:16]
}

func hashToken(t string) string {
	s := sha256.Sum256([]byte(t))
	return hex.EncodeToString(s[:])
}

func newToken() string {
	b := make([]byte, 24)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) statePath() string { return filepath.Join(s.dir, "state.json") }

// AdminTokenPath is where a hub keeps its admin token.
func AdminTokenPath(dir string) string { return filepath.Join(dir, "admin-token") }

// NewWithState makes a hub that keeps workers, approvals and grants in dir
// (made private if missing). admin is the admin token; "" uses the one
// saved in dir, making it on first start.
func NewWithState(token, admin, dir string) (*Server, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if admin == "" {
		data, err := os.ReadFile(AdminTokenPath(dir))
		switch {
		case err == nil:
			admin = strings.TrimSpace(string(data))
		case errors.Is(err, os.ErrNotExist):
			admin = newToken()
			if err := os.WriteFile(AdminTokenPath(dir), []byte(admin+"\n"), 0o600); err != nil {
				return nil, err
			}
		default:
			return nil, err
		}
	}
	s := New(token)
	s.admin, s.dir = admin, dir
	data, err := os.ReadFile(s.statePath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		var st state
		if err := json.Unmarshal(data, &st); err != nil {
			return nil, fmt.Errorf("%s: %w", s.statePath(), err)
		}
		if st.Schema != StateSchema {
			return nil, fmt.Errorf("%s: schema %q, want %s", s.statePath(), st.Schema, StateSchema)
		}
		if st.Workers != nil {
			s.records = st.Workers
		}
		if st.Grants != nil {
			s.grants = st.Grants
		}
	}
	// Known workers show (stale) until they report again.
	for id, r := range s.records {
		s.workers[id] = &View{Snapshot: Snapshot{Worker: r.Worker, Activity: "known; no report since the hub started",
			At: s.now().UTC().Format(time.RFC3339), Lanes: []Lane{}, Queue: []Batch{}, Events: []string{}}, Seen: s.now().Add(-time.Hour)}
	}
	return s, nil
}

// AdminToken is the hub's admin token ("" for a hub without state).
func (s *Server) AdminToken() string { return s.admin }

// save writes the state; s.mu is held.
func (s *Server) save() error {
	if s.dir == "" {
		return nil
	}
	data, err := json.MarshalIndent(state{Schema: StateSchema, Workers: s.records, Grants: s.grants}, "", "  ")
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", s.statePath(), os.Getpid())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.statePath())
}

// audit appends a line to the hub's audit log (dir/audit.log).
func (s *Server) audit(format string, a ...any) {
	if s.dir == "" {
		return
	}
	f, err := os.OpenFile(filepath.Join(s.dir, "audit.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s  %s\n", s.now().UTC().Format(time.RFC3339), fmt.Sprintf(format, a...))
}

func tokenIs(got, want string) bool {
	return want != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func bearer(r interface{ Get(string) string }) string {
	return strings.TrimPrefix(r.Get("Authorization"), "Bearer ")
}

// isAdmin: the request carries the admin token.
func (s *Server) isAdmin(got string) bool { return tokenIs(got, s.admin) }

// isWorker: the request carries worker id's own token. s.mu is held.
func (s *Server) isWorker(id, got string) bool {
	r := s.records[id]
	return r != nil && r.TokenHash != "" && tokenIs(hashToken(got), r.TokenHash)
}

// Approve marks worker id approved for secrets; false if unknown or it has
// no key.
func (s *Server) Approve(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.records[id]
	if r == nil {
		return "", errors.New("no such worker")
	}
	if r.Key == "" {
		return "", fmt.Errorf("%s enrolled without a key (an older swim): enroll it again", r.Name)
	}
	if r.Approved {
		return fmt.Sprintf("%s is already approved (key %s)", r.Name, r.KeyFP), nil
	}
	r.Approved, r.ApprovedAt = true, s.now().UTC().Format(time.RFC3339)
	if err := s.save(); err != nil {
		return "", err
	}
	s.audit("approved %s (%s, %s:%s) key %s", r.ID, r.Name, r.Host, r.Root, r.KeyFP)
	return fmt.Sprintf("approved %s (key %s): it can now be granted secrets", r.Name, r.KeyFP), nil
}

// Unapprove withdraws worker id's approval and deletes its grants.
func (s *Server) Unapprove(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.records[id]
	if r == nil {
		return "", errors.New("no such worker")
	}
	n := len(s.grants[id])
	r.Approved, r.ApprovedAt = false, ""
	delete(s.grants, id)
	if err := s.save(); err != nil {
		return "", err
	}
	s.audit("unapproved %s (%s); deleted %d grants", r.ID, r.Name, n)
	return fmt.Sprintf("unapproved %s; deleted its %d grants (rotate any secret it held)", r.Name, n), nil
}

// grantNames lists worker id's granted secrets. s.mu is held.
func (s *Server) grantNames(id string) []string {
	var out []string
	for n := range s.grants[id] {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (s *Server) adminOnly(w http.ResponseWriter, r *http.Request) bool {
	if s.admin == "" {
		http.Error(w, "this hub keeps no state (swim hub --state DIR): nothing to approve or grant", http.StatusNotImplemented)
		return false
	}
	if !s.isAdmin(bearer(r.Header)) {
		http.Error(w, "needs the hub's admin token", http.StatusUnauthorized)
		return false
	}
	return true
}

func (s *Server) approveAPI(w http.ResponseWriter, r *http.Request) {
	if !s.adminOnly(w, r) {
		return
	}
	var msg string
	var err error
	if r.Method == http.MethodDelete {
		msg, err = s.Unapprove(r.PathValue("id"))
	} else {
		msg, err = s.Approve(r.PathValue("id"))
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"message": msg})
}

func (s *Server) keyAPI(w http.ResponseWriter, r *http.Request) {
	if !s.adminOnly(w, r) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.records[r.PathValue("id")]
	if rec == nil {
		http.Error(w, "no such worker", http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"id": rec.ID, "name": rec.Name, "key": rec.Key, "key_fp": rec.KeyFP, "approved": rec.Approved})
}

// GrantInfo is a grant as listed: never its ciphertext.
type GrantInfo struct {
	Worker string `json:"worker"`
	Name   string `json:"name"`
	Secret string `json:"secret"`
	At     string `json:"at"`
	By     string `json:"by,omitempty"`
}

func (s *Server) grantsAPI(w http.ResponseWriter, r *http.Request) {
	if !s.adminOnly(w, r) {
		return
	}
	s.mu.Lock()
	var out []GrantInfo
	for id, m := range s.grants {
		name := id
		if rec := s.records[id]; rec != nil {
			name = rec.Name
		}
		for n, g := range m {
			out = append(out, GrantInfo{Worker: id, Name: name, Secret: n, At: g.At, By: g.By})
		}
	}
	s.mu.Unlock()
	sort.Slice(out, func(a, b int) bool {
		if out[a].Name != out[b].Name {
			return out[a].Name < out[b].Name
		}
		return out[a].Secret < out[b].Secret
	})
	if out == nil {
		out = []GrantInfo{}
	}
	json.NewEncoder(w).Encode(map[string]any{"grants": out})
}

func (s *Server) grantAPI(w http.ResponseWriter, r *http.Request) {
	if !s.adminOnly(w, r) {
		return
	}
	id, name := r.PathValue("id"), r.PathValue("name")
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.records[id]
	if rec == nil {
		http.Error(w, "no such worker", http.StatusNotFound)
		return
	}
	if r.Method == http.MethodDelete {
		if _, ok := s.grants[id][name]; !ok {
			http.Error(w, "no grant "+name+" for "+rec.Name, http.StatusNotFound)
			return
		}
		delete(s.grants[id], name)
		s.save()
		s.audit("revoked %s from %s (%s)", name, id, rec.Name)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var g Grant
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&g); err != nil || g.Ciphertext == "" {
		http.Error(w, "a grant needs {ciphertext, key_fp}", http.StatusBadRequest)
		return
	}
	switch {
	case !rec.Approved:
		http.Error(w, rec.Name+" isn't approved: approve it on the hub first", http.StatusConflict)
		return
	case g.KeyFP != rec.KeyFP:
		http.Error(w, "encrypted to "+g.KeyFP+", but "+rec.Name+"'s key is "+rec.KeyFP+": fetch its key again", http.StatusConflict)
		return
	}
	g.At = s.now().UTC().Format(time.RFC3339)
	if s.grants[id] == nil {
		s.grants[id] = map[string]Grant{}
	}
	s.grants[id][name] = g
	if err := s.save(); err != nil {
		http.Error(w, "hub state: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit("granted %s to %s (%s) by %s", name, id, rec.Name, g.By)
	w.WriteHeader(http.StatusNoContent)
}

// Fetched is one grant as its worker gets it.
type Fetched struct {
	Name       string `json:"name"`
	Ciphertext string `json:"ciphertext"`
}

// fetchAPI gives a worker its own grants: only with its own token, only
// while approved, only those encrypted to its current key.
func (s *Server) fetchAPI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.isWorker(id, bearer(r.Header)) {
		http.Error(w, "needs this worker's own token", http.StatusUnauthorized)
		return
	}
	rec := s.records[id]
	if !rec.Approved {
		http.Error(w, rec.Name+" isn't approved on the hub yet", http.StatusForbidden)
		return
	}
	out := []Fetched{}
	for n, g := range s.grants[id] {
		if g.KeyFP == rec.KeyFP {
			out = append(out, Fetched{Name: n, Ciphertext: g.Ciphertext})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	s.fetched[id] = s.now()
	if len(out) > 0 {
		names := make([]string, len(out))
		for i, f := range out {
			names[i] = f.Name
		}
		s.audit("%s (%s) fetched %s", id, rec.Name, strings.Join(names, ", "))
	}
	json.NewEncoder(w).Encode(map[string]any{"grants": out})
}
