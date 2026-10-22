package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/chars"
)

// Hub users: a person behind every command. The admin adds them (swim hub
// user add NAME --role viewer|operator|admin) and each gets a token; the
// hub keeps only its hash, as for worker tokens. A browser trades the token
// for a session cookie (POST /api/login); scripts send it as a bearer
// token. The admin token manages users and sends no command itself; an
// admin user does both (adminOnly), from the CLI with their token or from
// the app with their session.

// Roles.
const (
	RoleViewer   = "viewer"   // reads full logs, sends signed answers
	RoleOperator = "operator" // also interrupts, drains, runs, and answers with a button
	RoleAdmin    = "admin"    // an operator who also does what the admin token does: users, approvals, grants
)

// validRole: one of the roles.
func validRole(r string) bool { return r == RoleViewer || r == RoleOperator || r == RoleAdmin }

// covers reports whether a user with role has want's rights: an admin is
// an operator too, and both are viewers.
func covers(role, want string) bool {
	switch want {
	case RoleViewer:
		return validRole(role)
	case RoleOperator:
		return role == RoleOperator || role == RoleAdmin
	}
	return role == want
}

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
	// Tailscale is the Tailscale login (Tailscale-User-Login) that signs
	// them in on a hub run with --trust-tailscale-identity (tailscale.go).
	Tailscale string `json:"tailscale,omitempty"`
}

// UserInfo is a user as listed: never the token's hash.
type UserInfo struct {
	Name      string `json:"name"`
	Role      string `json:"role"`
	Added     string `json:"added,omitempty"`
	Tailscale string `json:"tailscale,omitempty"`
}

// session is one login, kept in the state (keyed by its id's hash, so the
// file holds no id a browser could present) so a restart doesn't end it.
type session struct {
	User    string    `json:"user"`
	CSRF    string    `json:"csrf"` // a form POST with this session must carry it
	Expires time.Time `json:"expires"`
	Via     string    `json:"via,omitempty"` // "tailscale": signed in by its identity header, not a token
}

// A user name is what lookup.yml names people by, and lands in
// worker logs, so it is plain.

// SecureCookies marks session cookies Secure: the hub serves TLS, or a
// proxy serves it over https.
func (s *Server) SecureCookies(on bool) { s.secure = on }

// Private makes viewing need a login: the pages and the read API take a
// user's session or token (the admin token too, for the list).
func (s *Server) Private(on bool) { s.private = on }

// BehindProxy says the hub is served through a proxy on this host
// (swim hub --public-url): X-Forwarded-For from a loopback peer then
// names the client in the audit log. From any other peer it's ignored.
func (s *Server) BehindProxy(on bool) { s.proxied = on }

// canView reports whether r may see the pages and the workers list.
func (s *Server) canView(r *http.Request) bool {
	if !s.private {
		return true
	}
	got := bearer(r.Header)
	if got != "" && s.isAdmin(got) {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if got != "" {
		return s.userByToken(got) != nil
	}
	se, _ := s.sessionOf(r)
	return se != nil
}

// clientAddr is who sent r, for the audit log: the peer, or, behind a
// proxy on this host, the client it forwarded. The proxy appends the
// address it saw to X-Forwarded-For, so the last one is the one to trust;
// anything before it came from the client.
func (s *Server) clientAddr(r *http.Request) string {
	if !s.proxied {
		return r.RemoteAddr
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err != nil || !LoopbackHost(host) {
		return r.RemoteAddr
	}
	xff := r.Header.Values("X-Forwarded-For")
	if len(xff) == 0 {
		return r.RemoteAddr
	}
	parts := strings.Split(xff[len(xff)-1], ",")
	ip := strings.TrimSpace(parts[len(parts)-1])
	if net.ParseIP(ip) == nil {
		return r.RemoteAddr
	}
	return ip
}

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
	return s.AddUserTailscale(name, role, "")
}

// AddUserTailscale is AddUser, with the Tailscale login that signs them
// in ("" for none).
func (s *Server) AddUserTailscale(name, role, tailscale string) (string, error) {
	if tailscale != "" && !validTailscaleLogin(tailscale) {
		return "", fmt.Errorf("tailscale login %q: one word of printable characters, like you@github or you@example.com", tailscale)
	}
	if !chars.Name(name, 64) {
		return "", fmt.Errorf("user name %q: letters, digits, '.', '_' and '-' only (the name lookup.yml gives them)", name)
	}
	if !validRole(role) {
		return "", fmt.Errorf("role %q: viewer, operator or admin", role)
	}
	if s.dir == "" {
		return "", errors.New("this hub keeps no state (swim hub --state DIR): it can't keep users")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.users {
		if tailscale != "" && u.Name != name && strings.EqualFold(u.Tailscale, tailscale) {
			return "", fmt.Errorf("tailscale login %s is %s's already", tailscale, u.Name)
		}
	}
	token := newToken()
	verb := "added"
	if _, ok := s.users[name]; ok {
		verb = "replaced"
	}
	s.users[name] = &User{Name: name, Role: role, TokenHash: hashToken(token), Added: s.now().UTC().Format(time.RFC3339), Tailscale: tailscale}
	s.endSessions(name)
	if err := s.save(); err != nil {
		return "", err
	}
	if tailscale != "" {
		s.audit("user %s %s (%s, tailscale %s)", name, verb, role, tailscale)
	} else {
		s.audit("user %s %s (%s)", name, verb, role)
	}
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
		out = append(out, UserInfo{Name: u.Name, Role: u.Role, Added: u.Added, Tailscale: u.Tailscale})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// endSessions logs a user out everywhere. s.mu is held.
func (s *Server) endSessions(name string) {
	for id, se := range s.sessions {
		if se.User == name {
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
		token, err := s.AddUserTailscale(in.Name, in.Role, in.Tailscale)
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
	key := hashToken(c.Value)
	se := s.sessions[key]
	if se == nil {
		return nil, nil
	}
	if s.now().After(se.Expires) {
		delete(s.sessions, key)
		return nil, nil
	}
	u := s.users[se.User]
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
			if !tokenIs(sent, se.CSRF) {
				http.Error(w, "missing or stale CSRF token: reload the page", http.StatusForbidden)
				return nil, false
			}
		}
	}
	if !covers(u.Role, role) {
		http.Error(w, u.Name+" is a "+u.Role+": this needs the "+role+" role", http.StatusForbidden)
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
		s.audit("failed login from %s", s.clientAddr(r))
		s.mu.Unlock()
		if form {
			http.Redirect(w, r, "/login?failed=1", http.StatusSeeOther)
			return
		}
		http.Error(w, "bad or missing token", http.StatusUnauthorized)
		return
	}
	sid, csrf := newToken(), newToken()
	s.sessions[hashToken(sid)] = &session{User: u.Name, CSRF: csrf, Expires: s.now().Add(SessionTTL)}
	s.audit("%s logged in (%s) from %s", u.Name, u.Role, s.clientAddr(r))
	s.save() // a restart keeps the login (save drops expired ones)
	name, role := u.Name, u.Role
	s.mu.Unlock()
	s.setSessionCookie(w, sid)
	if form {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"user": name, "role": role, "csrf": csrf})
}

// setSessionCookie gives the browser its session.
func (s *Server) setSessionCookie(w http.ResponseWriter, sid string) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: sid, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: s.secure, MaxAge: int(SessionTTL.Seconds())})
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
		delete(s.sessions, hashToken(c.Value))
		s.audit("%s logged out", u.Name)
		s.save()
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
	return u.Name, u.Role, se.CSRF
}
