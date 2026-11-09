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
// phone at 100.64.0.2, with links that work for a minute.
func phoneForTest(a *watchAsks) *phoneAnswers {
	return &phoneAnswers{name: "zphone", phone: netip.MustParseAddr("100.64.0.2"), base: "http://100.64.0.1:4242",
		expiry: time.Minute, asks: a, warn: func(string) {}, links: map[string]phoneLink{}}
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
	p := phoneForTest(a)
	var warned []string
	p.warn = func(s string) { warned = append(warned, s) }
	got := make(chan approval.Response, 1)
	go func() { got <- c.ask(approval.Request{Lane: 3, Flag: "DEPLOY", Desc: "deploy 2.4.1"}, nil) }()
	q := waitPending(t, a, 1)[0]
	approve, reject, expires := p.issue(q)
	apTok, rjTok := strings.TrimPrefix(approve, "http://100.64.0.1:4242/approve/"), strings.TrimPrefix(reject, "http://100.64.0.1:4242/reject/")
	if len(apTok) != 32 || len(rjTok) != 32 || apTok == rjTok || !expires.Equal(q.since.Add(time.Minute)) {
		t.Fatalf("links %q %q, expires %v (asked %v)", approve, reject, expires, q.since)
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
		if strings.Contains(w, apTok) {
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
	p := phoneForTest(a)
	got := make(chan approval.Response, 1)
	go func() { got <- c.ask(approval.Request{Lane: 1, Flag: "DROP", Desc: "drop it"}, nil) }()
	_, reject, _ := p.issue(waitPending(t, a, 1)[0])
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
	p := phoneForTest(a)
	p.expiry = -time.Second
	got := make(chan approval.Response, 1)
	go func() { got <- c.ask(approval.Request{Lane: 2, Flag: "F", Desc: "d"}, nil) }()
	q := waitPending(t, a, 1)[0]
	approve, _, expires := p.issue(q)
	if code, body := tap(p, "GET", approve, "100.64.0.2:5000"); code != http.StatusGone || !strings.Contains(body, "expired at "+expires.Format("15:04:05")) {
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
	p := phoneForTest(a)
	got := make(chan approval.Response, 1)
	go func() { got <- c.ask(approval.Request{Lane: 2, Flag: "F", Desc: "d"}, nil) }()
	q := waitPending(t, a, 1)[0]
	approve, _, _ := p.issue(q)
	a.answer(q.key, 'y')
	<-got
	if code, _ := tap(p, "GET", approve, "100.64.0.2:5000"); code != http.StatusGone {
		t.Errorf("after the terminal answered: %d", code)
	}
	// The next question's links clear the answered one's.
	go c.ask(approval.Request{Lane: 2, Flag: "G", Desc: "d"}, nil)
	q = waitPending(t, a, 1)[0]
	p.issue(q)
	if len(p.links) != 2 {
		t.Errorf("%d links kept, want the waiting question's 2", len(p.links))
	}
	a.answer(q.key, 'n')
}

// Without phone, questions have no links.
func TestPhoneOffNoLinks(t *testing.T) {
	var p *phoneAnswers
	if ap, rj, _ := p.issue(watchAsk{key: "ask:q:1"}); ap != "" || rj != "" {
		t.Errorf("links without phone: %q %q", ap, rj)
	}
}

// A lane's dry-run line says a decline came from the phone.
func TestDeclinedOnPhone(t *testing.T) {
	if got := declined(approval.Response{By: "zach", Via: "phone zphone"}); got != "declined by zach on phone zphone" {
		t.Errorf("declined = %q", got)
	}
}
