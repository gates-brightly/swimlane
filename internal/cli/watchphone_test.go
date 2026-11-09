package cli

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/approval"
)

// phoneForTest is a's phone answers, as startPhone sets them up, from the
// phone at 100.64.0.2, with links that work for a minute after now().
func phoneForTest(a *watchAsks, now *time.Time, warned *[]string) *phoneAnswers {
	p := &phoneAnswers{name: "zphone", phone: netip.MustParseAddr("100.64.0.2"), base: "http://100.64.0.1:4242",
		expiry: time.Minute, now: func() time.Time { return *now }, asks: a,
		warn: func(s string) { *warned = append(*warned, s) }}
	a.phone = p
	return p
}

// tap requests url's path from addr, as the phone (or anything else on
// the tailnet) would.
func tap(p *phoneAnswers, method, url, addr string) (int, string) {
	r := httptest.NewRequest(method, url, nil)
	r.RemoteAddr = addr
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

func TestPhoneLinkApproves(t *testing.T) {
	c, a, stop := askPairForTest(t, 7)
	defer stop()
	now := time.Now()
	var warned []string
	p := phoneForTest(a, &now, &warned)
	got := make(chan approval.Response, 1)
	go func() { got <- c.ask(approval.Request{Lane: 3, Flag: "DEPLOY", Desc: "deploy 2.4.1"}, nil) }()
	q := waitPending(t, a, 1)[0]
	approve, reject := p.links(q)
	if !strings.HasPrefix(approve, "http://100.64.0.1:4242/approve/") || !strings.HasPrefix(reject, "http://100.64.0.1:4242/reject/") {
		t.Fatalf("links: %q %q", approve, reject)
	}
	if len(q.approveTok) != 32 || q.approveTok == q.rejectTok || !q.expires.Equal(q.since.Add(time.Minute)) {
		t.Fatalf("tokens %q %q, expires %v (asked %v)", q.approveTok, q.rejectTok, q.expires, q.since)
	}

	// Anything but the phone is refused before its token is looked at,
	// and the question still waits: this machine (a lane), another device.
	for _, from := range []string{"100.64.0.1:5000", "127.0.0.1:5000", "100.64.0.9:5000"} {
		if code, body := tap(p, "GET", approve, from); code != http.StatusForbidden || !strings.Contains(body, "only zphone") {
			t.Errorf("from %s: %d %q", from, code, body)
		}
	}
	if len(warned) != 3 || !strings.Contains(warned[0], "came from 100.64.0.1, not zphone") {
		t.Errorf("warnings: %q", warned)
	}
	for _, w := range warned {
		if strings.Contains(w, q.approveTok) {
			t.Errorf("a warning holds the token: %q", w)
		}
	}
	// A token answers only as what it is: the decline token can't approve.
	if code, _ := tap(p, "GET", strings.Replace(reject, "/reject/", "/approve/", 1), "100.64.0.2:5000"); code != http.StatusGone {
		t.Errorf("the decline token at /approve: %d", code)
	}
	if code, _ := tap(p, "PUT", approve, "100.64.0.2:5000"); code != http.StatusMethodNotAllowed {
		t.Errorf("PUT: %d", code)
	}
	if code, _ := tap(p, "GET", "http://100.64.0.1:4242/approve/", "100.64.0.2:5000"); code != http.StatusNotFound {
		t.Errorf("no token: %d", code)
	}
	if p, _ := a.pending(); len(p) != 1 {
		t.Fatalf("the question went: %d waiting", len(p))
	}

	// The phone's tap answers it, once.
	if code, body := tap(p, "GET", approve, "100.64.0.2:5000"); code != http.StatusOK || !strings.Contains(body, "lane 3 (#7): DEPLOY approved") {
		t.Errorf("the phone's tap: %d %q", code, body)
	}
	r := <-got
	if !r.Approved || r.By == "" || r.Via != "phone zphone" {
		t.Errorf("response = %+v", r)
	}
	waitPending(t, a, 0)
	for _, u := range []string{approve, reject} {
		if code, body := tap(p, "POST", u, "100.64.0.2:5000"); code != http.StatusGone || !strings.Contains(body, "nothing waits") {
			t.Errorf("again: %d %q", code, body)
		}
	}
}

func TestPhoneLinkDeclines(t *testing.T) {
	c, a, stop := askPairForTest(t, 2)
	defer stop()
	now := time.Now()
	var warned []string
	p := phoneForTest(a, &now, &warned)
	got := make(chan approval.Response, 1)
	go func() { got <- c.ask(approval.Request{Lane: 1, Flag: "DROP", Desc: "drop it"}, nil) }()
	_, reject := p.links(waitPending(t, a, 1)[0])
	if code, body := tap(p, "POST", reject, "[::ffff:100.64.0.2]:5000"); code != http.StatusOK || !strings.Contains(body, "DROP declined") {
		t.Errorf("decline: %d %q", code, body)
	}
	if r := <-got; r.Approved || r.Via != "phone zphone" {
		t.Errorf("response = %+v", r)
	}
}

// A link stops working phone_expiry after its question was asked; the
// question still waits for the terminal.
func TestPhoneLinkExpires(t *testing.T) {
	c, a, stop := askPairForTest(t, 4)
	defer stop()
	now := time.Now()
	var warned []string
	p := phoneForTest(a, &now, &warned)
	got := make(chan approval.Response, 1)
	go func() { got <- c.ask(approval.Request{Lane: 2, Flag: "F", Desc: "d"}, nil) }()
	q := waitPending(t, a, 1)[0]
	approve, _ := p.links(q)
	now = q.expires
	if code, body := tap(p, "GET", approve, "100.64.0.2:5000"); code != http.StatusGone || !strings.Contains(body, "expired at "+q.expires.Format("15:04:05")) {
		t.Errorf("expired: %d %q", code, body)
	}
	if p, _ := a.pending(); len(p) != 1 {
		t.Fatalf("an expired link took the question: %d waiting", len(p))
	}
	a.answer(q.key, 'n')
	if r := <-got; r.Approved || r.Via != "" {
		t.Errorf("the terminal's answer: %+v", r)
	}
}

// The terminal's answer ends the question's links.
func TestPhoneLinkAfterTerminal(t *testing.T) {
	c, a, stop := askPairForTest(t, 5)
	defer stop()
	now := time.Now()
	var warned []string
	p := phoneForTest(a, &now, &warned)
	got := make(chan approval.Response, 1)
	go func() { got <- c.ask(approval.Request{Lane: 2, Flag: "F", Desc: "d"}, nil) }()
	q := waitPending(t, a, 1)[0]
	a.answer(q.key, 'y')
	<-got
	approve, _ := p.links(q)
	if code, _ := tap(p, "GET", approve, "100.64.0.2:5000"); code != http.StatusGone {
		t.Errorf("after the terminal answered: %d", code)
	}
}

// Without phone, questions have no links.
func TestPhoneOffNoLinks(t *testing.T) {
	c, a, stop := askPairForTest(t, 1)
	defer stop()
	go c.ask(approval.Request{Lane: 1, Flag: "F", Desc: "d"}, nil)
	q := waitPending(t, a, 1)[0]
	if ap, rj := a.phone.links(q); ap != "" || rj != "" || q.approveTok != "" {
		t.Errorf("links without phone: %q %q", ap, rj)
	}
	a.answer(q.key, 'n')
}

func TestInTailnet(t *testing.T) {
	for addr, want := range map[string]bool{"100.64.0.1": true, "100.127.255.254": true, "100.128.0.1": false,
		"192.168.1.20": false, "127.0.0.1": false, "fd7a:115c:a1e0::1": true} {
		if got := inTailnet(netip.MustParseAddr(addr)); got != want {
			t.Errorf("inTailnet(%s) = %v", addr, got)
		}
	}
}

// A lane's dry-run line says a decline came from the phone.
func TestDeclinedOnPhone(t *testing.T) {
	if got := declined(approval.Response{By: "zach", Via: "phone zphone"}); got != "declined by zach on phone zphone" {
		t.Errorf("declined = %q", got)
	}
}
