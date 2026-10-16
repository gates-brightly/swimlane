package hub

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/chars"
	"github.com/gates-brightly/swimlane/internal/sign"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// Approvals through the hub: a headless worker publishes each guard
// waiting for an answer in its snapshot (Pending); a user answers it with
// an answer command. A signed answer carries the approver's SSH signature
// of the question's statement (approval.HubQuestion), which the worker
// checks against its own lookup.yml; a button answer carries nothing but
// who clicked, and counts only where the worker's swim.yml and lookup.yml
// let it. The hub decides neither: it relays.

// KindAnswer answers a waiting guard.
const KindAnswer = "answer"

// PendingApproval is a guard waiting on a headless worker for an answer
// through the hub.
type PendingApproval struct {
	ID       string `json:"id"` // the question's id: an answer names it
	Lane     int    `json:"lane"`
	Cell     string `json:"cell,omitempty"`
	Job      string `json:"job,omitempty"`
	Run      string `json:"run,omitempty"`
	Flag     string `json:"flag"`
	Desc     string `json:"desc"` // what the lane says the step does
	Script   string `json:"script_sha256,omitempty"`
	Manifest string `json:"manifest_sha256,omitempty"`
	// The run's evidence (what the step will do, capped), its hash, and
	// whether the guard's rule needs approvals bound to it.
	Evidence         string `json:"evidence,omitempty"`
	EvidenceSHA      string `json:"evidence_sha256,omitempty"`
	EvidenceRequired bool   `json:"evidence_required,omitempty"`
	Expires          string `json:"expires"` // the worker stops waiting then (a decline)
	// SignedOnly: only signed answers approve it (swim.yml's
	// headless.approvals is signed, or lookup.yml has a require: rule for
	// it); else a button click by a hub user listed in lookup.yml does.
	SignedOnly bool `json:"signed_only"`
	// Signers counted so far, and what the rule still needs.
	Signers []string `json:"signers,omitempty"`
	Need    string   `json:"need,omitempty"`
}

// Question is what an answer to p from worker is bound to.
func (p PendingApproval) Question(worker string) approval.HubQuestion {
	return approval.HubQuestion{Request: p.ID, Worker: worker, Lane: p.Lane, Cell: p.Cell, Job: p.Job, Run: p.Run,
		Script: p.Script, Manifest: p.Manifest, Evidence: p.EvidenceSHA, Flag: p.Flag, Expires: p.Expires}
}

func (p *PendingApproval) sanitize() {
	for _, f := range []*string{&p.ID, &p.Cell, &p.Job, &p.Run, &p.Flag, &p.Desc, &p.Script, &p.Manifest, &p.EvidenceSHA, &p.Expires, &p.Need} {
		*f = ui.Safe(*f)
	}
	// The evidence keeps its lines; each is made safe.
	p.Evidence = strings.Join(safeAll(strings.Split(p.Evidence, "\n")), "\n")
	p.Signers = safeAll(p.Signers)
}

// Answer is what an answer command carries: the decision, and for a signed
// answer the approver's public key and signature of the statement.
type Answer struct {
	Decision string `json:"decision"` // approve | decline
	Key      string `json:"key,omitempty"`
	Sig      string `json:"sig,omitempty"`
}

// Signed reports whether the answer carries a signature.
func (a *Answer) Signed() bool { return a != nil && a.Sig != "" }

// A public key line, and a signature (one line of base64); each at most
// 16 KiB.
var base64Char = chars.Or(chars.Alnum, chars.In("+/="))

// validKey: "<type> <base64>[ <comment>]", the type 3-64 of [a-z0-9@.-],
// the comment at most 200 printable ASCII characters.
func validKey(s string) bool {
	typ, rest, ok := strings.Cut(s, " ")
	if !ok || len(typ) < 3 || len(typ) > 64 || !chars.All(typ, chars.Or(chars.Lower, chars.Digit, chars.In("@.-"))) {
		return false
	}
	b64, comment, hasComment := strings.Cut(rest, " ")
	if !validSig(b64) {
		return false
	}
	return !hasComment || len(comment) <= 200 && chars.All(comment, func(r rune) bool { return r >= ' ' && r <= '~' })
}

// validSig: one line of base64, 16 characters or more.
func validSig(s string) bool { return len(s) >= 16 && chars.All(s, base64Char) }

// checkAnswer validates an answer command against worker v's waiting
// questions: it must name one, and carry a decision (and a well-formed key
// and signature, if signed). It fills in the lane and, if the sender
// didn't pin one, the evidence the question shows.
func checkAnswer(v *View, c *Command) error {
	a := c.Answer
	if a == nil || (a.Decision != approval.Approve && a.Decision != approval.Decline) {
		return fmt.Errorf("an answer needs a decision: approve or decline")
	}
	a.Key = strings.TrimSpace(a.Key)
	a.Sig = sign.Unarmor(a.Sig)
	switch {
	case a.Sig == "" && a.Key != "":
		return fmt.Errorf("a key without a signature")
	case len(a.Key) > 16<<10 || len(a.Sig) > 16<<10:
		return fmt.Errorf("the key or signature is too long")
	case a.Sig != "" && !validKey(a.Key):
		return fmt.Errorf("a signed answer needs the signer's public key (one line: type, base64, comment)")
	case a.Sig != "" && !validSig(a.Sig):
		return fmt.Errorf("the signature isn't ssh-keygen -Y sign output")
	}
	if v == nil {
		return fmt.Errorf("no such worker")
	}
	for _, p := range v.Waiting {
		if p.ID == c.Pin.Request {
			c.Lane = p.Lane
			if c.Pin.Evidence == "" {
				c.Pin.Evidence = p.EvidenceSHA
			}
			return nil
		}
	}
	return fmt.Errorf("no question %s waits on this worker (answered, timed out, or never asked)", c.Pin.Request)
}

// SignCommand is the shell an approver runs to sign statement: it writes
// the statement to a file and signs it into answer.txt.sig.
func SignCommand(statement string) string {
	return "cat > answer.txt <<'EOF'\n" + statement + "EOF\nssh-keygen -Y sign -n " + sign.Namespace + " -f ~/.ssh/id_ed25519 answer.txt   # writes answer.txt.sig"
}

var answerTmpl = template.Must(template.New("answer").Funcs(funcs).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>answer · {{.Name}} · swim hub</title>` + style + `</head><body>
<div><a href="/w/{{.ID}}">← {{.Name}}</a></div>
{{with .P}}<h1>swim {{.Lane}}{{if .Cell}} [{{.Cell}}]{{end}} asks to approve <span class="mono">{{.Flag}}</span></h1>
<div class="muted mono">{{if .Job}}job {{.Job}} · {{end}}run {{.Run}} · script {{short .Script}} · manifest {{short .Manifest}}{{if .EvidenceSHA}} · evidence {{short .EvidenceSHA}}{{end}} · waits until {{.Expires}}</div>
<p>the lane says: {{.Desc}}</p>
{{if .Evidence}}<h2>Evidence (what it will do)</h2><pre>{{.Evidence}}</pre>{{end}}
{{if .Signers}}<p>signed so far: {{range .Signers}}{{.}} {{end}}{{if .Need}}<span class="muted">· still {{.Need}}</span>{{end}}</p>{{end}}{{end}}
<h2>Sign your answer: {{.Decision}}</h2>
<p class="muted">{{if eq .Decision "approve"}}<a href="?decision=decline">decline instead</a>{{else}}<a href="?decision=approve">approve instead</a>{{end}} · or from a terminal: <span class="mono">swim approve --hub {{.ID}}/{{.P.ID}}{{if eq .Decision "decline"}} --decline{{end}}</span></p>
<p>The worker checks your signature of exactly this statement against its <span class="mono">lookup.yml</span>. Run this (with your key), then paste your public key and <span class="mono">answer.txt.sig</span>:</p>
<pre>{{.Command}}</pre>
<form method="post" action="/api/workers/{{.ID}}/commands"><input type="hidden" name="csrf" value="{{.CSRF}}">
<input type="hidden" name="kind" value="answer"><input type="hidden" name="request" value="{{.P.ID}}"><input type="hidden" name="evidence" value="{{.P.EvidenceSHA}}"><input type="hidden" name="decision" value="{{.Decision}}">
<p><textarea name="key" rows="2" cols="80" placeholder="ssh-ed25519 AAAA... you@host (your .pub)"></textarea></p>
<p><textarea name="sig" rows="8" cols="80" placeholder="-----BEGIN SSH SIGNATURE-----"></textarea></p>
<button>Send the signed answer</button></form>
</body></html>`))

// answerPage shows a waiting question and how to sign an answer to it:
// the statement, the ssh-keygen command, and a form for the signature.
// It doesn't refresh (a paste would be lost).
func (s *Server) answerPage(w http.ResponseWriter, r *http.Request) {
	user, _, csrf := s.viewer(r)
	if user == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	v, ok := s.worker(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	var p *PendingApproval
	for i := range v.Waiting {
		if v.Waiting[i].ID == r.PathValue("req") {
			p = &v.Waiting[i]
		}
	}
	if p == nil {
		http.Error(w, "no such question waits on this worker (answered, timed out, or never asked)", http.StatusNotFound)
		return
	}
	decision := approval.Approve
	if r.URL.Query().Get("decision") == approval.Decline {
		decision = approval.Decline
	}
	d := struct {
		ID, Name, CSRF, Decision, Command string
		P                                 *PendingApproval
	}{ID: v.ID, Name: v.Name, CSRF: csrf, Decision: decision, P: p, Command: SignCommand(p.Question(v.ID).Statement(decision))}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	answerTmpl.Execute(w, d)
}
