package launcher

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/authority"
	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/manifest"
	"github.com/gates-brightly/swimlane/internal/secrets"
	"github.com/gates-brightly/swimlane/internal/sign"
	"github.com/gates-brightly/swimlane/internal/status"
)

// runAuthority is the launcher as the only judge of a run's guards: every
// guard of every lane asks it (approval broker), it decides from what it
// saw when the run started (signed approvals, lookup.yml, its own
// environment's guard flags) or asks the operator, and it records each
// approval in the chain as the lane uses it. When the last lane exits it
// attests the run: what each lane ran and every approval it recorded, so
// swim verify can tell an approve entry a lane wrote itself. Every entry
// of the run, the lanes' rounds included, is signed with the run key
// (runKey), so a lane needs no key of its own.
type runAuthority struct {
	snap *authority.Snapshot
	disp *display.Display

	mu        sync.Mutex
	approvals []string        // approve entries recorded, in order
	flags     map[string]bool // guard flags approved from swim run's environment
	// granted are the guards approved for each lane process (by its
	// token), as it used them: what releases a guarded secret to it.
	// Forgotten when the process exits.
	granted map[string]map[string]secrets.Approval
	// recorded are the entries each lane process had recorded ("token
	// round"): one round or check entry each.
	recorded map[string]bool
}

// startAuthority starts the approval broker. ask asks the operator (nil:
// no one can be asked, so a guard that nothing else approves is a dry run).
func startAuthority(o *Options, ask approval.Asker, disp *display.Display) (*approval.Server, error) {
	a := &runAuthority{snap: authority.Take(o.Root, os.Environ()), disp: disp, flags: map[string]bool{}, granted: map[string]map[string]secrets.Approval{},
		recorded: map[string]bool{}}
	srv, err := approval.Listen(func(req approval.Request, cancel <-chan struct{}) approval.Response {
		resp := a.decide(*o, req, ask, cancel)
		if !resp.Approved {
			dryRun(*o, disp, req.Lane, req.Flag)
		}
		return resp
	})
	if err != nil {
		return nil, err
	}
	srv.FailUndeclared = o.FailUnanswerable
	srv.Refused = func(c approval.Caller, req approval.Request, why string) {
		a.refused(*o, c, req, why)
		if c.Lane != 0 {
			dryRun(*o, disp, c.Lane, req.Flag)
		}
	}
	srv.Record = func(c approval.Caller, g *approval.Grant) (string, error) {
		hash, err := o.record(g.Entry)
		if err != nil {
			return "", err
		}
		a.mu.Lock()
		a.approvals = append(a.approvals, hash)
		if g.Entry.Method == "flag" || g.Entry.Method == "queue" {
			a.flags[g.Entry.Guard] = true
		}
		if c.Token != "" && g.Entry.Guard != "" {
			if a.granted[c.Token] == nil {
				a.granted[c.Token] = map[string]secrets.Approval{}
			}
			a.granted[c.Token][g.Entry.Guard] = secrets.Approval{Flag: g.Entry.Guard, Via: g.Entry.Method, Entry: hash}
		}
		a.mu.Unlock()
		unprompted(*o, c, g.Entry)
		history.Log(o.Root, history.Entry{Event: history.Approve, Lane: g.Entry.Lane, Job: g.Entry.Job, Run: g.Entry.Run, Detail: g.History})
		return hash, nil
	}
	srv.Chain = func(c approval.Caller, e chain.Entry) (string, error) { return a.laneEntry(o, c, e) }
	o.auth = a
	return srv, nil
}

// runKey makes the key the launcher signs the run's entries with: a run
// key in its memory, which the runner key vouches for once (one prompt or
// touch, for a key that asks: sign.NewRunKey). Without a runner key it's
// nil and entries are recorded as before (unsigned, or refused under
// signing: required). When the runner key can't vouch for one (the
// operator said no, a passphrase and no agent), signing: required stops
// the run before any lane starts; auto records the run unsigned, with one
// warning, rather than ask again for every entry.
func runKey(o *Options) error {
	hideMemory()
	long, err := authority.Key(o.Cfg)
	if err != nil || long == nil {
		return nil // authority.Record says why, as before
	}
	if sign.Storage(long) == sign.OnDevice {
		fmt.Fprintln(os.Stderr, "swim: touch the runner key's security key to start the run (once)")
	}
	host, _ := os.Hostname()
	k, err := sign.NewRunKey(long, o.RunID, host)
	switch {
	case err == nil:
		o.runKey = k
	case o.Cfg.SigningMode() == config.SigningRequired:
		return fmt.Errorf("signing: required, but the runner key didn't vouch for a run key: %v", err)
	default:
		fmt.Fprintln(os.Stderr, "swim: warning: chain of custody: the runner key didn't vouch for a run key ("+err.Error()+"); this run is recorded unsigned")
		o.unsigned = true
	}
	return nil
}

// record appends e to the chain, signed with the run key.
func (o *Options) record(e chain.Entry) (string, error) {
	switch {
	case o.runKey != nil:
		return authority.RecordWith(o.Root, o.Cfg, o.runKey, e)
	case o.unsigned:
		return authority.RecordWith(o.Root, o.Cfg, nil, e)
	}
	return authority.Record(o.Root, o.Cfg, e)
}

// laneEntry records the entry a lane process sends as its round ends
// (swim _finish), signed by the launcher: one round or check entry per
// process, for its own lane, job, run and manifest whatever it says.
// Approvals are recorded as they're consumed and the attestation is the
// launcher's own, so a lane gets no other entry signed.
func (a *runAuthority) laneEntry(o *Options, c approval.Caller, e chain.Entry) (string, error) {
	if e.Type != "round" && e.Type != "check" {
		return "", fmt.Errorf("a lane can't record a %q entry", e.Type)
	}
	k := c.Token + " " + e.Type
	a.mu.Lock()
	done := a.recorded[k]
	a.recorded[k] = true
	a.mu.Unlock()
	if done {
		return "", fmt.Errorf("swim %d's process already recorded its %s entry", c.Lane, e.Type)
	}
	r := chain.Entry{Type: e.Type, Run: or(c.Run, e.Run), Lane: c.Lane, Job: or(c.Job, e.Job), Round: e.Round,
		Script: e.Script, Manifest: or(c.Manifest, e.Manifest), Result: e.Result, Exit: e.Exit, Log: e.Log,
		Approvals: e.Approvals, AutoApproved: e.AutoApproved, EnvSHA256: e.EnvSHA256, Handlers: e.Handlers,
		Outputs: e.Outputs, Inputs: e.Inputs, Evidence: e.Evidence, InputRounds: e.InputRounds,
		PausedS: e.PausedS, ReplacedBy: e.ReplacedBy, Cache: e.Cache}
	return o.record(r)
}

// dryRun marks lane n as having dry-run guard flag this
// round: [DRY] in the live view, dry_run in status (swim status, the
// watch screen, the hub).
func dryRun(o Options, disp *display.Display, n int, flag string) {
	if flag == "" || flag == approval.ConfirmFlag {
		return
	}
	disp.Set(n, func(v *display.LaneView) { v.Dry = true })
	status.UpdateFile(o.Root, max(n, o.Cfg.Lanes), func(f *status.File) error {
		f.Get(n).Dry(flag)
		return nil
	})
}

// unprompted lists a guard approved without anyone being asked (-y, or a
// guard flag) on the lane's status entry, where swim
// status, swim ci's summary and the hub show it.
func unprompted(o Options, c approval.Caller, e chain.Entry) {
	via := ""
	switch e.Method {
	case "auto":
		via = or(o.ApprovedVia, "swim run -y")
	case "flag", "queue":
	default:
		return
	}
	status.UpdateFile(o.Root, max(c.Lane, o.Cfg.Lanes), func(f *status.File) error {
		f.Get(c.Lane).Unprompted(e.Guard, via)
		return nil
	})
}

// approved says whether guard flag was approved for the lane process with
// token tok in this run, and how (the approve entry it recorded).
func (a *runAuthority) approved(tok, flag string) (secrets.Approval, bool) {
	if a == nil || tok == "" {
		return secrets.Approval{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	ap, ok := a.granted[tok][flag]
	return ap, ok
}

// forget drops what was approved for a lane process that has exited.
func (a *runAuthority) forget(tok string) {
	if a == nil || tok == "" {
		return
	}
	a.mu.Lock()
	delete(a.granted, tok)
	a.mu.Unlock()
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

// decide answers one guard of a lane (req's lane, job and manifest
// are the launcher's: the server replaced the lane's claims). In order: a
// flag the lane has set (approved only if swim run's own environment set
// it), the signed approvals as they were when the run started, then the
// operator (a pre-answer, -y, the prompt), if anyone can be asked.
func (a *runAuthority) decide(o Options, req approval.Request, ask approval.Asker, cancel <-chan struct{}) approval.Response {
	if req.Job == "" {
		req.Job = runningJob(o, req.Lane)
	}
	m := o.manifests[req.Lane]
	entry := chain.Entry{Type: "approve", Run: o.RunID, Lane: req.Lane, Job: req.Job, Guard: req.Flag, Action: req.RawDesc, Lookup: a.snap.LookupSHA}
	if m != nil {
		entry.Script, entry.Manifest = m.ScriptSHA, m.SHA256
	}
	// Observe: facts are read now, with the decision, and the lane checks
	// them again before the guarded step.
	req.Observed = manifest.Observe(o.Root, probesFor(o, req.Lane), laneEnv(o, req.Lane))
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

	ask0 := authority.Ask{Root: o.Root, Lane: req.Lane, Flag: req.Flag, Desc: req.RawDesc, Job: req.Job, Run: o.RunID, Manifest: m}
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
	req.Script, req.Run, req.Rules = entry.Script, o.RunID, a.snap.Lookup
	resp := ask(req, cancel)
	resp.Job, resp.Observed, resp.Warns = req.Job, req.Observed, r.Warns
	if !resp.Approved {
		return resp
	}
	if resp.Hub != "" {
		hr, why := a.hubAnswer(req, resp)
		if why != "" {
			a.refused(o, approval.Caller{Lane: req.Lane, Job: req.Job}, req, why)
			return approval.Response{Dry: true, Why: why, Warns: r.Warns, Job: req.Job}
		}
		return grant(resp, hr)
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

// hubAnswer checks an approval that came through the hub against the
// run's own rules (lookup.yml as the run started), whatever the asker
// concluded: signed answers must come from listed keys, over this
// question, and meet the guard's rule; a button click counts only where no
// require: rule names the guard and it needs no evidence. It returns the
// decision to record, or why it doesn't count.
func (a *runAuthority) hubAnswer(req approval.Request, resp approval.Response) (authority.Result, string) {
	lk := a.snap.Lookup
	if lk == nil || lk.Path == "" {
		return authority.Result{}, "an answer through the hub, but no lookup.yml to check it against"
	}
	rule := lk.RuleFor(req.Flag)
	switch resp.Hub {
	case approval.HubSigned:
		var users, names []string
		var approvers []chain.Approver
		for _, sa := range resp.Signed {
			u, ok := lk.Who(sa.Key)
			if !ok || u != sa.User || approval.StatementField(sa.Statement, "request") != req.ID ||
				approval.StatementField(sa.Statement, "flag") != req.Flag || approval.StatementField(sa.Statement, "decision") != approval.Approve {
				return authority.Result{}, "a signed answer through the hub doesn't match this question or lookup.yml"
			}
			users = append(users, u)
			names = append(names, lk.Describe(u))
			approvers = append(approvers, chain.Approver{Name: u, Key: sa.Key, Statement: sa.Statement, SignedAt: sa.At, Sig: sa.Sig})
		}
		if ok, need := lk.Satisfied(rule, users); !ok {
			return authority.Result{}, "the signed answers through the hub aren't enough: approval " + need
		}
		how := "approved through the hub, signed by " + strings.Join(names, ", ")
		st := ""
		if len(resp.Signed) > 0 {
			st = resp.Signed[0].Statement
		}
		return authority.Result{How: how, History: req.RawDesc + "  (" + req.Flag + ", hub-signed: " + strings.Join(users, ", ") + ")",
			Entry: chain.Entry{Method: approval.HubSigned, Statement: st, Approvers: approvers}}, ""
	case approval.HubButton:
		switch _, listed := lk.Users[resp.By]; {
		case !listed:
			return authority.Result{}, "hub user " + resp.By + " isn't in lookup.yml"
		case lk.Explicit(req.Flag) || rule.Evidence || req.EvidenceRequired:
			return authority.Result{}, "a hub button click doesn't count for " + req.Flag + ": lookup.yml's rule for it needs signed answers"
		}
		how := "approved through the hub by " + lk.Describe(resp.By) + " (button, unsigned)"
		return authority.Result{How: how, History: req.RawDesc + "  (" + req.Flag + ", hub-button: " + resp.By + ")",
			Entry: chain.Entry{Method: approval.HubButton, Statement: how + "; request " + req.ID, Approvers: []chain.Approver{{Name: resp.By}}}}, ""
	}
	return authority.Result{}, "an answer through the hub of an unknown kind (" + resp.Hub + ")"
}

// operator is the operator as an approver, with the runner's key.
func (a *runAuthority) operator(cfg *config.Config) chain.Approver {
	ap := chain.Approver{Name: operator()}
	if k, _ := authority.Key(cfg); k != nil {
		ap.Key = k.Public
	}
	return ap
}

// attestUnavailable attests a run whose approvals couldn't start: what
// each lane ran, no approvals, and why, so swim verify can tell it from a
// run made directly with bash.
func attestUnavailable(o Options, sel []int, why string) {
	e := chain.Entry{Type: "run", Run: o.RunID, Manifests: map[string]string{}, Scripts: map[string]string{}, ScheduledFor: o.ScheduledFor,
		AuthorityUnavailable: why}
	for _, n := range sel {
		if m := o.manifests[n]; m != nil {
			e.Manifests[strconv.Itoa(n)] = m.SHA256
			e.Scripts[strconv.Itoa(n)] = m.ScriptSHA
		}
	}
	if _, err := o.record(e); err != nil {
		fmt.Fprintln(os.Stderr, "swim: run attestation: "+err.Error())
	}
}

// attest records the run in the chain once its last lane has exited: the
// manifest (and script) each lane ran, every approve entry the launcher
// recorded and the guard flags its environment approved. swim verify holds
// the run's approve and round entries to it.
func (a *runAuthority) attest(o Options, sel []int) {
	if a == nil {
		return
	}
	e := chain.Entry{Type: "run", Run: o.RunID, Manifests: map[string]string{}, Scripts: map[string]string{}, ScheduledFor: o.ScheduledFor}
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
	if _, err := o.record(e); err != nil {
		fmt.Fprintln(os.Stderr, "swim: run attestation: "+err.Error())
	}
}
