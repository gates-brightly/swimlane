package hub

import (
	"net"
	"net/http"
	"strings"

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

// TailscaleHeader names the tailnet user tailscale serve proxied.
const TailscaleHeader = "Tailscale-User-Login"

// ViaTailscale marks a session started by the Tailscale identity header.
const ViaTailscale = "tailscale"

// TrustTailscaleIdentity turns signing in by Tailscale-User-Login on; the
// caller makes sure the hub listens on loopback only.
func (s *Server) TrustTailscaleIdentity(on bool) { s.trustTailscale = on }

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
	if u == nil {
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
