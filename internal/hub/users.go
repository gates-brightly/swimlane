package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Hub users: a person behind every command. The admin adds them (swim hub
// user add NAME --role viewer|operator) and each gets a token; the hub
// keeps only its hash, as for worker tokens. A browser trades the token
// for a session cookie (POST /api/login); scripts send it as a bearer
// token. The admin token manages users and sends no command itself.

// Roles.
const (
	RoleViewer   = "viewer"   // reads full logs
	RoleOperator = "operator" // also interrupts, drains and runs
)

// SessionTTL is how long a login lasts.
const SessionTTL = 12 * time.Hour

// sessionCookie names the session cookie.
const sessionCookie = "swim_session"

// User is one hub user, as the state keeps them.
type User struct {
	Name      string `json:"name"`
	Role      string `json:"role"`
	TokenHash string `json:"token_hash"`
	Added     string `json:"added,omitempty"`
}

// UserInfo is a user as listed: never the token's hash.
type UserInfo struct {
	Name  string `json:"name"`
	Role  string `json:"role"`
	Added string `json:"added,omitempty"`
}

type session struct {
	user    string
	csrf    string // a form POST with this session must carry it
	expires time.Time
}

// userNameRE: a user name is what lookup.yml names people by, and lands in
// worker logs, so it is plain.
var userNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// SecureCookies marks session cookies Secure: the hub serves TLS.
func (s *Server) SecureCookies(on bool) { s.secure = on }

func (s *Server) userRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/users", s.usersAPI)
	mux.HandleFunc("GET /api/users", s.usersAPI)
	mux.HandleFunc("DELETE /api/users/{name}", s.usersAPI)
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /logout", s.logout)
}

// AddUser adds a user, or replaces one (a new token and role, its sessions
// ended), and returns the token: shown once, never kept.
func (s *Server) AddUser(name, role string) (string, error) {
	if !userNameRE.MatchString(name) {
		return "", fmt.Errorf("user name %q: letters, digits, '.', '_' and '-' only (the name lookup.yml gives them)", name)
	}
	if role != RoleViewer && role != RoleOperator {
		return "", fmt.Errorf("role %q: viewer or operator", role)
	}
	if s.dir == "" {
		return "", errors.New("this hub keeps no state (swim hub --state DIR): it can't keep users")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	token := newToken()
	verb := "added"
	if _, ok := s.users[name]; ok {
		verb = "replaced"
	}
	s.users[name] = &User{Name: name, Role: role, TokenHash: hashToken(token), Added: s.now().UTC().Format(time.RFC3339)}
	s.endSessions(name)
	if err := s.save(); err != nil {
		return "", err
	}
	s.audit("user %s %s (%s)", name, verb, role)
	return token, nil
}

// RemoveUser removes a user and ends their sessions.
func (s *Server) RemoveUser(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[name]; !ok {
		return fmt.Errorf("no user %q", name)
	}
	delete(s.users, name)
	s.endSessions(name)
	if err := s.save(); err != nil {
		return err
	}
	s.audit("user %s removed", name)
	return nil
}

// Users lists the users, by name.
func (s *Server) Users() []UserInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []UserInfo{}
	for _, u := range s.users {
		out = append(out, UserInfo{Name: u.Name, Role: u.Role, Added: u.Added})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// endSessions logs a user out everywhere. s.mu is held.
func (s *Server) endSessions(name string) {
	for id, se := range s.sessions {
		if se.user == name {
			delete(s.sessions, id)
		}
	}
}

func (s *Server) usersAPI(w http.ResponseWriter, r *http.Request) {
	if !s.adminOnly(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		json.NewEncoder(w).Encode(map[string]any{"users": s.Users()})
	case http.MethodDelete:
		if err := s.RemoveUser(r.PathValue("name")); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		var in UserInfo
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
			http.Error(w, "a user needs {name, role}", http.StatusBadRequest)
			return
		}
		token, err := s.AddUser(in.Name, in.Role)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"name": in.Name, "role": in.Role, "token": token})
	}
}

// userByToken is the user whose token got is. s.mu is held.
func (s *Server) userByToken(got string) *User {
	if got == "" {
		return nil
	}
	h := hashToken(got)
	for _, u := range s.users {
		if tokenIs(h, u.TokenHash) {
			return u
		}
	}
	return nil
}

// sessionOf is r's live session and its user (nil, nil without one).
// s.mu is held.
func (s *Server) sessionOf(r *http.Request) (*session, *User) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil, nil
	}
	se := s.sessions[c.Value]
	if se == nil {
		return nil, nil
	}
	if s.now().After(se.expires) {
		delete(s.sessions, c.Value)
		return nil, nil
	}
	u := s.users[se.user]
	if u == nil {
		return nil, nil
	}
	return se, u
}

// isForm: r's body is an HTML form.
func isForm(r *http.Request) bool {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return mt == "application/x-www-form-urlencoded" || mt == "multipart/form-data"
}

// requireUser is who sent r, if they may act as role: a user's bearer
// token, or a session cookie (whose POSTs must carry the session's CSRF
// token, as the csrf form field or X-CSRF-Token). It writes the refusal
// otherwise. A form must be parsed (r.ParseForm) before.
func (s *Server) requireUser(w http.ResponseWriter, r *http.Request, role string) (*User, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	got := bearer(r.Header)
	var u *User
	switch {
	case got != "" && s.isAdmin(got):
		http.Error(w, "the admin token manages users only: send commands as a user (swim hub user add NAME --role operator)", http.StatusForbidden)
		return nil, false
	case got != "":
		if u = s.userByToken(got); u == nil {
			http.Error(w, "bad or missing token", http.StatusUnauthorized)
			return nil, false
		}
	default:
		var se *session
		if se, u = s.sessionOf(r); se == nil {
			http.Error(w, "log in first: /login", http.StatusUnauthorized)
			return nil, false
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			sent := r.Header.Get("X-CSRF-Token")
			if sent == "" {
				sent = r.PostFormValue("csrf")
			}
			if !tokenIs(sent, se.csrf) {
				http.Error(w, "missing or stale CSRF token: reload the page", http.StatusForbidden)
				return nil, false
			}
		}
	}
	if role == RoleOperator && u.Role != RoleOperator {
		http.Error(w, u.Name+" is a "+u.Role+": this needs the operator role", http.StatusForbidden)
		return nil, false
	}
	return u, true
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	form := isForm(r)
	var tok string
	if form {
		tok = strings.TrimSpace(r.PostFormValue("token"))
	} else {
		var in struct {
			Token string `json:"token"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		tok = strings.TrimSpace(in.Token)
	}
	s.mu.Lock()
	u := s.userByToken(tok)
	if u == nil {
		s.audit("failed login from %s", r.RemoteAddr)
		s.mu.Unlock()
		if form {
			http.Redirect(w, r, "/login?failed=1", http.StatusSeeOther)
			return
		}
		http.Error(w, "bad or missing token", http.StatusUnauthorized)
		return
	}
	now := s.now()
	for id, se := range s.sessions {
		if now.After(se.expires) {
			delete(s.sessions, id)
		}
	}
	sid, csrf := newToken(), newToken()
	s.sessions[sid] = &session{user: u.Name, csrf: csrf, expires: now.Add(SessionTTL)}
	s.audit("%s logged in (%s) from %s", u.Name, u.Role, r.RemoteAddr)
	name, role := u.Name, u.Role
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: sid, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: s.secure, MaxAge: int(SessionTTL.Seconds())})
	if form {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"user": name, "role": role, "csrf": csrf})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	r.ParseForm()
	u, ok := s.requireUser(w, r, RoleViewer)
	if !ok {
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.audit("%s logged out", u.Name)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.secure, MaxAge: -1})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// viewer is the user logged in on r's session and its CSRF token, for the
// pages ("" when nobody is).
func (s *Server) viewer(r *http.Request) (name, role, csrf string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	se, u := s.sessionOf(r)
	if se == nil {
		return "", "", ""
	}
	return u.Name, u.Role, se.csrf
}
