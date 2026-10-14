package secrets

import (
	"net"
	"os"
	"strings"
	"sync"
	"testing"
)

// broker is a test broker: lane processes by token, and the guards
// approved for each.
type broker struct {
	*Server
	mu       sync.Mutex
	approved map[string]map[string]bool // lane token -> flag
	used     []string
	refused  []string
	released []string
}

func newTestBroker(t *testing.T, policies map[string]Policy, check bool) *broker {
	t.Helper()
	v := &Vault{secrets: map[string]Secret{
		"plain":   {Name: "plain", Value: "plain-value-1", Scope: ScopeRepo},
		"guarded": {Name: "guarded", Value: "guarded-value-2", Scope: ScopeRepo},
		"either":  {Name: "either", Value: "either-value-3", Scope: ScopeRepo},
		"once":    {Name: "once", Value: "once-value-4", Scope: ScopeRepo},
	}, errs: map[string]error{}}
	lanes := map[string]Caller{"tok1": {Lane: 1, Job: "j1"}, "tok1b": {Lane: 1, Job: "j1b"}, "tok2": {Lane: 2, Job: "j2"}}
	b := &broker{approved: map[string]map[string]bool{}}
	srv, err := Serve(v, policies, Hooks{
		Identify: func(_ net.Conn, tok string) (Caller, string) {
			c, ok := lanes[tok]
			if !ok {
				return Caller{}, "not a lane of this run"
			}
			return c, ""
		},
		Approved: func(c Caller, flag string) (Approval, bool) {
			b.mu.Lock()
			defer b.mu.Unlock()
			if b.approved[c.Key][flag] {
				return Approval{Via: "prompt", Entry: "h-" + flag}, true
			}
			return Approval{}, false
		},
		Used: func(c Caller, s Secret, how string) {
			b.mu.Lock()
			b.used = append(b.used, s.Name+"@"+string(rune('0'+c.Lane)))
			b.mu.Unlock()
		},
		Released: func(c Caller, s Secret, by []Approval) {
			b.mu.Lock()
			b.released = append(b.released, s.Name+"@"+string(rune('0'+c.Lane))+":"+by[0].Flag+"/"+by[0].Via)
			b.mu.Unlock()
		},
		Refused: func(c Caller, name string, flags []string, why string) {
			b.mu.Lock()
			b.refused = append(b.refused, name+": "+why)
			b.mu.Unlock()
		},
		Check: check,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	b.Server = srv
	return b
}

func (b *broker) approve(tok, flag string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.approved[tok] == nil {
		b.approved[tok] = map[string]bool{}
	}
	b.approved[tok][flag] = true
}

// call asks the broker as the lane process with laneTok ("" for none).
func (b *broker) call(laneTok string, req Request) (*Response, error) {
	env := map[string]string{}
	for _, kv := range b.Env() {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	env[LaneTokenEnv] = laneTok
	resp, _, err := Call(func(k string) string { return env[k] }, req)
	return resp, err
}

func (b *broker) get(laneTok, name string) (string, error) {
	resp, err := b.call(laneTok, Request{Op: "get", Name: name, How: "get"})
	if err != nil {
		return "", err
	}
	return resp.Value, nil
}

func (b *broker) masks(laneTok string) string {
	resp, err := b.call(laneTok, Request{Op: "masks"})
	if err != nil {
		return "error: " + err.Error()
	}
	var names []string
	for _, m := range resp.Masks {
		names = append(names, m.Name)
	}
	return strings.Join(names, ",")
}

var testPolicies = map[string]Policy{
	"guarded": {Guards: [][]string{{"ALLOW_DROP"}}},
	"either":  {Guards: [][]string{{"INFRA_APPLY", "INFRA_DESTROY"}, {"HUB_OK"}}},
	"once":    {Guards: [][]string{{"ALLOW_DROP"}}, Step: true},
}

func TestBrokerGuardedSecrets(t *testing.T) {
	b := newTestBroker(t, testPolicies, false)

	// Unguarded: any lane, with or without a lane token.
	for _, tok := range []string{"", "tok1", "tok2"} {
		if v, err := b.get(tok, "plain"); err != nil || v != "plain-value-1" {
			t.Fatalf("plain as %q: %q %v", tok, v, err)
		}
	}
	// Guarded, no approval: refused, and the refusal says why.
	_, err := b.get("tok1", "guarded")
	if err == nil || err.Error() != "refused: guarded is guarded by ALLOW_DROP, which isn't approved in this run" {
		t.Fatalf("guarded without approval: %v", err)
	}
	if _, err := b.get("", "guarded"); err == nil || !strings.Contains(err.Error(), "no lane token") {
		t.Fatalf("guarded without a lane token: %v", err)
	}
	if _, err := b.get("forged", "plain"); err == nil || !strings.Contains(err.Error(), "refused: not a lane of this run") {
		t.Fatalf("an unknown lane token: %v", err)
	}
	// Approved for lane 1's process: released to it, not to lane 2, nor
	// to another process of lane 1 (another job).
	b.approve("tok1", "ALLOW_DROP")
	before, _ := os.ReadFile(GenPath(b.ln.Addr().String()))
	if v, err := b.get("tok1", "guarded"); err != nil || v != "guarded-value-2" {
		t.Fatalf("guarded after approval: %q %v", v, err)
	}
	after, _ := os.ReadFile(GenPath(b.ln.Addr().String()))
	if string(before) == string(after) {
		t.Errorf("a release didn't bump the generation file (%q)", after)
	}
	for _, tok := range []string{"tok2", "tok1b"} {
		if _, err := b.get(tok, "guarded"); err == nil {
			t.Errorf("released to %s, which wasn't approved", tok)
		}
	}
	// Both groups needed: one flag of the first isn't enough.
	b.approve("tok1", "INFRA_DESTROY")
	if _, err := b.get("tok1", "either"); err == nil || !strings.Contains(err.Error(), "guarded by HUB_OK") {
		t.Fatalf("either with one group approved: %v", err)
	}
	b.approve("tok1", "HUB_OK")
	if v, err := b.get("tok1", "either"); err != nil || v != "either-value-3" {
		t.Fatalf("either with both groups: %q %v", v, err)
	}
	// release: step: once.
	if v, err := b.get("tok1", "once"); err != nil || v != "once-value-4" {
		t.Fatalf("once, first get: %q %v", v, err)
	}
	if _, err := b.get("tok1", "once"); err == nil || !strings.Contains(err.Error(), "release: step") {
		t.Fatalf("once, second get: %v", err)
	}

	// masks: unguarded ones, and the guarded ones released to that lane.
	if got := b.masks(""); got != "plain" {
		t.Errorf("masks without a lane token: %s", got)
	}
	if got := b.masks("tok2"); got != "plain" {
		t.Errorf("masks for lane 2: %s", got)
	}
	if got := b.masks("tok1"); got != "either,guarded,once,plain" {
		t.Errorf("masks for lane 1: %s", got)
	}
	if got := b.masks("forged"); !strings.HasPrefix(got, "error: refused") {
		t.Errorf("masks with a forged token: %s", got)
	}

	// The lane a request names is ignored: the broker's own says.
	b.used = nil
	if _, err := b.call("tok2", Request{Op: "get", Name: "plain", Lane: 1}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(b.used, " ") != "plain@2" {
		t.Errorf("used: %v", b.used)
	}
	if strings.Join(b.released, " ") != "guarded@1:ALLOW_DROP/prompt either@1:INFRA_DESTROY/prompt once@1:ALLOW_DROP/prompt" {
		t.Errorf("released: %v", b.released)
	}
	if len(b.refused) != 6 {
		t.Errorf("refused: %d\n%s", len(b.refused), strings.Join(b.refused, "\n"))
	}
	// A bad run token gets nothing.
	resp, _, err := Call(func(k string) string {
		if k == SockEnv {
			return b.ln.Addr().String()
		}
		return "nope"
	}, Request{Op: "masks"})
	if err == nil || len(resp.Masks) != 0 {
		t.Errorf("bad run token: %v %+v", err, resp)
	}
}

func TestBrokerCheckModeNeverReleases(t *testing.T) {
	b := newTestBroker(t, testPolicies, true)
	b.approve("tok1", "ALLOW_DROP")
	if _, err := b.get("tok1", "guarded"); err == nil || !strings.Contains(err.Error(), "plan --check") {
		t.Fatalf("check mode: %v", err)
	}
	if v, err := b.get("tok1", "plain"); err != nil || v != "plain-value-1" {
		t.Fatalf("check mode, unguarded: %q %v", v, err)
	}
}
