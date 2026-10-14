package approval

import (
	"fmt"
	"strings"
)

// Answers through the hub (swim watch --headless). A guard waiting on a
// headless worker is published to its hub as a question; an approver
// answers it there, signed with their SSH key (swim approve --hub, or
// ssh-keygen -Y sign on the statement the hub's page shows) or, where
// swim.yml's headless.approvals is hub, with a button. The hub only
// relays the answer: the worker builds the statement from its own facts,
// checks the signature against lookup.yml, and the launcher grants.

// How an answer through the hub approved (Response.Hub, and the approve
// entry's method in the chain).
const (
	HubSigned = "hub-signed" // SSH-signed answers whose signers meet lookup.yml's rule
	HubButton = "hub-button" // a hub user's click (headless.approvals: hub), only where no require: rule names the guard
)

// Answer decisions.
const (
	Approve = "approve"
	Decline = "decline"
)

// HubQuestion is the question a hub answer is bound to: every field is the
// worker's (the hub can't change one without breaking the signature).
type HubQuestion struct {
	Request  string // the question's id (Request.ID): one ask, never another
	Worker   string // the worker's hub id
	Lane     int
	Cell     string
	Job      string
	Run      string
	Script   string // the lane script's SHA-256
	Manifest string // its execution manifest's
	Evidence string // the run's evidence hash ("" for none)
	Flag     string
	Expires  string // RFC 3339: the worker stops waiting then, and the answer stops counting
}

// Statement is what an approver signs to answer q with decision (approve
// or decline): one field per line, ending in a newline.
func (q HubQuestion) Statement(decision string) string {
	var b strings.Builder
	b.WriteString("swim hub answer v1\n")
	for _, kv := range [][2]string{
		{"request", q.Request}, {"worker", q.Worker}, {"lane", fmt.Sprint(q.Lane)}, {"cell", q.Cell}, {"job", q.Job}, {"run", q.Run},
		{"script-sha256", q.Script}, {"manifest-sha256", q.Manifest}, {"evidence-sha256", q.Evidence},
		{"flag", q.Flag}, {"decision", decision}, {"expires", q.Expires},
	} {
		b.WriteString(kv[0] + ": " + oneLine(kv[1]) + "\n")
	}
	return b.String()
}

// oneLine keeps a field to its line: a newline in it can't add a field.
func oneLine(s string) string { return strings.NewReplacer("\n", " ", "\r", " ").Replace(s) }

// StatementField is a field of a hub answer's statement ("" if missing).
func StatementField(statement, name string) string {
	for _, l := range strings.Split(statement, "\n") {
		if v, ok := strings.CutPrefix(l, name+": "); ok {
			return v
		}
	}
	return ""
}

// SignedAnswer is one approver's signed answer, as the worker checked it.
type SignedAnswer struct {
	User      string // who lookup.yml says the key is
	Key       string
	Statement string
	Sig       string
	At        string // when the worker took it (RFC 3339)
}
