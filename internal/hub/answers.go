package hub

import (
	"fmt"
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
	return fmt.Errorf("no question %s waits on this swimmer (answered, timed out, or never asked)", c.Pin.Request)
}

// SignCommand is the shell an approver runs to sign statement: it writes
// the statement to a file and signs it into answer.txt.sig.
func SignCommand(statement string) string {
	return "cat > answer.txt <<'EOF'\n" + statement + "EOF\nssh-keygen -Y sign -n " + sign.Namespace + " -f ~/.ssh/id_ed25519 answer.txt   # writes answer.txt.sig"
}

// questionAPI is a question waiting on worker id, for a user to answer:
// the question, and the statement an answer signs and the command that
// signs it, for ?decision= (approve, the default, or decline).
func (s *Server) questionAPI(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireUser(w, r, RoleViewer); !ok {
		return
	}
	v, ok := s.worker(r.PathValue("id"))
	if !ok {
		http.Error(w, "no such enrollment", http.StatusNotFound)
		return
	}
	var p *PendingApproval
	for i := range v.Waiting {
		if v.Waiting[i].ID == r.PathValue("req") {
			p = &v.Waiting[i]
		}
	}
	if p == nil {
		http.Error(w, "no such question waits on this swimmer (answered, timed out, or never asked)", http.StatusNotFound)
		return
	}
	decision := approval.Approve
	if r.URL.Query().Get("decision") == approval.Decline {
		decision = approval.Decline
	}
	statement := p.Question(v.ID).Statement(decision)
	writeJSON(w, map[string]any{"schema": APISchema, "question": p, "decision": decision, "statement": statement,
		"command": SignCommand(statement), "terminal": "swim approve --hub " + v.ID + "/" + p.ID + map[bool]string{true: " --decline"}[decision == approval.Decline]})
}
