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
	"github.com/gates-brightly/swimlane/internal/evidence"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/queue"
	"github.com/gates-brightly/swimlane/internal/sigfile"
	"github.com/gates-brightly/swimlane/internal/sign"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// cmdApproveHook is `guard FLAG "<desc>"` when FLAG isn't set: ask the operator
// to approve the guarded step, through swim run (SWIM_APPROVAL) or, for a
// lane run directly with bash, on the terminal. It records APPROVED (exit
// 0) or SKIP as a dry run (exit 1), as guard does with the flag.
//
//	swim _approve N FLAG "<desc>"
func cmdApproveHook(args []string) error {
	if len(args) != 3 {
		return usagef(`usage: swim _approve N FLAG "<desc>"`)
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

	// Signed approvals come first: those of this exact script, plus the
	// standing approvals of whoever signed the queue entry this run came
	// from (swim approve --auto), counted together against lookup.yml.
	sha := scriptSHA(root, n)
	sigWhy := ""
	// This run's evidence (its evidence steps' output so far), and whether
	// the guard's rule needs approvals bound to it. Batches run by swim
	// watch don't check evidence: unattended, a drifted plan would turn every
	// queued change into a dry run with no one to re-approve.
	var live *evidence.Index
	if ev := evidence.Load(root, job); ev != nil && ev.Run == run {
		live = ev
	}
	watch := os.Getenv("SWIM_QUEUE") != ""
	evRequired := false
	liveText := ""
	if live != nil {
		liveText, _ = live.Text(root)
	}
	if lk, err := lookup.Load(root); err != nil {
		sigWhy = err.Error()
	} else {
		evRequired = lk.RuleFor(flag).Evidence && !watch
		files := sigfile.ForCell(sigfile.ForScript(root, sha), lane.CurrentCell())
		files = evidenceFilter(root, args[0], files, live, liveText, evRequired, watch, desc)
		queuer := verifiedQueuer(root, lk, n, sha)
		if queuer != "" {
			files = append(files, sigfile.StandingFor(root, queuer)...)
		}
		relevant := false
		for _, f := range files {
			relevant = relevant || f.Covers(flag)
		}
		if relevant {
			d := sigfile.Decide(files, lk, flag, time.Now())
			if d.OK {
				var names []string
				for _, u := range d.Users {
					names = append(names, lk.Describe(u))
				}
				var approvers []chain.Approver
				methods := map[string]bool{}
				for _, g := range d.Grants {
					methods[g.File.Kind] = true
					for _, sg := range g.Signers {
						ev := sg.Events[len(sg.Events)-1]
						approvers = append(approvers, chain.Approver{Name: sg.User, Key: sg.Key, Statement: g.File.Statement(), Prev: ev.Prev, SignedAt: ev.At, Sig: ev.Sig})
					}
				}
				how := "approved by " + strings.Join(names, ", ")
				method := "signature"
				switch {
				case methods[sigfile.Standing] && methods[sigfile.Script]:
					method, how = "signature+standing", how+" (signed, and standing for jobs queued by "+queuer+")"
				case methods[sigfile.Standing]:
					method, how = "standing", how+" (standing approval for jobs queued by "+queuer+")"
				default:
					how += " (signed)"
				}
				evCovered := ""
				for _, g := range d.Grants {
					if g.File.Evidence != "" {
						evCovered = g.File.Evidence
					}
				}
				if evCovered != "" {
					how += "; evidence " + short12(evCovered)
				}
				if watch {
					for _, f := range files {
						if f.Evidence != "" {
							how += "; evidence not checked (watch)"
							break
						}
					}
				}
				approveRecorded = true
				if err := cmdMark([]string{args[0], logparse.Approved, desc, how}); err != nil {
					return err
				}
				if err := record(root, cfg, chain.Entry{Type: "approve", Run: run, Lane: n, Job: job, Script: sha, Guard: flag, Action: desc,
					Method: method, Approvers: approvers, Evidence: evCovered}); err != nil {
					return err
				}
				history.Log(root, history.Entry{Event: history.Approve, Lane: n, Job: job, Run: run, Detail: desc + "  (" + flag + ", " + method + ": " + strings.Join(d.Users, ", ") + ")"})
				return nil
			}
			sigWhy = d.Why
		} else {
			// An approval of another version of this script: say so.
			all, _ := sigfile.All(root)
			for _, f := range all {
				if f.Kind == sigfile.Script && f.Lane == n && f.SHA256 != sha && f.Covers(flag) {
					sigWhy = fmt.Sprintf("%s approves another version of %s (approved %s, running %s)", filepath.Base(f.Path), lane.ScriptName(root, n), short12(f.SHA256), short12(sha))
				}
			}
		}
	}

	ask, why := approvalChannel(cfg)
	if ask == nil {
		if sigWhy != "" {
			why = sigWhy
		}
		return dryRun(why)
	}
	status.Update(root, n, cfg.Lanes, func(l *status.Lane) { l.CurrentStep = "awaiting approval: " + desc })
	resp := ask(approval.Request{Lane: n, Job: job, Flag: flag, Desc: desc, Evidence: liveText, EvidenceRequired: evRequired}, func() bool {
		return lane.StopRequested(root, n, run)
	}, deadline())
	if !resp.Approved {
		w := "declined at the prompt"
		if resp.Pre {
			w = "pre-declined by " + or(resp.By, operator()) + " at " + resp.At + " in the live view"
		} else if resp.By != "" {
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
	if resp.Pre {
		how = "pre-approved by " + by + " at " + resp.At + " in the live view"
	}
	approveRecorded = true
	if err := cmdMark([]string{args[0], logparse.Approved, desc, how}); err != nil {
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
	if err := record(root, cfg, chain.Entry{Type: "approve", Run: run, Lane: n, Job: job, Script: sha, Guard: flag, Action: desc,
		Method: method, Statement: how, Approvers: []chain.Approver{approver}}); err != nil {
		return err
	}
	history.Log(root, history.Entry{Event: history.Approve, Lane: n, Job: job, Run: run, Detail: desc + "  (" + flag + ", by " + by + ")"})
	return nil
}

// evidenceFilter keeps the signed approvals that still apply given this
// run's evidence: one bound to evidence counts only while the evidence
// matches (a WARN and a diff in the log when it doesn't), and when the
// guard's rule requires evidence a script-only approval doesn't count. Under
// swim watch nothing is compared.
func evidenceFilter(root, laneArg string, files []*sigfile.File, live *evidence.Index, liveText string, required, watch bool, desc string) []*sigfile.File {
	if watch {
		return files
	}
	var out []*sigfile.File
	for _, f := range files {
		switch {
		case f.Evidence != "" && (live == nil || f.Evidence != live.Hash):
			now := "no evidence this run"
			if live != nil {
				now = short12(live.Hash)
			}
			cmdMark([]string{laneArg, logparse.Warn, "evidence changed since approval: " + desc,
				fmt.Sprintf("%s approved evidence %s; this run has %s", filepath.Base(f.Path), short12(f.Evidence), now)})
			if old := evidence.Blob(root, f.Evidence); old != "" && liveText != "" {
				var lines []string
				for _, l := range evidence.Diff(old, liveText, 40) {
					lines = append(lines, logparse.Indent+"| "+l)
				}
				if ns := os.Getenv("SWIM_LANE"); ns != "" {
					if n, err := strconv.Atoi(ns); err == nil {
						appendLog(lane.Log(root, n), strings.Join(lines, "\n")+"\n")
					}
				}
			}
		case required && f.Evidence == "":
			// Script-only: doesn't count where the rule needs the plan approved.
		default:
			out = append(out, f)
		}
	}
	return out
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

// verifiedQueuer is who queued the batch this run came from, proven by
// their SSH signature on the queue entry (SWIM_QUEUE_ENTRY, set by swim
// watch) and lookup.yml; "" if there's no such entry, it isn't signed by a
// listed user, or it doesn't hold lane n's exact script.
func verifiedQueuer(root string, lk *lookup.Lookup, n int, sha string) string {
	path := os.Getenv("SWIM_QUEUE_ENTRY")
	if path == "" || filepath.Dir(path) != queue.Dir(root) {
		return ""
	}
	e, err := queue.Load(path)
	if err != nil || e.QueueSig == "" || !e.Has(n, sha) {
		return ""
	}
	if sign.Verify(e.QueuedKey, []byte(e.Statement()), e.QueueSig) != nil {
		return ""
	}
	user, _ := lk.Who(e.QueuedKey)
	return user
}
