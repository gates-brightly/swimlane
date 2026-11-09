package cli

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/notify"
)

// With phone set, a batch's question can be answered from the operator's
// phone with one tap: its notification (notify_url, notify_command) carries
// two links, one that approves and one that declines. swim watch serves
// them over the tailnet (Tailscale). What keeps a tap the operator's own:
//
//   - Only the phone answers. swim watch listens on this machine's tailnet
//     address alone and takes a request only from the phone's, checked
//     before anything else. A process here (a lane included) sends from
//     this machine's address, and can't send from another without root, so
//     a link that leaks (ntfy's cache, a screenshot) answers nothing.
//   - Each link answers one question, once: a random 128-bit token each,
//     in swim watch's memory alone. They are never written to .swim/ or
//     .swim.log, which the repo's agents read. The first answer, at the
//     terminal or on the phone, ends both of a question's links.
//   - Each link expires: phone_expiry after the question was asked. After
//     that, the question waits for the terminal.

// phoneAnswers is swim watch's server for phone links.
type phoneAnswers struct {
	name   string     // the phone, as the phone setting names it
	phone  netip.Addr // the one address answers come from
	base   string     // http://<this machine's tailnet address>:<port>
	expiry time.Duration
	now    func() time.Time
	asks   *watchAsks
	warn   func(string) // hears of a request refused
	srv    *http.Server
}

// macTailscale is the Tailscale app's CLI on macOS, which isn't on PATH
// unless the operator put it there.
const macTailscale = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"

// tailnetIP is peer's tailnet address ("": this machine's): peer itself if
// it is an address, else what `tailscale ip -4 PEER` says. Tests replace
// it.
var tailnetIP = func(peer string) (netip.Addr, error) {
	if a, err := netip.ParseAddr(peer); err == nil {
		return a.Unmap(), nil
	}
	bin, err := exec.LookPath("tailscale")
	if err != nil {
		if _, serr := os.Stat(macTailscale); serr != nil {
			return netip.Addr{}, errors.New("tailscale isn't on PATH (nor at " + macTailscale + ")")
		}
		bin = macTailscale
	}
	args := []string{"ip", "-4"}
	if peer != "" {
		args = append(args, peer)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if err != nil {
		return netip.Addr{}, fmt.Errorf("tailscale %s: %v: %s", strings.Join(args, " "), err, notify.Truncate(line, 200))
	}
	a, err := netip.ParseAddr(strings.TrimSpace(line))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("tailscale %s said %q, not an address", strings.Join(args, " "), notify.Truncate(line, 200))
	}
	return a.Unmap(), nil
}

// Tailscale's address ranges: a tailnet address is in one of them.
var tailnetRanges = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fd7a:115c:a1e0::/48"),
}

func inTailnet(a netip.Addr) bool {
	for _, p := range tailnetRanges {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// startPhone starts answering asks' questions from the phone cfg names:
// it gives each question its links and serves them until stop.
func startPhone(cfg *config.Config, asks *watchAsks, warn func(string)) (*phoneAnswers, error) {
	self, err := tailnetIP("")
	if err != nil {
		return nil, fmt.Errorf("this machine's tailnet address: %v", err)
	}
	phone, err := tailnetIP(cfg.Phone)
	if err != nil {
		return nil, fmt.Errorf("phone %s: %v", cfg.Phone, err)
	}
	for _, a := range []netip.Addr{self, phone} {
		if !inTailnet(a) {
			return nil, fmt.Errorf("%s isn't a tailnet address (100.64.0.0/10)", a)
		}
	}
	ln, err := net.Listen("tcp", netip.AddrPortFrom(self, 0).String())
	if err != nil {
		return nil, err
	}
	p := &phoneAnswers{name: cfg.Phone, phone: phone, base: "http://" + ln.Addr().String(),
		expiry: cfg.PhoneExpiryFor(), now: time.Now, asks: asks, warn: warn}
	p.srv = &http.Server{Handler: p, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 10 * time.Second, MaxHeaderBytes: 8 << 10}
	go p.srv.Serve(ln)
	asks.phone = p
	return p, nil
}

// stop stops serving the links: none answers anything after.
func (p *phoneAnswers) stop() {
	p.srv.Close()
}

// issue gives q its links: a token that approves and one that declines,
// stamped with when q was asked and good until phone_expiry after.
func (p *phoneAnswers) issue(q *watchAsk) {
	if p == nil {
		return
	}
	q.approveTok, q.rejectTok = approval.NewID(), approval.NewID()
	q.expires = q.since.Add(p.expiry)
}

// links are q's two links ("" when q has none).
func (p *phoneAnswers) links(q watchAsk) (approve, reject string) {
	if p == nil || q.approveTok == "" {
		return "", ""
	}
	return p.base + "/approve/" + q.approveTok, p.base + "/reject/" + q.rejectTok
}

// ServeHTTP answers one tap: GET or POST /approve/TOKEN or /reject/TOKEN,
// from the phone.
func (p *phoneAnswers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	reply := func(code int, msg string) {
		w.WriteHeader(code)
		fmt.Fprintln(w, msg)
	}
	from, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil || from.Addr().Unmap() != p.phone {
		who := r.RemoteAddr
		if err == nil {
			who = from.Addr().Unmap().String()
		}
		p.warn(fmt.Sprintf("phone link refused: the request came from %s, not %s (%s)", who, p.name, p.phone))
		reply(http.StatusForbidden, "only "+p.name+" answers swim's questions")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		h.Set("Allow", "GET, POST")
		reply(http.StatusMethodNotAllowed, "GET or POST")
		return
	}
	verb, tok, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if !ok || tok == "" || verb != "approve" && verb != "reject" {
		reply(http.StatusNotFound, "not a swim link")
		return
	}
	reply(p.asks.answerLink(verb == "approve", tok, p.now(), "phone "+p.name))
}

// answerLink answers the question whose approve (or reject) link has tok,
// on via, if that link hasn't expired by now. It returns the HTTP status
// and what happened.
func (a *watchAsks) answerLink(approve bool, tok string, now time.Time, via string) (int, string) {
	a.mu.Lock()
	var q *watchAsk
	for _, p := range a.list {
		mine := p.rejectTok
		if approve {
			mine = p.approveTok
		}
		if mine != "" && subtle.ConstantTimeCompare([]byte(mine), []byte(tok)) == 1 {
			q = p
			break
		}
	}
	if q == nil {
		a.mu.Unlock()
		return http.StatusGone, "nothing waits on this link: its question was answered, withdrawn or its batch ended"
	}
	if !now.Before(q.expires) {
		a.mu.Unlock()
		return http.StatusGone, fmt.Sprintf("%s: this link expired at %s (phone_expiry); %s waits for an answer at swim watch",
			q.who(), q.expires.Format("15:04:05"), approval.Clean(q.msg.Flag))
	}
	return http.StatusOK, a.settle(q, askMsg{Approved: approve, By: operator(), Via: via})
}
