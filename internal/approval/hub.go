package approval

import "strings"

// Answers through the hub. swim no longer has a hub, but chain entries an
// older swim recorded for them still verify (swim verify): an approve
// entry whose method is HubSigned carries each approver's signed
// statement, a "swim hub answer v1" text of one "field: value" per line.

// How an answer through the hub approved (the approve entry's method in
// the chain).
const (
	HubSigned = "hub-signed" // SSH-signed answers whose signers met lookup.yml's rule
	HubButton = "hub-button" // a hub user's click (headless.approvals: hub), only where no require: rule named the guard
)

// Answer decisions.
const (
	Approve = "approve"
	Decline = "decline"
)

// StatementField is a field of a hub answer's statement ("" if missing).
func StatementField(statement, name string) string {
	for _, l := range strings.Split(statement, "\n") {
		if v, ok := strings.CutPrefix(l, name+": "); ok {
			return v
		}
	}
	return ""
}
