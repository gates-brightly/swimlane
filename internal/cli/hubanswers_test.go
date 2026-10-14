package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/hub"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/sign"
)

// answerSetup is lookup.yml with alice, bob and zach (ops: alice, bob;
// FIN_ALLOW_* needs two of ops) and their keys, and mallory's key.
func answerSetup(t *testing.T) (*lookup.Lookup, map[string]*sign.Key) {
	t.Helper()
	if !sign.Available() {
		t.Skip("ssh-keygen not installed")
	}
	dir := t.TempDir()
	keys := map[string]*sign.Key{}
	var b strings.Builder
	b.WriteString("users:\n")
	for _, n := range []string{"alice", "bob", "zach", "mallory"} {
		p := filepath.Join(dir, n)
		if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", n, "-f", p).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		k, err := sign.LoadKey(p)
		if err != nil {
			t.Fatal(err)
		}
		keys[n] = k
		if n != "mallory" {
			b.WriteString("  " + n + ": {keys: [\"" + k.Public + "\"]}\n")
		}
	}
	b.WriteString("groups:\n  ops: [alice, bob]\nrequire:\n  - guards: [FIN_ALLOW_*]\n    group: ops\n    count: 2\n")
	os.WriteFile(filepath.Join(dir, lookup.FileName), []byte(b.String()), 0o644)
	lk, err := lookup.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return lk, keys
}

// asking starts hubAsker on req in the background and waits for its
// question to be published; the answer comes on the channel.
func asking(t *testing.T, cfg *config.Config, req approval.Request, cancel chan struct{}) (hub.PendingApproval, <-chan approval.Response) {
	t.Helper()
	out := make(chan approval.Response, 1)
	go func() { out <- hubAsker(cfg, "w1")(req, cancel) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range waitingApprovals() {
			if p.ID == req.ID {
				return p, out
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the question was never published")
	return hub.PendingApproval{}, nil
}

func signedAnswer(t *testing.T, p hub.PendingApproval, k *sign.Key, decision string) hub.Command {
	t.Helper()
	sig, err := sign.Sign(k, []byte(p.Question("w1").Statement(decision)))
	if err != nil {
		t.Fatal(err)
	}
	return hub.Command{Kind: hub.KindAnswer, By: "someone", Pin: hub.Pin{Request: p.ID, Evidence: p.EvidenceSHA}, Answer: &hub.Answer{Decision: decision, Key: k.Public, Sig: sig}}
}

func button(p hub.PendingApproval, by, decision string) hub.Command {
	return hub.Command{Kind: hub.KindAnswer, By: by, Pin: hub.Pin{Request: p.ID, Evidence: p.EvidenceSHA}, Answer: &hub.Answer{Decision: decision}}
}

func TestHubAskerSigned(t *testing.T) {
	lk, k := answerSetup(t)
	cfg := &config.Config{}
	req := approval.Request{ID: "q-signed", Lane: 3, Flag: "FIN_ALLOW_DROP", Desc: "drop", Script: "s1", Manifest: "m1", Run: "r-1", EvidenceSHA: "e1", Rules: lk}
	p, out := asking(t, cfg, req, nil)
	if !p.SignedOnly || p.Script != "s1" || p.EvidenceSHA != "e1" {
		t.Errorf("published: %+v", p)
	}
	// A button, a stranger's key, a signature over another question, an
	// answer pinned to other evidence: refused, still waiting.
	for name, c := range map[string]hub.Command{
		"button":  button(p, "alice", approval.Approve),
		"mallory": signedAnswer(t, p, k["mallory"], approval.Approve),
		"evidence": func() hub.Command {
			c := signedAnswer(t, p, k["alice"], approval.Approve)
			c.Pin.Evidence = "e0"
			return c
		}(),
		"another": func() hub.Command {
			p2 := p
			p2.Flag = "FIN_ALLOW_ALL"
			c := signedAnswer(t, p2, k["alice"], approval.Approve)
			return c
		}(),
	} {
		if outcome, detail := answerHub(c); outcome != hub.OutcomeRefused {
			t.Errorf("%s: %s %s", name, outcome, detail)
		}
	}
	// Two signers of ops, counted until the rule is met; zach (not in
	// ops) doesn't count toward it.
	if outcome, detail := answerHub(signedAnswer(t, p, k["alice"], approval.Approve)); outcome != hub.OutcomeDone || !strings.Contains(detail, "still needs 2 signature(s) from ops, has 1") {
		t.Errorf("alice: %s %s", outcome, detail)
	}
	if outcome, detail := answerHub(signedAnswer(t, p, k["zach"], approval.Approve)); outcome != hub.OutcomeDone || !strings.Contains(detail, "has 1") {
		t.Errorf("zach: %s %s", outcome, detail)
	}
	if w := waitingApprovals(); len(w) != 1 || strings.Join(w[0].Signers, ",") != "alice,zach" {
		t.Errorf("signers shown: %+v", w)
	}
	if outcome, detail := answerHub(signedAnswer(t, p, k["bob"], approval.Approve)); outcome != hub.OutcomeDone || !strings.Contains(detail, "approved swim 3's FIN_ALLOW_DROP") {
		t.Errorf("bob: %s %s", outcome, detail)
	}
	resp := <-out
	if !resp.Approved || resp.Hub != approval.HubSigned || len(resp.Signed) != 3 || resp.Signed[0].User != "alice" ||
		approval.StatementField(resp.Signed[0].Statement, "worker") != "w1" {
		t.Errorf("answer: %+v", resp)
	}
	// Settled: a late answer is stale.
	if outcome, detail := answerHub(signedAnswer(t, p, k["bob"], approval.Approve)); outcome != hub.OutcomeRefused || !strings.Contains(detail, "stale") {
		t.Errorf("late: %s %s", outcome, detail)
	}
}

func TestHubAskerButton(t *testing.T) {
	lk, k := answerSetup(t)
	hubMode := &config.Config{}
	hubMode.Headless = &config.Headless{Approvals: config.HeadlessHub}

	// A require: rule names FIN_ALLOW_*: a button never approves it, even
	// under headless.approvals: hub; a signed decline settles it.
	p, out := asking(t, hubMode, approval.Request{ID: "q-rule", Lane: 1, Flag: "FIN_ALLOW_X", Rules: lk}, nil)
	if outcome, detail := answerHub(button(p, "alice", approval.Approve)); outcome != hub.OutcomeRefused || !strings.Contains(detail, "require: rule for FIN_ALLOW_X") {
		t.Errorf("button under a rule: %s %s", outcome, detail)
	}
	answerHub(signedAnswer(t, p, k["bob"], approval.Decline))
	if resp := <-out; resp.Approved || !strings.Contains(resp.Why, "declined through the hub by bob (signed)") {
		t.Errorf("signed decline: %+v", resp)
	}

	// No rule names DEPLOY: a listed hub user's click approves it; one
	// lookup.yml doesn't know can't.
	p, out = asking(t, hubMode, approval.Request{ID: "q-button", Lane: 2, Flag: "DEPLOY", Rules: lk}, nil)
	if p.SignedOnly {
		t.Error("signed only under headless.approvals: hub with no rule")
	}
	if outcome, detail := answerHub(button(p, "eve", approval.Approve)); outcome != hub.OutcomeRefused || !strings.Contains(detail, "eve isn't in lookup.yml") {
		t.Errorf("eve: %s %s", outcome, detail)
	}
	answerHub(button(p, "zach", approval.Approve))
	if resp := <-out; !resp.Approved || resp.Hub != approval.HubButton || resp.By != "zach" {
		t.Errorf("button: %+v", resp)
	}

	// headless.approvals: signed (the default): a button doesn't approve,
	// but declines.
	p, out = asking(t, &config.Config{}, approval.Request{ID: "q-default", Lane: 2, Flag: "DEPLOY", Rules: lk}, nil)
	if outcome, detail := answerHub(button(p, "zach", approval.Approve)); outcome != hub.OutcomeRefused || !strings.Contains(detail, "headless.approvals is signed") {
		t.Errorf("button under signed: %s %s", outcome, detail)
	}
	answerHub(button(p, "zach", approval.Decline))
	if resp := <-out; resp.Approved || !resp.Dry || resp.Why != "declined through the hub by zach" {
		t.Errorf("button decline: %+v", resp)
	}
}

// No answer is a decline: by headless.answer_timeout, or as soon as the
// lane stops waiting (its round's Timeout, the run stopping).
func TestHubAskerNoAnswer(t *testing.T) {
	lk, _ := answerSetup(t)
	cfg := &config.Config{}
	cfg.Headless = &config.Headless{AnswerTimeout: "300ms"}
	_, out := asking(t, cfg, approval.Request{ID: "q-timeout", Lane: 1, Flag: "DEPLOY", Rules: lk}, nil)
	select {
	case resp := <-out:
		if resp.Approved || !strings.Contains(resp.Why, "no answer through the hub within headless.answer_timeout (300ms)") {
			t.Errorf("timeout: %+v", resp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no decline at the timeout")
	}
	cancel := make(chan struct{})
	_, out = asking(t, &config.Config{}, approval.Request{ID: "q-cancel", Lane: 1, Flag: "DEPLOY", Rules: lk}, cancel)
	close(cancel)
	if resp := <-out; resp.Approved || !resp.Dry {
		t.Errorf("cancelled: %+v", resp)
	}
	if len(waitingApprovals()) != 0 {
		t.Error("questions left waiting")
	}
	// Without lookup.yml nobody can answer: it declines at once.
	resp := hubAsker(&config.Config{}, "w1")(approval.Request{ID: "q-none", Flag: "DEPLOY", Rules: &lookup.Lookup{}}, nil)
	if resp.Approved || !strings.Contains(resp.Why, "lookup.yml") {
		t.Errorf("no lookup.yml: %+v", resp)
	}
	if outcome, _ := answerHub(hub.Command{Pin: hub.Pin{Request: "q-none"}}); outcome != hub.OutcomeRefused {
		t.Error("an answer to no question")
	}
}
