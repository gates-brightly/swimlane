package hub

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/approval"
)

func TestAnswerCommands(t *testing.T) {
	s, srv, worker, alice, vic := commandHub(t)
	op, _ := NewUser(srv.URL, alice, nil)
	viewer, _ := NewUser(srv.URL, vic, nil)
	q := PendingApproval{ID: "q1234567890", Lane: 3, Run: "r-1", Flag: "FIN_ALLOW_DROP", Desc: "drop table <b>old</b>",
		Script: strings.Repeat("a", 64), Manifest: strings.Repeat("b", 64), EvidenceSHA: strings.Repeat("e", 64), Expires: "2026-10-14T12:00:00Z", SignedOnly: true}
	worker.Report(Snapshot{Worker: Worker{Name: "billing", Host: "box", Root: "/r"}, Activity: "running",
		Headless: &HeadlessInfo{Mode: "systemd", Allow: []string{"answer"}}, Waiting: []PendingApproval{q}})
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOx2 alice@laptop"
	sig := "-----BEGIN SSH SIGNATURE-----\nU1NIU0lHAAAAAQAAADMAAAALc3No\nLWVkMjU1MTkAAAAg==\n-----END SSH SIGNATURE-----\n"

	// It must name a question the worker waits on, and carry a decision.
	for _, c := range []Command{
		{Kind: KindAnswer, Pin: Pin{Request: "nope"}, Answer: &Answer{Decision: approval.Approve}},
		{Kind: KindAnswer, Pin: Pin{Request: q.ID}, Answer: &Answer{Decision: "maybe"}},
		{Kind: KindAnswer, Pin: Pin{Request: q.ID}},
		{Kind: KindAnswer, Pin: Pin{Request: q.ID}, Answer: &Answer{Decision: approval.Approve, Key: key}},
		{Kind: KindAnswer, Pin: Pin{Request: q.ID}, Answer: &Answer{Decision: approval.Approve, Key: "not a key", Sig: sig}},
	} {
		if _, err := op.Send(worker.ID, c); err == nil || !strings.Contains(err.Error(), "400") {
			t.Errorf("%+v: %v", c, err)
		}
	}
	// A viewer may send a signed answer (the signature counts, not the
	// login), not a button one.
	if _, err := viewer.Send(worker.ID, Command{Kind: KindAnswer, Pin: Pin{Request: q.ID}, Answer: &Answer{Decision: approval.Approve}}); err == nil || !strings.Contains(err.Error(), "operator role") {
		t.Errorf("a viewer's button answer: %v", err)
	}
	c, err := viewer.Send(worker.ID, Command{Kind: KindAnswer, Pin: Pin{Request: q.ID}, Answer: &Answer{Decision: approval.Approve, Key: key, Sig: sig}})
	if err != nil {
		t.Fatal(err)
	}
	// The hub fills the lane and the evidence the question shows, and
	// stores the signature as one line.
	if c.Lane != 3 || c.Pin.Evidence != q.EvidenceSHA || c.Answer.Sig != "U1NIU0lHAAAAAQAAADMAAAALc3NoLWVkMjU1MTkAAAAg==" || c.By != "vic" {
		t.Errorf("signed answer: %+v %+v", c, c.Answer)
	}
	// The worker gets it with its answer.
	_, cmds, _ := worker.Poll(context.Background(), 0, "", 0)
	if len(cmds) != 1 || cmds[0].Answer == nil || cmds[0].Answer.Key != key || cmds[0].Pin.Request != q.ID {
		t.Fatalf("polled: %+v", cmds)
	}
	if _, err := op.Send(worker.ID, Command{Kind: KindAnswer, Pin: Pin{Request: q.ID}, Answer: &Answer{Decision: approval.Decline}}); err != nil {
		t.Errorf("an operator's decline: %v", err)
	}
	// Only answers carry answers.
	d, _ := op.Send(worker.ID, Command{Kind: KindDrain, Answer: &Answer{Decision: approval.Approve}})
	if d.Answer != nil {
		t.Errorf("a drain carried an answer: %+v", d.Answer)
	}

	// The page lists the question; a logged-in user gets a page to sign
	// an answer: the statement, bound to this worker, and the command.
	jar, _ := cookiejar.New(nil)
	br := &http.Client{Jar: jar}
	br.PostForm(srv.URL+"/api/login", url.Values{"token": {vic}})
	page := getWith(t, br, srv.URL+"/w/"+worker.ID)
	if !strings.Contains(page, "Waiting for an answer") || !strings.Contains(page, "drop table &lt;b&gt;old&lt;/b&gt;") || !strings.Contains(page, "Sign an answer") ||
		strings.Contains(page, ">Approve<") || !strings.Contains(page, "only signed answers approve it") {
		t.Errorf("worker page:\n%s", page)
	}
	ap := getWith(t, br, srv.URL+"/w/"+worker.ID+"/answer/"+q.ID)
	stmt := q.Question(worker.ID).Statement(approval.Approve)
	if !strings.Contains(ap, "request: "+q.ID) || !strings.Contains(ap, "worker: "+worker.ID) || !strings.Contains(ap, "decision: approve") ||
		!strings.Contains(ap, "ssh-keygen -Y sign -n swim@gates-brightly") || !strings.Contains(stmt, "evidence-sha256: "+q.EvidenceSHA+"\n") {
		t.Errorf("answer page:\n%s", ap)
	}
	if ap := getWith(t, br, srv.URL+"/w/"+worker.ID+"/answer/"+q.ID+"?decision=decline"); !strings.Contains(ap, "decision: decline") {
		t.Error("the decline statement")
	}
	if resp, _ := http.Get(srv.URL + "/w/" + worker.ID + "/answer/" + q.ID); resp.Request.URL.Path != "/login" {
		t.Errorf("the answer page without a login: %s", resp.Request.URL.Path)
	}
	_ = s
}

func TestHubStatement(t *testing.T) {
	q := approval.HubQuestion{Request: "r1", Worker: "w1", Lane: 2, Flag: "F", Expires: "x", Cell: "eu\nflag: OTHER"}
	st := q.Statement(approval.Approve)
	if strings.Count(st, "\nflag: ") != 1 || approval.StatementField(st, "flag") != "F" || approval.StatementField(st, "cell") != "eu flag: OTHER" ||
		!strings.HasPrefix(st, "swim hub answer v1\nrequest: r1\nworker: w1\nlane: 2\n") || !strings.HasSuffix(st, "decision: approve\nexpires: x\n") {
		t.Errorf("statement:\n%s", st)
	}
	if q.Statement(approval.Decline) == st {
		t.Error("the decision isn't signed")
	}
}
