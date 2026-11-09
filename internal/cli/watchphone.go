package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"sync"
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
	asks   *watchAsks
	warn   func(string) // hears of a request refused
	srv    *http.Server

	mu    sync.Mutex
	links map[string]phoneLink // by token
}

// phoneLink is what one link answers: the question with key, approving or
// declining, until expires. pair is the question's other link's token.
type phoneLink struct {
	key, who, flag string
	approve        bool
	expires        time.Time
	pair           string
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

// tailnet is Tailscale's IPv4 range: tailnetIP asks for IPv4 alone.
var tailnet = netip.MustParsePrefix("100.64.0.0/10")

// startPhone starts answering asks' questions from the phone cfg names:
// it serves the links issue gives them until stop.
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
		if !tailnet.Contains(a) {
			return nil, fmt.Errorf("%s isn't a tailnet address (100.64.0.0/10)", a)
		}
	}
	ln, err := net.Listen("tcp", netip.AddrPortFrom(self, 0).String())
	if err != nil {
		return nil, err
	}
	p := &phoneAnswers{name: cfg.Phone, phone: phone, base: "http://" + ln.Addr().String(),
		expiry: cfg.PhoneExpiryFor(), asks: asks, warn: warn, links: map[string]phoneLink{}}
	p.srv = &http.Server{Handler: p, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 10 * time.Second, MaxHeaderBytes: 8 << 10}
	go p.srv.Serve(ln)
	return p, nil
}

// stop stops serving the links: none answers anything after.
func (p *phoneAnswers) stop() {
	p.srv.Close()
}

// issue gives q its links, one that approves and one that declines, and
// when they stop working: phone_expiry after q was asked. They are all ""
// when phone answers are off. Links whose question no longer waits go.
func (p *phoneAnswers) issue(q watchAsk) (approve, reject string, expires time.Time) {
	if p == nil {
		return "", "", time.Time{}
	}
	ap, rj := approval.NewID(), approval.NewID()
	l := phoneLink{key: q.key, who: q.who(), flag: approval.Clean(q.msg.Flag), expires: q.since.Add(p.expiry)}
	p.mu.Lock()
	defer p.mu.Unlock()
	for tok, old := range p.links {
		if _, waits := p.asks.lane(old.key); !waits {
			delete(p.links, tok)
		}
	}
	l.approve, l.pair = true, rj
	p.links[ap] = l
	l.approve, l.pair = false, ap
	p.links[rj] = l
	return p.base + "/approve/" + ap, p.base + "/reject/" + rj, l.expires
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
	reply(p.answer(verb == "approve", tok, time.Now()))
}

// answer answers the question tok's link is for, if that link approves
// (or declines) as asked, its question waits still and it hasn't expired
// by now. It returns the HTTP status and what happened.
func (p *phoneAnswers) answer(approve bool, tok string, now time.Time) (int, string) {
	const gone = "nothing waits on this link: its question was answered, withdrawn or its batch ended"
	p.mu.Lock()
	l, ok := p.links[tok]
	if ok && l.approve != approve {
		ok = false
	}
	expired := ok && !now.Before(l.expires)
	if ok && !expired {
		delete(p.links, tok)
		delete(p.links, l.pair)
	}
	p.mu.Unlock()
	switch {
	case !ok:
		return http.StatusGone, gone
	case expired:
		if _, waits := p.asks.lane(l.key); !waits {
			return http.StatusGone, gone
		}
		return http.StatusGone, fmt.Sprintf("%s: this link expired at %s (phone_expiry); %s waits for an answer at swim watch",
			l.who, l.expires.Format("15:04:05"), l.flag)
	}
	msg, waits := p.asks.answerKey(l.key, approve, "phone "+p.name)
	if !waits {
		return http.StatusGone, gone
	}
	return http.StatusOK, msg
}
