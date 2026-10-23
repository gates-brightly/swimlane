package hub

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/ui"
)

// Machines: a machine-wide enrollment (swim auth login, kept in the
// user's ~/.config/swim). The machine enrolls once and waits, as a worker
// does, for the admin to admit it; once admitted, any directory on it
// that runs swim watch enrolls with the machine's token and is admitted at
// once, marked as the machine's. Removing the machine (swim auth logout,
// or the admin) removes every directory that joined through it.

// Machine is one machine-wide enrollment.
type Machine struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Host       string `json:"host"`
	User       string `json:"user,omitempty"`
	Access     string `json:"access,omitempty"` // what its directories enroll with: read or readwrite
	Version    string `json:"version,omitempty"`
	TokenHash  string `json:"token_hash"`
	Pending    bool   `json:"pending,omitempty"`
	EnrolledAt string `json:"enrolled_at,omitempty"`
	From       string `json:"from,omitempty"`
	AdmittedAt string `json:"admitted_at,omitempty"`
	AdmittedBy string `json:"admitted_by,omitempty"`
}

// MachineIDFor is the id of user's enrollment on host.
func MachineIDFor(host, user string) string { return "m-" + IDFor(host, "user:"+user) }

// isMachineID reports whether id names a machine, not a worker.
func isMachineID(id string) bool { return strings.HasPrefix(id, "m-") }

// machineByToken is the machine got is the token of. s.mu is held.
func (s *Server) machineByToken(got string) *Machine {
	if got == "" {
		return nil
	}
	h := hashToken(got)
	for _, m := range s.machines {
		if m.TokenHash != "" && tokenIs(h, m.TokenHash) {
			return m
		}
	}
	return nil
}

func (s *Server) machineRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/machines", s.machineEnrollAPI)
	mux.HandleFunc("GET /api/machines/me", s.machineMeAPI)
	mux.HandleFunc("DELETE /api/machines/me", s.machineLeaveAPI)
	mux.HandleFunc("GET /api/machines", s.machinesAPI)
	mux.HandleFunc("DELETE /api/machines/{id}", s.machineLeaveAPI)
}

func (s *Server) machineEnrollAPI(w http.ResponseWriter, r *http.Request) {
	var in Machine
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil || in.Host == "" {
		http.Error(w, "a machine enrolls with {name, host, user, access}", http.StatusBadRequest)
		return
	}
	if !s.admitting() {
		http.Error(w, "this hub keeps no state: swimmers can join only a hub run with --state DIR (swim hub --state DIR)", http.StatusServiceUnavailable)
		return
	}
	in.Name, in.Host, in.User, in.Version = ui.Safe(in.Name), ui.Safe(in.Host), ui.Safe(in.User), ui.Safe(in.Version)
	if in.Access != "read" && in.Access != "readwrite" {
		in.Access = "read"
	}
	if in.Name == "" {
		in.Name = in.Host
	}
	id := MachineIDFor(in.Host, in.User)
	got := bearer(r.Header)
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.machines[id]
	own := m != nil && got != "" && tokenIs(hashToken(got), m.TokenHash)
	admin := s.isAdmin(got)
	switch {
	case m != nil && !own && !admin:
		http.Error(w, "this machine and user are enrolled with the hub already: log in again with its own token (~/.config/swim/auth.yml), or have the admin remove it first", http.StatusConflict)
		return
	case m == nil && !admin && s.pendingCount() >= MaxPending:
		http.Error(w, "too many are waiting to join this hub: try again once the admin has admitted or turned some away", http.StatusTooManyRequests)
		return
	}
	if m == nil {
		m = &Machine{ID: id, EnrolledAt: s.now().UTC().Format(time.RFC3339), From: s.clientAddr(r), Pending: !admin}
		s.machines[id] = m
		s.audit("%s (machine %s, %s@%s) enrolled from %s%s", id, in.Name, in.User, in.Host, m.From, map[bool]string{true: ": waiting to be admitted", false: ""}[m.Pending])
	}
	m.Name, m.Host, m.User, m.Access, m.Version = in.Name, in.Host, in.User, in.Access, in.Version
	token := got
	if !own {
		token = newToken()
		m.TokenHash = hashToken(token)
	}
	if err := s.save(); err != nil {
		http.Error(w, "hub state: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"id": id, "token": token, "pending": m.Pending})
}

// MachineStatus is what swim auth status hears.
type MachineStatus struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Access  string   `json:"access"`
	Pending bool     `json:"pending"`
	Workers []string `json:"workers"` // the directories that joined through it: "name (root)"
}

func (s *Server) machineMeAPI(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.machineByToken(bearer(r.Header))
	if m == nil {
		http.Error(w, "not a machine's token: swim auth login URL", http.StatusUnauthorized)
		return
	}
	st := MachineStatus{ID: m.ID, Name: m.Name, Access: m.Access, Pending: m.Pending, Workers: []string{}}
	for _, rec := range s.records {
		if rec.Machine == m.ID {
			st.Workers = append(st.Workers, rec.Name+" ("+rec.Root+")")
		}
	}
	sort.Strings(st.Workers)
	writeJSON(w, st)
}

// machinesAPI lists the machines, for the admin.
func (s *Server) machinesAPI(w http.ResponseWriter, r *http.Request) {
	if !s.adminOnly(w, r) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Machine{}
	for _, m := range s.machines {
		cp := *m
		cp.TokenHash = ""
		out = append(out, cp)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	writeJSON(w, map[string]any{"machines": out})
}

// machineLeaveAPI removes a machine (its own token: /me; the admin: by id)
// and every directory that joined through it.
func (s *Server) machineLeaveAPI(w http.ResponseWriter, r *http.Request) {
	got := bearer(r.Header)
	id := r.PathValue("id")
	var by string
	if id == "" {
		s.mu.Lock()
		m := s.machineByToken(got)
		s.mu.Unlock()
		if m == nil {
			http.Error(w, "not a machine's token", http.StatusUnauthorized)
			return
		}
		id, by = m.ID, "itself"
	} else {
		if !s.adminOnly(w, r) {
			return
		}
		by = s.adminName(r)
	}
	n, err := s.RemoveMachine(id, by)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"removed": id, "workers": n})
}

// RemoveMachine forgets machine id and the directories that joined
// through it (their grants and commands too), as by.
func (s *Server) RemoveMachine(id, by string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.machines[id]
	if m == nil {
		return 0, errors.New("no such machine")
	}
	n := 0
	for wid, rec := range s.records {
		if rec.Machine != id {
			continue
		}
		delete(s.workers, wid)
		delete(s.records, wid)
		delete(s.grants, wid)
		s.forgetCommands(wid)
		n++
	}
	delete(s.machines, id)
	s.audit("%s (machine %s, %s@%s) removed by %s with %d directories", id, m.Name, m.User, m.Host, by, n)
	return n, s.save()
}
