package cli

import (
	"bufio"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/chars"
	"github.com/gates-brightly/swimlane/internal/ci"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// exitUnanswered is swim _decide's and swim decide's exit code when no
// one answered the decision: the round ends there, SKIP, as a dry run.
const exitUnanswered = 3

// decideMarking is set while swim _decide writes its DECIDED mark: the
// only way one is written (cmdMark refuses it from anyone else).
var decideMarking bool

// cmdDecideHook is a bash lane's `decide NAME "<question>" "<choice>" ...`:
//
//	swim _decide N NAME "<question>" -- CHOICE...
//
// It prints the number chosen (1-based) and exits 0, or records the
// decision as needed (SKIP) and exits 3: no one answered it.
func cmdDecideHook(args []string) error {
	if len(args) < 4 || args[3] != "--" {
		return usagef(`usage: swim _decide N NAME "<question>" -- CHOICE...`)
	}
	return decide(args[0], args[1], args[2], args[4:])
}

// cmdDecide is `swim decide NAME "<question>" -- CHOICE...` from a lane in
// another language: the same as bash's decide. Unanswered, it ends the
// round there (SKIP) as well as exiting 3.
func cmdDecide(args []string) error {
	n, err := laneEnv("decide")
	if err != nil {
		return err
	}
	if len(args) < 3 || args[2] != "--" {
		return usagef(`usage: swim decide NAME "<question>" -- "<choice 1>" "<choice 2>" ...`)
	}
	if err := refuseEnded("decide", n, args[1]); err != nil {
		return err
	}
	err = decide(n, args[0], args[1], args[3:])
	if code, ok := err.(exitError); ok && code.code == exitUnanswered && os.Getenv("SWIM_WRAPPED") != "" {
		// The program can't go on down any branch: end the round here.
		// Nothing failed, so it ends SKIP (the decision is in the log).
		if root, _, ln, cerr := laneCtx(n); cerr == nil && lane.ClaimEnd(root, ln, 0) {
			signalEnd()
		}
	}
	return err
}

// decide answers decision name for lane ns: under swim run, its launcher
// (NAME=N from swim run's own environment, or the operator); run directly
// with bash, NAME=N or the terminal. It prints the choice, or returns
// exitUnanswered after recording why there's none.
func decide(ns, name, question string, choices []string) error {
	switch {
	case !chars.Ident(name):
		return usagef("bad decision name: %s (letters, digits, _)", name)
	case len(choices) < approval.MinChoices || len(choices) > approval.MaxChoices:
		return usagef("decision %s has %d choices: a decision offers %d to %d", name, len(choices), approval.MinChoices, approval.MaxChoices)
	}
	root, cfg, n, err := laneCtx(ns)
	if err != nil {
		return err
	}
	mk := cfg.Masker(os.Environ())
	question = mk.String(question)
	for i := range choices {
		choices[i] = mk.String(choices[i])
	}
	job, run := os.Getenv("SWIM_JOB"), os.Getenv("SWIM_RUN")
	k := len(choices)
	value, valueSet := os.LookupEnv(name)

	unanswered := func(why string) error {
		detail := fmt.Sprintf("dry run: decision needed: %s; %s", name, why)
		if err := cmdMark([]string{ns, logparse.Skip, question, detail}); err != nil {
			return err
		}
		history.Log(root, history.Entry{Event: history.Decline, Lane: n, Job: job, Run: run, Detail: question + "  (" + name + ": " + why + ")"})
		return exitError{exitUnanswered}
	}
	decided := func(choice int, how string) error {
		decideMarking = true
		err := cmdMark([]string{ns, logparse.Decided, fmt.Sprintf("%s: %d. %s", name, choice, ui.SafeField(choices[choice-1])), how})
		decideMarking = false
		if err != nil {
			return err
		}
		fmt.Println(choice)
		return nil
	}

	switch guardRoute(launched(), func() bool { return underSwimRun(root) }, false) {
	case routeLauncher:
		status.Update(root, n, cfg.Lanes, func(l *status.Lane) { l.CurrentStep = "awaiting a decision: " + question })
		req := approval.Request{Lane: n, Job: job, Flag: name, Desc: question, Choices: choices, Token: os.Getenv(approval.TokenEnv)}
		if valueSet {
			req.Value = value
		}
		sock := os.Getenv(approval.EnvVar)
		resp, err := approval.Ask(sock, req, func() bool { return lane.StopRequested(root, n, run) }, roundDeadline())
		if err != nil && resp.Why == "" {
			resp.Why = err.Error()
		}
		if !resp.Approved || resp.Choice < 1 || resp.Choice > k {
			return unanswered(or(resp.Why, fmt.Sprintf("no choice given (set %s=1..%d)", name, k)))
		}
		// The launcher records the decision in the chain as it's used,
		// once, by this lane.
		if _, err := approval.Consume(sock, os.Getenv(approval.TokenEnv), resp.ID, name); err != nil {
			return unanswered("refused: " + err.Error())
		}
		return decided(resp.Choice, resp.How)
	case routeRefuse:
		return unanswered("this lane runs under swim run, but its approvals channel is gone (SWIM_APPROVAL or SWIM_LAUNCHED missing from its environment)")
	}

	// Run directly with bash: only a declared decision is asked, and the
	// operator's own NAME=N or the terminal answers it.
	if info, err := lane.ReadScript(root, n); err == nil && !slices.Contains(info.DecisionNames(), name) {
		return unanswered(name + " isn't declared in " + lane.ScriptName(root, n) + " (list it in # Decisions:)")
	}
	record := func(choice int, method, how string) error {
		approver := chain.Approver{Name: operator()}
		if key, _ := runnerKey(cfg); key != nil {
			approver.Key = key.Public
		}
		return recordDecision(root, cfg, chain.Entry{Type: "decide", Run: run, Lane: n, Job: job, Script: scriptSHA(root, n),
			Guard: name, Action: question, Choices: choices, Choice: choice, Method: method, Statement: how, Approvers: []chain.Approver{approver}})
	}
	if valueSet {
		c, err := strconv.Atoi(value)
		if err != nil || c < 1 || c > k {
			return unanswered(fmt.Sprintf("%s=%s isn't one of the choices (1-%d)", name, value, k))
		}
		how, method := fmt.Sprintf("%s=%d", name, c), "flag"
		if strings.Contains(" "+os.Getenv("SWIM_QUEUE_DECIDED")+" ", fmt.Sprintf(" %s=%d ", name, c)) {
			how, method = how+", decided in "+os.Getenv("SWIM_QUEUE"), "queue"
		}
		if err := record(c, method, how); err != nil {
			return err
		}
		history.Log(root, history.Entry{Event: history.Decide, Lane: n, Job: job, Run: run, Detail: fmt.Sprintf("%s  (%s=%d, by %s)", question, name, c, operator())})
		return decided(c, how)
	}
	if cfg.ApprovalMode() == config.ApprovalsFlags || ci.InCI(os.Getenv) || !ui.IsTTY(os.Stdin) || !ui.IsTTY(os.Stderr) {
		return unanswered(fmt.Sprintf("nobody here to ask (set %s=1..%d)", name, k))
	}
	c := chooseTerminal(approval.DecisionQuestion(approval.Request{Lane: n, Job: job, Flag: name, Desc: question, Choices: choices}), k)
	if c == 0 {
		return unanswered(fmt.Sprintf("no choice given at the prompt (set %s=1..%d)", name, k))
	}
	how := "chosen at the prompt by " + operator()
	if err := record(c, "prompt", how); err != nil {
		return err
	}
	history.Log(root, history.Entry{Event: history.Decide, Lane: n, Job: job, Run: run, Detail: fmt.Sprintf("%s  (%s=%d, by %s)", question, name, c, operator())})
	return decided(c, how)
}

// recordDecision puts a decision a lane run directly with bash answered
// in the chain (under swim run, the launcher records it).
func recordDecision(root string, cfg *config.Config, e chain.Entry) error {
	return record(root, cfg, e)
}

// chooseTerminal asks on the terminal, for a lane run directly with bash:
// a listed number answers; anything else asks again, and an empty line or
// the end of input leaves it unanswered (0).
func chooseTerminal(question []string, k int) int {
	p := painter()
	in := bufio.NewReader(os.Stdin)
	fmt.Fprint(os.Stderr, p.Paint(ui.Yellow+ui.Bold, strings.Join(question, "\n"))+" ")
	for {
		line, err := in.ReadString('\n')
		a := strings.TrimSpace(line)
		if a == "" {
			return 0
		}
		if c, cerr := strconv.Atoi(a); cerr == nil && c >= 1 && c <= k {
			return c
		}
		if err != nil {
			return 0
		}
		fmt.Fprintf(os.Stderr, "%s ", p.Paint(ui.Yellow, fmt.Sprintf("choose 1-%d:", k)))
	}
}
