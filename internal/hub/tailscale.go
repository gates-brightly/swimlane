package hub

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/chars"
)

// Signing in with Tailscale (swim hub --trust-tailscale-identity): with the
// hub reachable only through tailscale serve on the same machine, the
// proxy names the tailnet user it served in Tailscale-User-Login. A hub
// user given that login (swim hub user add NAME --tailscale LOGIN) is
// signed in by it: loading the app or /login with the header, from a
// loopback peer, starts a session for them, as their token would.
//
// The header never authorizes anything itself: a request is a user's
// only through a session cookie (or their token), so every POST still
// carries the session's CSRF token. A header-borne identity is ambient,
// like a cookie without SameSite: if it authorized commands, any site a
// tailnet user visited could send them through the proxy.
//
// A session it starts may never answer a guard with a button (an
// unsigned answer rests on the hub's login alone, so it takes a token's);
// a signed answer is the signature's, not the login's, and is fine.
//
// The hub's admins can be named by login (swim hub --admin-tailscale
// LOGIN): the first sign-in by one makes them an admin user (named after
// the login, no token anyone holds) and a later one makes them admin again
// if their role was changed, so the hub's own command line, not its state,
// says who runs it. There is no "first visitor becomes admin".

// TailscaleHeader names the tailnet user tailscale serve proxied.
const TailscaleHeader = "Tailscale-User-Login"

// ViaTailscale marks a session started by the Tailscale identity header.
const ViaTailscale = "tailscale"

// TrustTailscaleIdentity turns signing in by Tailscale-User-Login on; the
// caller makes sure the hub listens on loopback only.
func (s *Server) TrustTailscaleIdentity(on bool) { s.trustTailscale = on }

// AdminTailscale names the Tailscale logins that sign in as admins.
func (s *Server) AdminTailscale(logins ...string) error {
	for _, l := range logins {
		if !validTailscaleLogin(l) {
			return fmt.Errorf("--admin-tailscale %q: one word of printable characters, like you@github or you@example.com", l)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.adminLogins = append([]string(nil), logins...)
	return nil
}

// adminLogin reports whether login is one AdminTailscale named. s.mu is held.
func (s *Server) adminLogin(login string) bool {
	for _, l := range s.adminLogins {
		if strings.EqualFold(l, login) {
			return true
		}
	}
	return false
}

// provisionAdmin is the admin user for login, made (or made admin again)
// when AdminTailscale names it: nil when it doesn't, or the hub keeps no
// state. s.mu is held.
func (s *Server) provisionAdmin(login string, u *User) *User {
	if !s.adminLogin(login) || s.dir == "" {
		return u
	}
	if u != nil {
		if u.Role != RoleAdmin {
			s.audit("user %s made admin again: %s is named by --admin-tailscale (was %s)", u.Name, login, u.Role)
			u.Role = RoleAdmin
			s.save()
		}
		return u
	}
	name := s.freeUserName(login)
	u = &User{Name: name, Role: RoleAdmin, TokenHash: hashToken(newToken()), Added: s.now().UTC().Format(time.RFC3339), Tailscale: login}
	s.users[name] = u
	s.audit("user %s added (admin, tailscale %s): its first sign-in, named by --admin-tailscale", name, login)
	s.save()
	return u
}

// freeUserName is a user name for login: its part before "@", in the
// characters a name may have, made unique. s.mu is held.
func (s *Server) freeUserName(login string) string {
	base, _, _ := strings.Cut(login, "@")
	base = strings.Trim(chars.Squash(base, func(r rune) bool {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-'
	}, "-"), ".-")
	if base == "" || !chars.Name(base, 60) {
		base = "admin"
	}
	name := base
	for i := 2; s.users[name] != nil; i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name
}

// validTailscaleLogin: a login name or an email, printable ASCII, no spaces.
func validTailscaleLogin(s string) bool {
	return len(s) >= 1 && len(s) <= 128 && chars.All(s, func(r rune) bool { return r > ' ' && r <= '~' })
}

// tailscaleLogin is r's Tailscale login, if the hub trusts the header and
// r came through a proxy on this machine ("" otherwise).
func (s *Server) tailscaleLogin(r *http.Request) string {
	if !s.trustTailscale {
		return ""
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !LoopbackHost(host) {
		return ""
	}
	login := strings.TrimSpace(r.Header.Get(TailscaleHeader))
	if !validTailscaleLogin(login) {
		return ""
	}
	return login
}

// signInTailscale starts a session for the user r's Tailscale login names,
// when r has none: true if it did. Only GETs that load a page call it.
func (s *Server) signInTailscale(w http.ResponseWriter, r *http.Request) bool {
	login := s.tailscaleLogin(r)
	if login == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if se, _ := s.sessionOf(r); se != nil {
		return false
	}
	var u *User
	for _, x := range s.users {
		if x.Tailscale != "" && strings.EqualFold(x.Tailscale, login) {
			u = x
		}
	}
	if u = s.provisionAdmin(login, u); u == nil {
		return false
	}
	sid, csrf := newToken(), newToken()
	s.sessions[hashToken(sid)] = &session{User: u.Name, CSRF: csrf, Expires: s.now().Add(SessionTTL), Via: ViaTailscale}
	s.audit("%s logged in (%s) by tailscale identity %s from %s", u.Name, u.Role, login, s.clientAddr(r))
	s.save()
	s.setSessionCookie(w, sid)
	// The request at hand reads as signed in too.
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: sid})
	return true
}

// viaTailscale reports whether r's session was started by the Tailscale
// identity header.
func (s *Server) viaTailscale(r *http.Request) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	se, _ := s.sessionOf(r)
	return se != nil && se.Via == ViaTailscale
}
