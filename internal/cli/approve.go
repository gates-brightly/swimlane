package cli

import (
	"bufio"
	"fmt"
	"os"
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
	if cell := lane.CurrentCell(); cell != "" {
		desc = "[" + cell + "] " + desc // each cell of a matrix asks for itself
	}
	job, run := os.Getenv("SWIM_JOB"), os.Getenv("SWIM_RUN")
	dryRun := func(why string) error {
		detail := "dry run: set " + flag + "=1 to approve"
		if why != "" {
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
	if launched() {
		return approveLaunched(root, cfg, args[0], n, flag, desc, flagSet, dryRun, drifted)
	}
	if flagSet {
		// Run directly with bash: the flag is the operator's own.
		return cmdMark([]string{args[0], logparse.Approved, desc, flag + "=1"})
	}

	// Run directly with bash, the lane decides for itself. Signed approvals
	// come first: those of this exact manifest (or, from before manifests,
	// of this script), plus the standing approvals of whoever signed the
	// queue entry this run came from (swim approve --auto), counted
	// together against lookup.yml.
	snap := authority.Take(root, os.Environ())
	d := snap.Signed(authority.Ask{Root: root, Lane: n, Cell: lane.CurrentCell(), Flag: flag, Desc: desc, Job: job, Run: run, Script: sha, Manifest: m}, time.Now())
	laneWarns(root, n, args[0], d.Warns)
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
			Method: d.Entry.Method, Approvers: d.Entry.Approvers, Evidence: d.Entry.Evidence, Observed: facts}); err != nil {
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
	req := approval.Request{Lane: n, Job: job, Flag: flag, Desc: desc, Evidence: d.EvidenceText, EvidenceRequired: d.EvidenceRequired,
		Token: os.Getenv(approval.TokenEnv), Manifest: man}
	if d.Evidence != nil {
		req.EvidenceSHA = d.Evidence.Hash
	}
	broker := os.Getenv(approval.EnvVar) != ""
	if !broker {
		req.Observed = observe() // the terminal question shows them; a broker reads its own
	}
	resp := ask(req, func() bool {
		return lane.StopRequested(root, n, run)
	}, deadline())
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
		Method: method, Statement: how, Approvers: []chain.Approver{approver}, Observed: was}); err != nil {
		return err
	}
	history.Log(root, history.Entry{Event: history.Approve, Lane: n, Job: job, Run: run, Detail: desc + "  (" + flag + ", by " + by + ")"})
	return nil
}

// launched reports whether this process is a lane swim run started, with
// its broker to ask: then the launcher decides and records approvals.
func launched() bool {
	return os.Getenv("SWIM_LAUNCHED") == "1" && os.Getenv(approval.EnvVar) != ""
}

// approveLaunched asks swim run's launcher to decide the guard: it alone
// approves (from what it saw as the run started, or the operator), and it
// records the approval in the chain as this lane uses it (_mark --request).
func approveLaunched(root string, cfg *config.Config, laneArg string, n int, flag, desc string, flagSet bool, dryRun func(string) error, drifted func(map[string]string) error) error {
	if !flagSet {
		status.Update(root, n, cfg.Lanes, func(l *status.Lane) { l.CurrentStep = "awaiting approval: " + desc })
	}
	req := approval.Request{Lane: n, Job: os.Getenv("SWIM_JOB"), Flag: flag, Desc: desc, Token: os.Getenv(approval.TokenEnv), FlagSet: flagSet}
	resp, err := approval.Ask(os.Getenv(approval.EnvVar), req, func() bool {
		return lane.StopRequested(root, n, os.Getenv("SWIM_RUN"))
	}, deadline())
	if err != nil && resp.Why == "" {
		resp.Why = err.Error()
	}
	laneWarns(root, n, laneArg, resp.Warns)
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

// laneWarns writes WARN lines (and diffs) to lane n's log: evidence that
// changed since an approval.
func laneWarns(root string, n int, laneArg string, warns []approval.Warn) {
	for _, w := range warns {
		cmdMark([]string{laneArg, logparse.Warn, w.Label, w.Detail})
		if len(w.Diff) > 0 {
			var lines []string
			for _, l := range w.Diff {
				lines = append(lines, logparse.Indent+"| "+l)
			}
			appendLog(lane.Log(root, n), strings.Join(lines, "\n")+"\n")
		}
	}
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

// deadline is the round's Timeout, from SWIM_DEADLINE (epoch; 0 for none).
func deadline() time.Time {
	if s, err := strconv.ParseInt(os.Getenv("SWIM_DEADLINE"), 10, 64); err == nil && s > 0 {
		return time.Unix(s, 0)
	}
	return time.Time{}
}

// approveRecorded is set when swim _approve has put an approval in the
// chain itself, so the _mark it calls doesn't record it twice.
var approveRecorded bool
