package hub

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"time"
)

// Admission: any swim may enroll with a hub, with or without the enroll
// token, but it joins only when the hub's admin admits it. Until then it
// waits: it shows on the admin's list of workers waiting to join (its name,
// host, directory, namespace and key's fingerprint), and nothing else. Its
// reports, runs, logs, checkpoints and grants are refused and it gets no
// commands, so a stranger can't put anything on the hub's pages or reach a
// worker through it. At most MaxPending wait at once.
//
// A hub without state (New: nowhere to keep admissions) works as before
// admission: the enroll token enrolls and admits.

// MaxPending is how many workers may wait to join at once.
const MaxPending = 50

// PendingWorker is a worker waiting to join, as the admin lists it.
type PendingWorker struct {
	ID         string `json:"id"`
	Kind       string `json:"kind,omitempty"` // "machine" for a machine-wide enrollment (swim auth); "" for a directory
	User       string `json:"user,omitempty"` // a machine's user
	Name       string `json:"name"`
	Host       string `json:"host"`
	Root       string `json:"root"`
	Namespace  string `json:"namespace,omitempty"`
	Access     string `json:"access,omitempty"` // what its enrollment lets the hub do: read or readwrite
	Version    string `json:"version,omitempty"`
	KeyFP      string `json:"key_fp,omitempty"`
	EnrolledAt string `json:"enrolled_at,omitempty"`
	From       string `json:"from,omitempty"` // the address it enrolled from
}

// errPending answers a waiting worker's every request.
const errPending = "waiting for the hub's admin to admit this worker (the hub's Admin page, or swim hub admit NAME)"

// admitting reports whether new workers wait to be admitted: the hub keeps
// state to keep the admissions in.
func (s *Server) admitting() bool { return s.dir != "" }

// isEnrolled reports whether got is worker id's own token, admitted or
// not. s.mu is held.
func (s *Server) isEnrolled(id, got string) bool {
	r := s.records[id]
	return r != nil && r.TokenHash != "" && tokenIs(hashToken(got), r.TokenHash)
}

// pendingCount is how many workers wait. s.mu is held.
func (s *Server) pendingCount() int {
	n := 0
	for _, r := range s.records {
		if r.Pending {
			n++
		}
	}
	for _, m := range s.machines {
		if m.Pending {
			n++
		}
	}
	return n
}

// Pending lists the workers waiting to join, oldest first.
func (s *Server) Pending() []PendingWorker {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []PendingWorker{}
	for id, r := range s.records {
		if r.Pending {
			out = append(out, PendingWorker{ID: id, Name: r.Name, Host: r.Host, Root: r.Root, Namespace: r.Namespace, Access: r.Access,
				Version: r.Version, KeyFP: r.KeyFP, EnrolledAt: r.EnrolledAt, From: r.From})
		}
	}
	for id, m := range s.machines {
		if m.Pending {
			out = append(out, PendingWorker{ID: id, Kind: "machine", Name: m.Name, Host: m.Host, User: m.User, Root: "every directory of " + m.User,
				Access: m.Access, Version: m.Version, EnrolledAt: m.EnrolledAt, From: m.From})
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].EnrolledAt != out[b].EnrolledAt {
			return out[a].EnrolledAt < out[b].EnrolledAt
		}
		return out[a].ID < out[b].ID
	})
	return out
}

// Admit lets waiting worker id join, as by.
func (s *Server) Admit(id, by string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.machines[id]; m != nil {
		if !m.Pending {
			return m.Name + " is already admitted", nil
		}
		m.Pending, m.AdmittedAt, m.AdmittedBy = false, s.now().UTC().Format(time.RFC3339), by
		if err := s.save(); err != nil {
			return "", err
		}
		s.audit("%s (machine %s, %s@%s) admitted by %s", id, m.Name, m.User, m.Host, by)
		return "machine " + m.Name + " admitted: every directory of " + m.User + " there joins as it runs swim watch", nil
	}
	r := s.records[id]
	if r == nil {
		return "", errors.New("no such worker")
	}
	if !r.Pending {
		return r.Name + " is already admitted", nil
	}
	r.Pending, r.AdmittedAt, r.AdmittedBy = false, s.now().UTC().Format(time.RFC3339), by
	if err := s.save(); err != nil {
		return "", err
	}
	if _, ok := s.workers[id]; !ok {
		s.workers[id] = &View{Snapshot: Snapshot{Worker: r.Worker, Activity: "admitted; waiting for its next report", At: s.now().UTC().Format(time.RFC3339),
			Lanes: []Lane{}, Queue: []Batch{}, Events: []string{}}, Seen: s.now()}
	}
	s.audit("%s (%s on %s:%s) admitted by %s", id, r.Name, r.Host, r.Root, by)
	return r.Name + " admitted: it shows on the hub at its next report", nil
}

// Reject turns waiting worker id away: its enrollment is forgotten (it may
// enroll again, and wait again).
func (s *Server) Reject(id, by string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.machines[id]; m != nil && m.Pending {
		delete(s.machines, id)
		if err := s.save(); err != nil {
			return "", err
		}
		s.audit("%s (machine %s, %s@%s) turned away by %s", id, m.Name, m.User, m.Host, by)
		return "machine " + m.Name + " turned away", nil
	}
	r := s.records[id]
	if r == nil || !r.Pending {
		return "", errors.New("no worker waiting with that id")
	}
	delete(s.records, id)
	if err := s.save(); err != nil {
		return "", err
	}
	s.audit("%s (%s on %s:%s) turned away by %s", id, r.Name, r.Host, r.Root, by)
	return r.Name + " turned away", nil
}

func (s *Server) admissionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/pending", s.pendingAPI)
	mux.HandleFunc("POST /api/workers/{id}/admit", s.admitAPI)
	mux.HandleFunc("DELETE /api/workers/{id}/admit", s.admitAPI)
}

func (s *Server) pendingAPI(w http.ResponseWriter, r *http.Request) {
	if !s.adminOnly(w, r) {
		return
	}
	writeJSON(w, map[string]any{"pending": s.Pending()})
}

func (s *Server) admitAPI(w http.ResponseWriter, r *http.Request) {
	if !s.adminOnly(w, r) {
		return
	}
	by := s.adminName(r)
	var msg string
	var err error
	if r.Method == http.MethodDelete {
		msg, err = s.Reject(r.PathValue("id"), by)
	} else {
		msg, err = s.Admit(r.PathValue("id"), by)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"message": msg})
}

// adminName is who an admin request came from, for the audit log: the
// admin user, or "the admin token".
func (s *Server) adminName(r *http.Request) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if got := bearer(r.Header); got != "" {
		if s.isAdmin(got) {
			return "the admin token"
		}
		if u := s.userByToken(got); u != nil {
			return u.Name
		}
	}
	if _, u := s.sessionOf(r); u != nil {
		return u.Name
	}
	return "an admin"
}
