package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/hub"
	"github.com/gates-brightly/swimlane/internal/sign"
)

// answerRepo is a repo enrolled with a hub, with lookup.yml listing alice
// and bob (ops; FIN_ALLOW_* needs both), and hub users alice (operator)
// and bob (viewer), whose tokens it returns.
func answerRepo(t *testing.T) (r *repo, url, id string, keys, tokens map[string]string, stop func()) {
	t.Helper()
	r = newRepo(t, "")
	r.env = append(r.env, "HOME="+t.TempDir())
	keys = people(t, r, "groups:\n  ops: [alice, bob]\nrequire:\n  - guards: [FIN_ALLOW_*]\n    group: ops\n    count: 2\n", "alice", "bob")
	url, stop = startHub(t, r, t.TempDir(), "127.0.0.1:0")
	t.Cleanup(stop)
	r.mustSwim("hub", "enroll", url, "--token", "t0ken", "--name", "headless")
	t.Cleanup(func() { r.swim("watch", "--headless", "--stop") })
	var hf struct{ ID string }
	data, _ := os.ReadFile(filepath.Join(r.root, ".swim", "hub.yml"))
	yaml.Unmarshal(data, &hf)
	tokens = map[string]string{}
	for name, role := range map[string]string{"alice": "operator", "bob": "viewer"} {
		out := r.mustSwim("hub", "user", "add", name, "--role", role, "--hub", url, "--admin-token", "adm1n")
		tok := regexp.MustCompile(`(?m)^  ([0-9a-f]{48})$`).FindStringSubmatch(out)
		if tok == nil {
			t.Fatalf("no token in:\n%s", out)
		}
		tokens[name] = tok[1]
	}
	return r, url, hf.ID, keys, tokens, stop
}

// waiting is the question lane n waits on, once the hub has it.
func waiting(t *testing.T, url, id string, n int) hub.PendingApproval {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(url + "/api/enrollments"); err == nil {
			var out struct {
				Workers []hub.View `json:"enrollments"`
			}
			json.NewDecoder(resp.Body).Decode(&out)
			resp.Body.Close()
			for _, w := range out.Workers {
				for _, p := range w.Waiting {
					if w.ID == id && p.Lane == n {
						return p
					}
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("swim %d's question never reached the hub", n)
	return hub.PendingApproval{}
}

// waitDone waits (up to 30s) for n batches to be done.
func waitDone(t *testing.T, r *repo, n int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && len(doneEntries(t, r)) < n {
		time.Sleep(50 * time.Millisecond)
	}
	if got := len(doneEntries(t, r)); got < n {
		hl, _ := os.ReadFile(filepath.Join(r.root, ".swim", "headless.log"))
		t.Fatalf("%d batches done, want %d\nheadless.log:\n%s", got, n, hl)
	}
}

// Guards on a headless worker are answered through the hub: signed answers
// count when lookup.yml's rule is met (a button never does where a
// require: rule names the guard), with headless.approvals: hub a listed
// user's click approves the others, and no answer is a decline.
func TestHeadlessHubApprovals(t *testing.T) {
	long(t)
	r, url, id, keys, tokens, stopHub := answerRepo(t)
	r.mustSwim("config", "signing", "off")
	dropLane(t, r)
	for n := 2; n <= 4; n++ {
		r.script(n, "deploy", `if guard DEPLOY "deploy to staging"; then run "deploy" echo deployed; fi`)
		withHeader(t, r, n, "# Guards:  DEPLOY deploy to staging")
	}
	r.mustSwim("watch", "--headless", "--detach", "--interval", "200ms")
	via := " via " + url + ": "
	alice, _ := hub.NewUser(url, tokens["alice"], nil)
	bob, _ := hub.NewUser(url, tokens["bob"], nil)

	// Signed (the default): FIN_ALLOW_DROP needs alice and bob.
	r.mustSwim("queue", "add", "1")
	q := waiting(t, url, id, 1)
	if q.Flag != "FIN_ALLOW_DROP" || !q.SignedOnly || q.Script == "" || q.Run == "" {
		t.Fatalf("the question: %+v", q)
	}
	// A button doesn't approve it (an operator's click, refused by the
	// worker); a signature over another question doesn't count.
	alice.Send(id, hub.Command{Kind: hub.KindAnswer, Pin: hub.Pin{Request: q.ID}, Answer: &hub.Answer{Decision: approval.Approve}})
	waitLog(t, r, "hub answer by alice"+via+"refused: only a signed answer approves FIN_ALLOW_DROP here (swim.yml's headless.approvals is signed)")
	other := q.Question(id)
	other.Lane = 2
	k, _ := sign.LoadKey(keys["bob"])
	forged, _ := sign.Sign(k, []byte(other.Statement(approval.Approve)))
	bob.Send(id, hub.Command{Kind: hub.KindAnswer, Pin: hub.Pin{Request: q.ID}, Answer: &hub.Answer{Decision: approval.Approve, Key: k.Public, Sig: forged}})
	waitLog(t, r, "hub answer by bob"+via+"refused: bob's signature doesn't match this question")
	// alice from her terminal; bob pastes a signature made with ssh-keygen.
	out := r.mustSwim("approve", "--hub", "headless/"+q.ID[:8], "--key", keys["alice"], "--url", url, "--token", tokens["alice"])
	contains(t, "approve --hub", out, "swim 1 asks to approve FIN_ALLOW_DROP", "decision: approve", "worker: "+id, "sent: approve swim 1's FIN_ALLOW_DROP")
	waitLog(t, r, "hub answer by alice"+via+"done: counted alice's signature for swim 1's FIN_ALLOW_DROP; still needs 2 signature(s) from ops, has 1")
	stmt := filepath.Join(t.TempDir(), "answer.txt")
	os.WriteFile(stmt, []byte(q.Question(id).Statement(approval.Approve)), 0o644)
	if out, err := exec.Command("ssh-keygen", "-Y", "sign", "-q", "-n", sign.Namespace, "-f", keys["bob"], stmt).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	armored, _ := os.ReadFile(stmt + ".sig")
	bob.Send(id, hub.Command{Kind: hub.KindAnswer, Pin: hub.Pin{Request: q.ID}, Answer: &hub.Answer{Decision: approval.Approve, Key: k.Public, Sig: string(armored)}})
	waitLog(t, r, "hub answer by bob"+via+"done: approved swim 1's FIN_ALLOW_DROP (signed by alice, bob)")
	waitDone(t, r, 1)
	contains(t, "lane 1", r.log(1), "APPROVED  drop table old_orders (approved through the hub, signed by alice (ops), bob (ops))", "PASS  drop")

	// No answer by headless.answer_timeout: a dry run. (Not in the policy
	// hash: changing it stales nothing.)
	r.write("swim.yml", "headless:\n  answer_timeout: 1s\n", 0o644)
	r.mustSwim("queue", "add", "2")
	waitDone(t, r, 2)
	contains(t, "lane 2 unanswered", r.log(2), "dry run: no answer through the hub within headless.answer_timeout (1s)")

	// headless.approvals: hub: an operator's click approves DEPLOY (no
	// rule names it), recorded as unsigned.
	r.write("swim.yml", "headless:\n  approvals: hub\n", 0o644)
	r.mustSwim("queue", "add", "3")
	q2 := waiting(t, url, id, 3)
	if q2.SignedOnly {
		t.Errorf("DEPLOY signed only under approvals: hub: %+v", q2)
	}
	alice.Send(id, hub.Command{Kind: hub.KindAnswer, Pin: hub.Pin{Request: q2.ID}, Answer: &hub.Answer{Decision: approval.Approve}})
	waitDone(t, r, 3)
	contains(t, "lane 3 button", r.log(3), "APPROVED  deploy to staging (approved through the hub by alice (ops) (button, unsigned))", "PASS  deploy")
	doc, _ := r.swim("doctor")
	contains(t, "doctor", doc, "headless-hub-answers", "headless-service")

	// The chain names the signers; swim verify checks their signatures and
	// lookup.yml's rule (authorized), and accepts the button.
	links, _ := chain.Read(r.root)
	methods := map[string]int{}
	for _, l := range links {
		if l.Type == "approve" {
			methods[l.Method]++
			if l.Method == approval.HubSigned && (len(l.Approvers) != 2 || l.Approvers[0].Sig == "" || l.Approvers[1].Name != "bob") {
				t.Errorf("hub-signed entry: %+v", l.Approvers)
			}
		}
	}
	if methods[approval.HubSigned] != 1 || methods[approval.HubButton] != 1 {
		t.Errorf("approve entries by method: %v", methods)
	}
	ver := r.mustSwim("verify")
	contains(t, "verify", ver, "consistent  yes", "authorized  yes")
	contains(t, "history", r.mustSwim("history", "--oneline", "--no-pager"), "approve FIN_ALLOW_DROP for swim 1 by alice, bob via hub-signed", "hub answer by bob")
	// An edited statement in the chain is caught.
	cp := chain.Path(r.root)
	orig, _ := os.ReadFile(cp)
	os.WriteFile(cp, []byte(strings.Replace(string(orig), `decision: approve\nexpires`, `decision: approve \nexpires`, -1)), 0o644)
	if out, code := r.swim("verify"); code == 0 || !strings.Contains(out, "answer through the hub") {
		t.Errorf("an edited hub statement: %d\n%s", code, out)
	}
	os.WriteFile(cp, orig, 0o644)

	// The hub goes away while a guard waits: it declines at the deadline,
	// and the worker keeps running.
	r.write("swim.yml", "headless:\n  answer_timeout: 2s\n", 0o644)
	r.mustSwim("queue", "add", "4")
	waiting(t, url, id, 4)
	stopHub()
	waitDone(t, r, 4)
	contains(t, "hub gone", r.log(4), "dry run: no answer through the hub within headless.answer_timeout (2s)")
	if !processAlive(headlessPid(r)) {
		t.Error("the worker stopped with its hub")
	}
}
