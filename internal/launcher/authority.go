package launcher

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/authority"
	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/manifest"
)

// runAuthority is the launcher as the only judge of a run's guards: every
// guard of every lane asks it (approval broker), it decides from what it
// saw when the run started (signed approvals, lookup.yml, its own
// environment's guard flags) or asks the operator, and it records each
// approval in the chain as the lane uses it. When the last lane exits it
// attests the run: what each lane ran and every approval it recorded, so
// swim verify can tell an approve entry a lane wrote itself.
type runAuthority struct {
	snap *authority.Snapshot
	disp *display.Display

	mu        sync.Mutex
	approvals []string        // approve entries recorded, in order
	flags     map[string]bool // guard flags approved from swim run's environment
}

// startAuthority starts the approval broker. ask asks the operator (nil:
// no one can be asked, so a guard that nothing else approves is a dry run).
func startAuthority(o *Options, ask approval.Asker, disp *display.Display) (*approval.Server, error) {
	a := &runAuthority{snap: authority.Take(o.Root, os.Environ()), disp: disp, flags: map[string]bool{}}
	srv, err := approval.Listen(func(req approval.Request, cancel <-chan struct{}) approval.Response {
		return a.decide(*o, req, ask, cancel)
	})
	if err != nil {
		return nil, err
	}
	srv.Refused = func(c approval.Caller, req approval.Request, why string) {
		a.refused(*o, c, req, why)
	}
	srv.Record = func(c approval.Caller, g *approval.Grant) (string, error) {
		hash, err := authority.Record(o.Root, o.Cfg, g.Entry)
		if err != nil {
			return "", err
		}
		a.mu.Lock()
		a.approvals = append(a.approvals, hash)
		if g.Entry.Method == "flag" || g.Entry.Method == "queue" {
			a.flags[g.Entry.Guard] = true
		}
		a.mu.Unlock()
		history.Log(o.Root, history.Entry{Event: history.Approve, Lane: g.Entry.Lane, Job: g.Entry.Job, Run: g.Entry.Run, Detail: g.History})
		return hash, nil
	}
	o.auth = a
	return srv, nil
}

func (a *runAuthority) refused(o Options, c approval.Caller, req approval.Request, why string) {
	who := fmt.Sprintf("swim %d", c.Lane)
	if c.Lane == 0 {
		who = "a process without a lane token"
	}
	a.disp.Message(fmt.Sprintf("swim: approval refused for %s: %s", who, why))
	history.Log(o.Root, history.Entry{Event: history.Decline, Lane: c.Lane, Job: c.Job, Run: o.RunID,
		Detail: fmt.Sprintf("%s  (%s: refused: %s)", approval.Clean(req.Desc), req.Flag, why)})
}

// decide answers one guard of a lane (req's lane, cell, job and manifest
// are the launcher's: the server replaced the lane's claims). In order: a
// flag the lane has set (approved only if swim run's own environment set
// it), the signed approvals as they were when the run started, then the
// operator (a pre-answer, -y, the prompt), if anyone can be asked.
func (a *runAuthority) decide(o Options, req approval.Request, ask approval.Asker, cancel <-chan struct{}) approval.Response {
	if req.Job == "" {
		req.Job = runningJob(o, req.Lane, req.Cell)
	}
	m := o.manifests[req.Lane]
	entry := chain.Entry{Type: "approve", Run: o.RunID, Lane: req.Lane, Job: req.Job, Guard: req.Flag, Action: req.RawDesc}
	if m != nil {
		entry.Script, entry.Manifest = m.ScriptSHA, m.SHA256
	}
	// Observe: facts are read now, with the decision, and the lane checks
	// them again before the guarded step.
	req.Observed = manifest.Observe(o.Root, probesFor(o, req.Lane, req.Cell), laneEnv(o, req.Lane))
	entry.Observed = req.Observed
	grant := func(resp approval.Response, r authority.Result) approval.Response {
		e := entry
		e.Method, e.Statement, e.Approvers, e.Evidence = r.Entry.Method, r.Entry.Statement, r.Entry.Approvers, r.Entry.Evidence
		resp.Approved, resp.How, resp.Job, resp.Observed = true, r.How, req.Job, req.Observed
		resp.Grant = &approval.Grant{Entry: e, History: r.History}
		return resp
	}

	if req.FlagSet {
		r := a.snap.FlagSet(req.Flag)
		if !r.OK {
			a.refused(o, approval.Caller{Lane: req.Lane, Job: req.Job}, req, r.Why)
			return approval.Response{Dry: true, Why: r.Why, Job: req.Job}
		}
		r.Entry.Approvers = []chain.Approver{a.operator(o.Cfg)}
		r.History = req.RawDesc + "  (" + req.Flag + ", by " + operator() + ")"
		return grant(approval.Response{By: operator()}, r)
	}

	ask0 := authority.Ask{Root: o.Root, Lane: req.Lane, Cell: req.Cell, Flag: req.Flag, Desc: req.RawDesc, Job: req.Job, Run: o.RunID, Manifest: m}
	if m != nil {
		ask0.Script = m.ScriptSHA
	}
	r := a.snap.Signed(ask0, time.Now())
	if r.OK {
		r.History = req.RawDesc + "  (" + req.Flag + ", " + r.History + ")"
		resp := grant(approval.Response{}, r)
		resp.Warns = r.Warns
		return resp
	}
	// The question shows the run's evidence as the launcher reads it.
	req.Evidence, req.EvidenceSHA, req.EvidenceRequired = r.EvidenceText, "", r.EvidenceRequired
	if r.Evidence != nil {
		req.EvidenceSHA = r.Evidence.Hash
	}
	if ask == nil || o.Cfg.ApprovalMode() == config.ApprovalsFlags {
		return approval.Response{Dry: true, Why: r.Why, Warns: r.Warns, Job: req.Job}
	}
	resp := ask(req, cancel)
	resp.Job, resp.Observed, resp.Warns = req.Job, req.Observed, r.Warns
	if !resp.Approved {
		return resp
	}
	by := or(resp.By, operator())
	how := "approved at the prompt by " + by
	method := "prompt"
	if resp.Auto {
		via := or(resp.Via, "swim run -y")
		how, method = "approved by "+via+" ("+by+")", "auto"
		by += " with " + via
	}
	if resp.Pre {
		how, method = "pre-approved by "+by+" at "+resp.At+" in the live view", "preapproved"
	}
	approver := chain.Approver{Name: or(resp.By, operator())}
	if method == "prompt" || resp.By == operator() {
		approver = a.operator(o.Cfg) // the operator at this terminal: the runner's key
	}
	return grant(resp, authority.Result{How: how, History: req.RawDesc + "  (" + req.Flag + ", by " + by + ")",
		Entry: chain.Entry{Method: method, Statement: how, Approvers: []chain.Approver{approver}}})
}

// operator is the operator as an approver, with the runner's key.
func (a *runAuthority) operator(cfg *config.Config) chain.Approver {
	ap := chain.Approver{Name: operator()}
	if k, _ := authority.Key(cfg); k != nil {
		ap.Key = k.Public
	}
	return ap
}

// attest records the run in the chain once its last lane has exited: the
// manifest (and script) each lane ran, every approve entry the launcher
// recorded and the guard flags its environment approved. swim verify holds
// the run's approve and round entries to it.
func (a *runAuthority) attest(o Options, sel []int) {
	if a == nil {
		return
	}
	e := chain.Entry{Type: "run", Run: o.RunID, Manifests: map[string]string{}, Scripts: map[string]string{}}
	for _, n := range sel {
		if m := o.manifests[n]; m != nil {
			e.Manifests[strconv.Itoa(n)] = m.SHA256
			e.Scripts[strconv.Itoa(n)] = m.ScriptSHA
		}
	}
	a.mu.Lock()
	e.Approvals = append([]string(nil), a.approvals...)
	for f := range a.flags {
		e.Flags = append(e.Flags, f)
	}
	a.mu.Unlock()
	sort.Strings(e.Flags)
	if len(e.Manifests) == 0 && len(e.Approvals) == 0 {
		return // nothing ran
	}
	if _, err := authority.Record(o.Root, o.Cfg, e); err != nil {
		fmt.Fprintln(os.Stderr, "swim: run attestation: "+err.Error())
	}
}
