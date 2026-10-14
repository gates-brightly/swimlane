package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/authority"
	"github.com/gates-brightly/swimlane/internal/lookup"
)

// The launcher checks what an answer through the hub claims against the
// run's own lookup.yml, whatever the asker concluded: a button click never
// meets a require: rule, and signed answers must come from listed keys,
// over this question, and meet the guard's rule.
func TestHubAnswerChecked(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, lookup.FileName), []byte(`users:
  alice: {keys: ["ssh-ed25519 AAAAalice"]}
  bob: {keys: ["ssh-ed25519 AAAAbob"]}
groups:
  ops: [alice, bob]
require:
  - guards: [FIN_*]
    group: ops
    count: 2
`), 0o644)
	lk, err := lookup.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := &runAuthority{snap: &authority.Snapshot{Lookup: lk}}
	q := approval.HubQuestion{Request: "q1", Worker: "w", Lane: 1, Flag: "FIN_DROP", Expires: "x"}
	answer := func(user, key string, q approval.HubQuestion) approval.SignedAnswer {
		return approval.SignedAnswer{User: user, Key: key, Statement: q.Statement(approval.Approve), Sig: "sig"}
	}
	req := approval.Request{ID: "q1", Lane: 1, Flag: "FIN_DROP"}
	cases := []struct {
		name string
		req  approval.Request
		resp approval.Response
		why  string
	}{
		{"button under a rule", req, approval.Response{Approved: true, By: "alice", Hub: approval.HubButton}, "needs signed answers"},
		{"button by a stranger", approval.Request{ID: "q2", Flag: "DEPLOY"}, approval.Response{Approved: true, By: "eve", Hub: approval.HubButton}, "eve isn't in lookup.yml"},
		{"button needing evidence", approval.Request{ID: "q2", Flag: "DEPLOY", EvidenceRequired: true}, approval.Response{Approved: true, By: "alice", Hub: approval.HubButton}, "needs signed answers"},
		{"one signer of two", req, approval.Response{Approved: true, Hub: approval.HubSigned, Signed: []approval.SignedAnswer{answer("alice", "ssh-ed25519 AAAAalice", q)}}, "needs 2 signature(s) from ops, has 1"},
		{"a key named for another", req, approval.Response{Approved: true, Hub: approval.HubSigned, Signed: []approval.SignedAnswer{answer("alice", "ssh-ed25519 AAAAbob", q), answer("bob", "ssh-ed25519 AAAAbob", q)}}, "doesn't match"},
		{"another question", approval.Request{ID: "q9", Lane: 1, Flag: "FIN_DROP"}, approval.Response{Approved: true, Hub: approval.HubSigned, Signed: []approval.SignedAnswer{answer("alice", "ssh-ed25519 AAAAalice", q), answer("bob", "ssh-ed25519 AAAAbob", q)}}, "doesn't match"},
	}
	for _, c := range cases {
		if _, why := a.hubAnswer(c.req, c.resp); !strings.Contains(why, c.why) || why == "" {
			t.Errorf("%s: %q", c.name, why)
		}
	}
	r, why := a.hubAnswer(req, approval.Response{Approved: true, Hub: approval.HubSigned, Signed: []approval.SignedAnswer{answer("alice", "ssh-ed25519 AAAAalice", q), answer("bob", "ssh-ed25519 AAAAbob", q)}})
	if why != "" || r.Entry.Method != approval.HubSigned || len(r.Entry.Approvers) != 2 || !strings.Contains(r.How, "signed by alice (ops), bob (ops)") {
		t.Errorf("two signers: %q %+v", why, r)
	}
	r, why = a.hubAnswer(approval.Request{ID: "q2", Flag: "DEPLOY"}, approval.Response{Approved: true, By: "bob", Hub: approval.HubButton})
	if why != "" || r.Entry.Method != approval.HubButton || !strings.Contains(r.How, "(button, unsigned)") {
		t.Errorf("button: %q %+v", why, r)
	}
}
