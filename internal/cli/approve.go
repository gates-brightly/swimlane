package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/ci"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/sigfile"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// cmdApprove is `guard FLAG "<desc>"` when FLAG isn't set: ask the operator
// to approve the guarded step, through swim run (SWIM_APPROVAL) or, for a
// lane run directly with bash, on the terminal. It records APPROVED (exit
// 0) or SKIP as a dry run (exit 1), as guard does with the flag.
//
//	swim _approve N FLAG "<desc>"
func cmdApprove(args []string) error {
	if len(args) != 3 {
		return usagef(`usage: swim _approve N FLAG "<desc>"`)
	}
	root, cfg, n, err := laneCtx(args[0])
	if err != nil {
		return err
	}
	flag, desc := args[1], cfg.Masker(os.Environ()).String(args[2])
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

	// A remote approval (.signature) for this exact script comes first.
	sha := scriptSHA(root, n)
	sigWhy := ""
	if lk, err := lookup.Load(root); err != nil {
		sigWhy = err.Error()
	} else {
		var whyNot []string
		for _, f := range sigfile.ForScript(root, sha) {
			d := f.Decide(lk, flag, time.Now())
			if !d.OK {
				whyNot = append(whyNot, d.Why)
				continue
			}
			var names []string
			var approvers []chain.Approver
			for _, v := range d.Signed {
				if v.User == "" {
					continue
				}
				names = append(names, lk.Describe(v.User))
				approvers = append(approvers, chain.Approver{Name: v.User, Key: v.Key, SignedAt: v.SignedAt, Sig: v.Sig})
			}
			approveRecorded = true
			if err := cmdMark([]string{args[0], logparse.Approved, desc, "approved by signature: " + strings.Join(names, ", ")}); err != nil {
				return err
			}
			if err := record(root, cfg, chain.Entry{Type: "approve", Run: run, Lane: n, Job: job, Script: sha, Guard: flag, Action: desc,
				Method: "signature", Statement: f.Statement(), Approvers: approvers}); err != nil {
				return err
			}
			history.Log(root, history.Entry{Event: history.Approve, Lane: n, Job: job, Run: run, Detail: desc + "  (" + flag + ", signed by " + strings.Join(d.Users, ", ") + " in " + filepath.Base(f.Path) + ")"})
			return nil
		}
		if len(whyNot) == 0 {
			// A signature for another version of this lane's script: say so
			// rather than leave the operator wondering why it didn't count.
			all, _ := sigfile.All(root)
			for _, f := range all {
				if f.Lane == n && f.SHA256 != sha && f.Covers(flag) {
					whyNot = append(whyNot, fmt.Sprintf("%s approves another version of lane.%d.sh (signed %s, running %s)", filepath.Base(f.Path), n, short12(f.SHA256), short12(sha)))
				}
			}
		}
		sigWhy = strings.Join(whyNot, "; ")
	}

	ask, why := approvalChannel(cfg)
	if ask == nil {
		if sigWhy != "" {
			why = sigWhy
		}
		return dryRun(why)
	}
	status.Update(root, n, cfg.Lanes, func(l *status.Lane) { l.CurrentStep = "awaiting approval: " + desc })
	resp := ask(approval.Request{Lane: n, Job: job, Flag: flag, Desc: desc}, func() bool {
		return lane.StopRequested(root, n, run)
	}, deadline())
	if !resp.Approved {
		w := "declined at the prompt"
		if resp.By != "" {
			w = "declined at the prompt by " + resp.By
		} else if resp.Why != "" {
			w = "not approved: " + resp.Why
		}
		return dryRun(w)
	}
	by := or(resp.By, operator())
	how := "approved at the prompt by " + by
	if resp.Auto {
		via := or(resp.Via, "swim run -y")
		how = "approved by " + via + " (" + by + ")"
		by += " with " + via
	}
	approveRecorded = true
	if err := cmdMark([]string{args[0], logparse.Approved, desc, how}); err != nil {
		return err
	}
	method, approver := "prompt", chain.Approver{Name: or(resp.By, operator())}
	if resp.Auto {
		method = "auto"
	}
	if k, _ := runnerKey(cfg); k != nil && (method == "prompt" || resp.By == operator()) {
		approver.Key = k.Public // the operator at this terminal: the runner's key
	}
	if err := record(root, cfg, chain.Entry{Type: "approve", Run: run, Lane: n, Job: job, Script: sha, Guard: flag, Action: desc,
		Method: method, Statement: how, Approvers: []chain.Approver{approver}}); err != nil {
		return err
	}
	history.Log(root, history.Entry{Event: history.Approve, Lane: n, Job: job, Run: run, Detail: desc + "  (" + flag + ", by " + by + ")"})
	return nil
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
