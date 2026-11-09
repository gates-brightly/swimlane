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
	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/ci"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
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
// Under swim run the launcher decides (the flag in its own environment,
// then the operator) and records the approval; with
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
	job, run := os.Getenv("SWIM_ROUND_ID"), os.Getenv("SWIM_RUN")
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
	// blocked ends the round failed: a guard no one here can ever answer
	// (the launcher says so) would otherwise dry-run its change on every
	// run, and the lane would look passed.
	blocked := func(why string) error {
		cmdMark([]string{args[0], logparse.Blocked, desc, "can't be answered here: " + why})
		history.Log(root, history.Entry{Event: history.Decline, Lane: n, Job: job, Run: run, Detail: desc + "  (blocked: " + why + ")"})
		return endRound(root, n, 1, "guard "+flag+" can't be answered on this watcher: "+why)
	}
	m := laneManifest(root, cfg, n)
	sha, man := scriptSHA(root, n), ""
	if m != nil {
		sha, man = m.ScriptSHA, m.SHA256
	}
	route := guardRoute(launched(), func() bool { return underSwimRun(root) }, flagSet)
	switch route {
	case routeLauncher:
		return approveLaunched(root, cfg, args[0], n, flag, desc, flagSet, dryRun, blocked)
	case routeRefuse:
		// Started by swim run, but without its approval channel: the lane
		// unset it, or swim run couldn't start it. Nobody can decide the
		// guard, and the lane's own flag doesn't count: fail closed.
		why := "this lane runs under swim run, but its approvals channel is gone (SWIM_APPROVAL or SWIM_LAUNCHED missing from its environment)"
		cmdMark([]string{args[0], logparse.Warn, "approval refused: " + flag, why})
		return dryRun(why)
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

	// Run directly with bash, the lane decides for itself: it asks the
	// operator on the terminal, or it's a dry run.
	ask, why := approvalChannel()
	if ask == nil {
		return dryRun(why)
	}
	status.Update(root, n, cfg.Lanes, func(l *status.Lane) { l.CurrentStep = display.WaitingApproval(desc) })
	req := approval.Request{Lane: n, Job: job, Flag: flag, Desc: desc,
		Token: os.Getenv(approval.TokenEnv), Manifest: man}
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
	if resp.Pre {
		how = "pre-approved by " + by + " at " + resp.At + " in the live view"
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
	if resp.Pre {
		method = "preapproved"
	}
	if k, _ := runnerKey(cfg); k != nil && (method == "prompt" || resp.By == operator()) {
		approver.Key = k.Public // the operator at this terminal: the runner's key
	}
	if err := record(root, cfg, chain.Entry{Type: "approve", Run: run, Lane: n, Job: job, Script: sha, Manifest: man, Guard: flag, Action: desc,
		Method: method, Statement: how, Approvers: []chain.Approver{approver}}); err != nil {
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
	routeSelf                  // the lane: the terminal (or a dry run)
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
// boundary against a hostile one (docs/reference/trust-model.md).
func underSwimRun(string) bool {
	return launched() || os.Getenv("SWIM_LAUNCHED") == "1"
}

// approveLaunched asks swim run's launcher to decide the guard: it alone
// approves (from what it saw as the run started, or the operator), and it
// records the approval in the chain as this lane uses it (_mark --request).
func approveLaunched(root string, cfg *config.Config, laneArg string, n int, flag, desc string, flagSet bool, dryRun, blocked func(string) error) error {
	if !flagSet {
		status.Update(root, n, cfg.Lanes, func(l *status.Lane) { l.CurrentStep = display.WaitingApproval(desc) })
	}
	req := approval.Request{Lane: n, Job: os.Getenv("SWIM_ROUND_ID"), Flag: flag, Desc: desc, Token: os.Getenv(approval.TokenEnv), FlagSet: flagSet}
	resp, err := approval.Ask(os.Getenv(approval.EnvVar), req, func() bool {
		return lane.StopRequested(root, n, os.Getenv("SWIM_RUN"))
	}, roundDeadline())
	if err != nil && resp.Why == "" {
		resp.Why = err.Error()
	}
	if !resp.Approved {
		return dryRun(declined(resp))
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
	case resp.Via != "":
		return "declined by " + or(resp.By, operator()) + " on " + resp.Via
	case resp.By != "":
		return "declined at the prompt by " + resp.By
	case resp.Why != "":
		return "not approved: " + resp.Why
	}
	return "declined at the prompt"
}

type askFunc func(req approval.Request, stopped func() bool, deadline time.Time) approval.Response

// approvalChannel picks how to ask: swim run's broker, the terminal (a lane
// run directly), or nobody (CI, no terminal), with why.
func approvalChannel() (askFunc, string) {
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
