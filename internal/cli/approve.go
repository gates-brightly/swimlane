package cli

import (
	"bufio"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/authority"
	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/ci"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/manifest"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// cmdApproveHook is `guard FLAG "<desc>"` when FLAG isn't set: ask the operator
// to approve the guarded step, through swim run (SWIM_APPROVAL) or, for a
// lane run directly with bash, on the terminal. It records APPROVED (exit
// 0) or SKIP as a dry run (exit 1), as guard does with the flag.
//
// Under swim run the launcher decides (signed approvals as they were when
// the run started, then the operator) and records the approval; with
// --flag-set (the lane has FLAG=1) it approves only if swim run's own
// environment set the flag.
//
//	swim _approve N FLAG "<desc>" [--flag-set]
func cmdApproveHook(args []string) error {
	flagSet := len(args) == 4 && args[3] == "--flag-set"
	if flagSet {
		args = args[:3]
	}
	if len(args) != 3 {
		return usagef(`usage: swim _approve N FLAG "<desc>" [--flag-set]`)
	}
	root, cfg, n, err := laneCtx(args[0])
	if err != nil {
		return err
	}
	flag, desc := args[1], cfg.Masker(os.Environ()).String(args[2])
	job, run := os.Getenv("SWIM_JOB"), os.Getenv("SWIM_RUN")
	dryRun := func(why string) error {
		detail := "dry run: set " + flag + "=1 to approve"
		switch {
		case approvalMismatch(why):
			// What was signed changed: the way on is to look at the change
			// and sign it, not to wave it through unsigned.
			detail = "dry run: " + why + "; review the change (swim approve " + args[0] + " shows the manifest), then: swim approve " + args[0] + " --guards " + flag
		case why != "":
			detail = "dry run: " + why + "; set " + flag + "=1 to approve"
		}
		if err := cmdMark([]string{args[0], logparse.Skip, desc, detail}); err != nil {
			return err
		}
		if why != "" {
			history.Log(root, history.Entry{Event: history.Decline, Lane: n, Job: job, Run: run, Detail: desc + "  (" + why + ")"})
		}
		return exitError{1}
	}
	// blocked ends the round failed: a guard no one here can ever answer
	// (the launcher says so) would otherwise dry-run its change on every
	// run, and the lane would look passed.
	blocked := func(why string) error {
		cmdMark([]string{args[0], logparse.Blocked, desc, "can't be answered here: " + why})
		history.Log(root, history.Entry{Event: history.Decline, Lane: n, Job: job, Run: run, Detail: desc + "  (blocked: " + why + ")"})
		return endRound(root, n, 1, "guard "+flag+" can't be answered on this swimmer: "+why)
	}
	m := laneManifest(root, cfg, n)
	sha, man := scriptSHA(root, n), ""
	if m != nil {
		sha, man = m.ScriptSHA, m.SHA256
	}
	// Observe: facts are read when the guard is approved and again just
	// before it returns; a difference ends the round as drift.
	probes := laneProbes(root, n, m)
	observe := func() map[string]string { return manifest.Observe(root, probes, nil) }
	drifted := func(was map[string]string) error {
		if len(probes) == 0 {
			return nil
		}
		diffs := manifest.ObservedDiff(was, observe())
		if len(diffs) == 0 {
			return nil
		}
		if err := cmdMark([]string{args[0], logparse.Drift, "drift: " + strings.Join(diffs, "; "), "observed facts changed between approval and the guarded step (" + flag + ")"}); err != nil {
			return err
		}
		history.Log(root, history.Entry{Event: history.Decline, Lane: n, Job: job, Run: run, Detail: desc + "  (drift: " + strings.Join(diffs, "; ") + ")"})
		return exitError{3}
	}
	route := guardRoute(launched(), func() bool { return underSwimRun(root) }, flagSet)
	switch route {
	case routeLauncher:
		return approveLaunched(root, cfg, args[0], n, flag, desc, flagSet, dryRun, blocked, drifted)
	case routeRefuse:
		// Started by swim run, but without its approval channel: the lane
		// unset it, or swim run couldn't start it. Nobody can decide the
		// guard, and the lane's own flag doesn't count: fail closed.
		why := "this lane runs under swim run, but its approvals channel is gone (SWIM_APPROVAL or SWIM_LAUNCHED missing from its environment)"
		cmdMark([]string{args[0], logparse.Warn, "approval refused: " + flag, why})
		return dryRun(why)
	}
	// Run directly with bash: a guard whose lookup.yml rule asks for
	// evidence-bound approvals (which swim no longer has) can't be
	// approved by anything, the operator's own flag included.
	if lk, err := lookup.Load(root); err == nil {
		if why := lk.EvidenceWhy(flag); why != "" {
			cmdMark([]string{args[0], logparse.Warn, "approval refused: " + flag, why})
			return dryRun(why)
		}
	}
	// Run directly with bash: only a guard the header declares can be
	// approved, as under swim run. With an approvals broker, it decides
	// that, knowing which lane really asks (a lane may claim another's
	// number).
	if info, err := lane.ReadScript(root, n); os.Getenv(approval.EnvVar) == "" && err == nil && info.DynamicGuards == 0 && !slices.Contains(info.GuardFlags(), flag) {
		why := flag + " isn't declared in " + lane.ScriptName(root, n) + " (list it in # Guards:)"
		cmdMark([]string{args[0], logparse.Warn, "approval refused: " + flag, why})
		return dryRun(why)
	}
	switch route {
	case routeFlag:
		// Run directly with bash: the flag is the operator's own. A queued
		// batch's approvals (swim queue add --approve) say where they came
		// from.
		detail := flag + "=1"
		if strings.Contains(" "+os.Getenv("SWIM_QUEUE_APPROVED")+" ", " "+flag+" ") {
			detail += ", approved in " + os.Getenv("SWIM_QUEUE")
		}
		return cmdMark([]string{args[0], logparse.Approved, desc, detail})
	}

	// Run directly with bash, the lane decides for itself. Signed approvals
	// come first: those of this exact manifest (or, from before manifests,
	// of this script), plus the standing approvals of whoever signed the
	// queue entry this run came from (swim approve --auto), counted
	// together against lookup.yml.
	snap := authority.Take(root, os.Environ())
	d := snap.Signed(authority.Ask{Root: root, Lane: n, Flag: flag, Desc: desc, Job: job, Run: run, Script: sha, Manifest: m}, time.Now())
	if d.OK {
		facts := observe()
		if err := drifted(facts); err != nil {
			return err
		}
		approveRecorded = true
		if err := cmdMark([]string{args[0], logparse.Approved, desc, d.How}); err != nil {
			return err
		}
		if err := record(root, cfg, chain.Entry{Type: "approve", Run: run, Lane: n, Job: job, Script: sha, Manifest: man, Guard: flag, Action: desc,
			Method: d.Entry.Method, Approvers: d.Entry.Approvers, Observed: facts, Lookup: snap.LookupSHA}); err != nil {
			return err
		}
		history.Log(root, history.Entry{Event: history.Approve, Lane: n, Job: job, Run: run, Detail: desc + "  (" + flag + ", " + d.History + ")"})
		return nil
	}
	sigWhy := d.Why

	ask, why := approvalChannel(cfg)
	if ask == nil {
		if sigWhy != "" {
			why = sigWhy
		}
		return dryRun(why)
	}
	status.Update(root, n, cfg.Lanes, func(l *status.Lane) { l.CurrentStep = "awaiting approval: " + desc })
	req := approval.Request{Lane: n, Job: job, Flag: flag, Desc: desc,
		Token: os.Getenv(approval.TokenEnv), Manifest: man}
	broker := os.Getenv(approval.EnvVar) != ""
	if !broker {
		req.Observed = observe() // the terminal question shows them; a broker reads its own
	}
	resp := ask(req, func() bool {
		return lane.StopRequested(root, n, run)
	}, roundDeadline())
	// A broker says which lane and job asked: the approval is theirs,
	// whatever this process claimed.
	if resp.Lane != 0 && resp.Lane != n {
		n, args[0] = resp.Lane, strconv.Itoa(resp.Lane)
		if m := manifest.FromEnv(n); m != nil {
			sha, man = m.ScriptSHA, m.SHA256
		}
	}
	if resp.Job != "" {
		job = resp.Job
	}
	if !resp.Approved {
		return dryRun(declined(resp))
	}
	by := or(resp.By, operator())
	how := "approved at the prompt by " + by
	if resp.Auto {
		via := or(resp.Via, "swim run -y")
		how = "approved by " + via + " (" + by + ")"
		by += " with " + via
	}
	if resp.Pre {
		how = "pre-approved by " + by + " at " + resp.At + " in the live view"
	}
	was := resp.Observed
	if !broker {
		was = req.Observed
	}
	if err := drifted(was); err != nil {
		return err
	}
	approveRecorded = true
	mark := []string{args[0], logparse.Approved, desc, how}
	if resp.ID != "" {
		// The broker's approval is used once, by this lane, for this guard.
		mark = append(mark, "--request", resp.ID, "--guard", flag)
	}
	if err := cmdMark(mark); err != nil {
		return err
	}
	method, approver := "prompt", chain.Approver{Name: or(resp.By, operator())}
	if resp.Auto {
		method = "auto"
	}
	if resp.Pre {
		method = "preapproved"
	}
	if k, _ := runnerKey(cfg); k != nil && (method == "prompt" || resp.By == operator()) {
		approver.Key = k.Public // the operator at this terminal: the runner's key
	}
	if err := record(root, cfg, chain.Entry{Type: "approve", Run: run, Lane: n, Job: job, Script: sha, Manifest: man, Guard: flag, Action: desc,
		Method: method, Statement: how, Approvers: []chain.Approver{approver}, Observed: was, Lookup: lookupSHA(root)}); err != nil {
		return err
	}
	history.Log(root, history.Entry{Event: history.Approve, Lane: n, Job: job, Run: run, Detail: desc + "  (" + flag + ", by " + by + ")"})
	return nil
}

// Who decides a lane's guard (guardRoute).
type route int

const (
	routeLauncher route = iota // swim run's launcher, through its approvals channel
	routeRefuse                // nobody: under swim run without the channel; fail closed
	routeFlag                  // the operator's own FLAG=1, for a lane run directly with bash
	routeSelf                  // the lane: signed approvals, then the terminal (or a dry run)
)

// guardRoute decides who decides a guard, from how the lane's process was
// started. A lane swim run started is decided by swim run's launcher; one
// under swim run that lost the channel to it is refused, whatever its
// environment says (its own FLAG=1 never counts there); only a lane run
// directly with bash takes FLAG=1 as the operator's. underRun is asked
// only when it matters (it walks the process tree).
func guardRoute(launched bool, underRun func() bool, flagSet bool) route {
	switch {
	case launched:
		return routeLauncher
	case underRun():
		return routeRefuse
	case flagSet:
		return routeFlag
	}
	return routeSelf
}

// launched reports whether this process is a lane swim run started, with
// its broker to ask: then the launcher decides and records approvals.
func launched() bool {
	return os.Getenv("SWIM_LAUNCHED") == "1" && os.Getenv(approval.EnvVar) != ""
}

// underSwimRun reports whether swim run (swim all, ci, watch) started this
// process's lane, as its environment says. A lane runs as the operator,
// so it can change its environment, or anything else the operator can:
// this tells an honest lane's guards to ask the launcher, and isn't a
// boundary against a hostile one (docs/trust-model.md).
func underSwimRun(string) bool {
	return launched() || os.Getenv("SWIM_LAUNCHED") == "1"
}

// approveLaunched asks swim run's launcher to decide the guard: it alone
// approves (from what it saw as the run started, or the operator), and it
// records the approval in the chain as this lane uses it (_mark --request).
func approveLaunched(root string, cfg *config.Config, laneArg string, n int, flag, desc string, flagSet bool, dryRun, blocked func(string) error, drifted func(map[string]string) error) error {
	if !flagSet {
		status.Update(root, n, cfg.Lanes, func(l *status.Lane) { l.CurrentStep = "awaiting approval: " + desc })
	}
	req := approval.Request{Lane: n, Job: os.Getenv("SWIM_JOB"), Flag: flag, Desc: desc, Token: os.Getenv(approval.TokenEnv), FlagSet: flagSet}
	resp, err := approval.Ask(os.Getenv(approval.EnvVar), req, func() bool {
		return lane.StopRequested(root, n, os.Getenv("SWIM_RUN"))
	}, roundDeadline())
	if err != nil && resp.Why == "" {
		resp.Why = err.Error()
	}
	if !resp.Approved {
		return dryRun(declined(resp))
	}
	if err := drifted(resp.Observed); err != nil {
		return err
	}
	how := resp.How
	if how == "" {
		how = "approved by swim run"
	}
	return cmdMark([]string{laneArg, logparse.Approved, desc, how, "--request", resp.ID, "--guard", flag})
}

// declined says why a guard wasn't approved, for its dry-run line.
func declined(resp approval.Response) string {
	switch {
	case resp.Dry:
		return resp.Why
	case resp.Pre:
		return "pre-declined by " + or(resp.By, operator()) + " at " + resp.At + " in the live view"
	case resp.By != "":
		return "declined at the prompt by " + resp.By
	case resp.Why != "":
		return "not approved: " + resp.Why
	}
	return "declined at the prompt"
}

type askFunc func(req approval.Request, stopped func() bool, deadline time.Time) approval.Response

// approvalChannel picks how to ask: swim run's broker, the terminal (a lane
// run directly), or nobody (CI, no terminal, approvals: flags), with why.
func approvalChannel(cfg *config.Config) (askFunc, string) {
	if cfg.ApprovalMode() == config.ApprovalsFlags {
		return nil, ""
	}
	if sock := os.Getenv(approval.EnvVar); sock != "" {
		return func(req approval.Request, stopped func() bool, dl time.Time) approval.Response {
			resp, err := approval.Ask(sock, req, stopped, dl)
			if err != nil && resp.Why == "" {
				resp.Why = err.Error()
			}
			return resp
		}, ""
	}
	if os.Getenv("SWIM_LAUNCHED") == "1" || ci.InCI(os.Getenv) || !ui.IsTTY(os.Stdin) || !ui.IsTTY(os.Stderr) {
		return nil, "" // no one to ask: the guard is a dry run, as always
	}
	return askTerminal, ""
}

// askTerminal asks on the terminal, for a lane run with bash directly.
func askTerminal(req approval.Request, _ func() bool, _ time.Time) approval.Response {
	p := painter()
	fmt.Fprint(os.Stderr, p.Paint(ui.Yellow+ui.Bold, strings.Join(approval.Question(req), "\n"))+" ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(line)); a == "y" || a == "yes" {
		return approval.Response{Approved: true, By: operator()}
	}
	return approval.Response{By: operator()}
}

func operator() string {
	for _, k := range []string{"SWIM_USER", "USER", "LOGNAME"} {
		if u := os.Getenv(k); u != "" {
			return u
		}
	}
	return "operator"
}

// roundDeadline is the round's Timeout: deadline, from SWIM_DEADLINE
// (epoch seconds, which lane_init exports); the zero time for none.
func roundDeadline() time.Time {
	s, err := strconv.ParseInt(os.Getenv("SWIM_DEADLINE"), 10, 64)
	if err != nil || s <= 0 {
		return time.Time{}
	}
	return time.Unix(s, 0)
}

// approveRecorded is set when swim _approve has put an approval in the
// chain itself, so the _mark it calls doesn't record it twice.
var approveRecorded bool

// approvalMismatch says a guard's reason for a dry run is a signed
// approval of something other than what's running (another version of the
// script, or a stale manifest).
func approvalMismatch(why string) bool {
	return strings.Contains(why, "approves another version of") || strings.Contains(why, ": stale: ")
}
