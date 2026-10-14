package cli

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/hub"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/sigfile"
)

// Approvals through the hub, on a headless worker. When a guard nothing
// else approves asks (no terminal, no -y), hubAsker publishes the
// question in the worker's snapshot and waits for answer commands: the
// command loop (hubcmds.go) hands each to the question it names. A signed
// answer counts when the key is a user's in lookup.yml (as the run
// started) and its signature covers the statement this worker builds from
// its own facts (approval.HubQuestion); a button answer only when
// swim.yml's headless.approvals is hub and no require: rule names the
// guard. Signers are collected until lookup.yml's rule is met. No answer
// by headless.answer_timeout (or the round's Timeout, or the run
// stopping) is a decline: the guard is a dry run.

// hubAsk is one question waiting for an answer.
type hubAsk struct {
	mu      sync.Mutex
	p       hub.PendingApproval
	answers chan hubAnswer
	done    chan struct{} // closed once the question is settled
}

// hubAnswer is an answer command handed to its question, and where its
// outcome goes.
type hubAnswer struct {
	cmd   hub.Command
	reply chan [2]string
}

var hubAsks = struct {
	sync.Mutex
	m map[string]*hubAsk
}{m: map[string]*hubAsk{}}

// waitingApprovals are the questions waiting now, for the snapshot.
func waitingApprovals() []hub.PendingApproval {
	hubAsks.Lock()
	defer hubAsks.Unlock()
	var out []hub.PendingApproval
	for _, a := range hubAsks.m {
		a.mu.Lock()
		p := a.p
		p.Signers = append([]string(nil), a.p.Signers...)
		a.mu.Unlock()
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Lane < out[j].Lane || out[i].Lane == out[j].Lane && out[i].ID < out[j].ID
	})
	return out
}

// maxEvidence caps the evidence a question shows on the hub.
const maxEvidence = 64 << 10

// hubAsker asks through the hub for the worker enrolled as worker.
func hubAsker(cfg *config.Config, worker string) approval.Asker {
	return func(req approval.Request, cancel <-chan struct{}) approval.Response {
		lk := req.Rules
		if lk == nil || lk.Path == "" {
			return approval.Response{Dry: true, Why: "no answer: an answer through the hub counts only from a user in lookup.yml, and there is none"}
		}
		rule := lk.RuleFor(req.Flag)
		signedOnly := ""
		switch {
		case cfg.HeadlessApprovals() != config.HeadlessHub:
			signedOnly = "swim.yml's headless.approvals is signed"
		case lk.Explicit(req.Flag):
			signedOnly = "lookup.yml has a require: rule for " + req.Flag
		case rule.Evidence || req.EvidenceRequired:
			signedOnly = "its rule needs evidence-bound approval"
		}
		wait := cfg.AnswerTimeout()
		// The statement names the second the wait ends (rounded up).
		expires := time.Now().Add(wait).UTC().Add(time.Second - 1).Truncate(time.Second)
		ev := req.Evidence
		if len(ev) > maxEvidence {
			ev = ev[:maxEvidence] + "\n… (cut: the rest is in .swim/evidence/ on the worker)"
		}
		a := &hubAsk{answers: make(chan hubAnswer), done: make(chan struct{}), p: hub.PendingApproval{
			ID: req.ID, Lane: req.Lane, Cell: req.Cell, Job: req.Job, Run: req.Run, Flag: req.Flag, Desc: req.Desc,
			Script: req.Script, Manifest: req.Manifest, Evidence: ev, EvidenceSHA: req.EvidenceSHA, EvidenceRequired: req.EvidenceRequired,
			Expires: expires.Format(time.RFC3339), SignedOnly: signedOnly != ""}}
		q := a.p.Question(worker)
		hubAsks.Lock()
		hubAsks.m[req.ID] = a
		hubAsks.Unlock()
		defer func() {
			hubAsks.Lock()
			delete(hubAsks.m, req.ID)
			hubAsks.Unlock()
			close(a.done)
		}()
		fmt.Printf("swim watch: swim %d waits for an answer through the hub to %s (question %s, until %s)\n", req.Lane, req.Flag, short(req.ID), expires.Local().Format("15:04:05"))

		timer := time.NewTimer(time.Until(expires))
		defer timer.Stop()
		signed := map[string]approval.SignedAnswer{}
		for {
			select {
			case <-cancel:
				return approval.Response{Dry: true, Why: "no answer through the hub before the lane stopped waiting"}
			case <-timer.C:
				return approval.Response{Dry: true, Why: fmt.Sprintf("no answer through the hub within headless.answer_timeout (%s)", wait)}
			case ans := <-a.answers:
				resp, settled, outcome, detail := a.take(ans.cmd, q, lk, rule, signed, signedOnly, expires)
				ans.reply <- [2]string{outcome, detail}
				if settled {
					return resp
				}
			}
		}
	}
}

// take weighs one answer command. settled: the question is answered
// (resp is the answer); otherwise it keeps waiting. outcome and detail
// are the command's acknowledgement.
func (a *hubAsk) take(cmd hub.Command, q approval.HubQuestion, lk *lookup.Lookup, rule lookup.Rule, signed map[string]approval.SignedAnswer, signedOnly string, expires time.Time) (resp approval.Response, settled bool, outcome, detail string) {
	refuse := func(why string) (approval.Response, bool, string, string) {
		return approval.Response{}, false, hub.OutcomeRefused, why
	}
	ans := cmd.Answer
	what := fmt.Sprintf("swim %d's %s", q.Lane, q.Flag)
	switch {
	case ans == nil || (ans.Decision != approval.Approve && ans.Decision != approval.Decline):
		return refuse("no decision (approve or decline)")
	case cmd.Pin.Evidence != q.Evidence:
		return refuse(fmt.Sprintf("stale: the evidence is %s, not the %s the page showed", orNone(short12(q.Evidence)), orNone(short12(cmd.Pin.Evidence))))
	case time.Now().After(expires):
		return refuse("the question expired at " + q.Expires)
	}
	if ans.Sig != "" {
		user, err := sigfile.VerifyHubAnswer(lk, q.Statement(ans.Decision), ans.Key, ans.Sig)
		if err != nil {
			return refuse(err.Error())
		}
		if ans.Decision == approval.Decline {
			return approval.Response{Dry: true, By: user, Why: "declined through the hub by " + user + " (signed)"}, true, hub.OutcomeDone, "declined " + what + " (signed by " + user + ")"
		}
		signed[user] = approval.SignedAnswer{User: user, Key: ans.Key, Statement: q.Statement(approval.Approve), Sig: ans.Sig, At: time.Now().UTC().Format(time.RFC3339)}
		var users []string
		for u := range signed {
			users = append(users, u)
		}
		sort.Strings(users)
		ok, need := lk.Satisfied(rule, users)
		if !ok {
			a.mu.Lock()
			a.p.Signers, a.p.Need = users, need
			a.mu.Unlock()
			return approval.Response{}, false, hub.OutcomeDone, "counted " + user + "'s signature for " + what + "; still " + need
		}
		var list []approval.SignedAnswer
		for _, u := range users {
			list = append(list, signed[u])
		}
		return approval.Response{Approved: true, By: strings.Join(users, ", "), Via: "hub signed by " + strings.Join(users, ", "), Hub: approval.HubSigned, Signed: list},
			true, hub.OutcomeDone, "approved " + what + " (signed by " + strings.Join(users, ", ") + ")"
	}
	// A button: the hub user who clicked, who must be in lookup.yml.
	user := hubUser(cmd.By)
	if _, listed := lk.Users[user]; !listed {
		return refuse("hub user " + user + " isn't in lookup.yml")
	}
	if ans.Decision == approval.Decline {
		return approval.Response{Dry: true, By: user, Why: "declined through the hub by " + user}, true, hub.OutcomeDone, "declined " + what
	}
	if signedOnly != "" {
		return refuse("only a signed answer approves " + q.Flag + " here (" + signedOnly + "): swim approve --hub, or Sign an answer on the page")
	}
	return approval.Response{Approved: true, By: user, Via: "hub button by " + user, Hub: approval.HubButton},
		true, hub.OutcomeDone, "approved " + what + " (button, unsigned)"
}

// answerHub hands an answer command to the question it names, and returns
// the outcome to acknowledge: refused as stale when no such question
// waits (answered, timed out, or another run's).
func answerHub(cmd hub.Command) (string, string) {
	hubAsks.Lock()
	a := hubAsks.m[cmd.Pin.Request]
	hubAsks.Unlock()
	if a == nil {
		return hub.OutcomeRefused, "stale: no question " + short(cmd.Pin.Request) + " waits here (answered, timed out, or never asked)"
	}
	ans := hubAnswer{cmd: cmd, reply: make(chan [2]string, 1)}
	select {
	case a.answers <- ans:
		r := <-ans.reply
		return r[0], r[1]
	case <-a.done:
		return hub.OutcomeRefused, "stale: question " + short(cmd.Pin.Request) + " was settled before this answer arrived"
	}
}

// hubAskerFor is the asker for a batch on a headless worker: nil when the
// directory isn't enrolled or swim.yml doesn't take answers from the hub.
func hubAskerFor(root string, cfg *config.Config) approval.Asker {
	if !cfg.HeadlessAllows(config.HubAnswer) {
		return nil
	}
	h, err := loadHub(root)
	if err != nil {
		return nil
	}
	return hubAsker(cfg, h.ID)
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
