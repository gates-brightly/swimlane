// Package launcher runs lanes in parallel, orders dependent lanes, relays
// their output with [N] prefixes and prints a summary. It never needs
// editing per round: what to run comes from the lane scripts on disk and the
// dependency table in config.
package launcher

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/envfile"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/manifest"
	"github.com/gates-brightly/swimlane/internal/observe"
	"github.com/gates-brightly/swimlane/internal/proc"
	"github.com/gates-brightly/swimlane/internal/runner"
	"github.com/gates-brightly/swimlane/internal/runrec"
	"github.com/gates-brightly/swimlane/internal/secrets"
	"github.com/gates-brightly/swimlane/internal/sign"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/step"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// Options configures a launch.
type Options struct {
	Root   string
	Cfg    *config.Config
	Lanes  []int  // explicit selection; empty means every lane with a pending round
	Rerun  bool   // with no explicit lanes, also run rounds that already passed
	DryRun bool   // plan only: don't refuse lanes that are running
	RunID  string // this run's id; generated if empty (swim run --run-id)
	// Parallel caps lanes running at once; nil means config's max_parallel,
	// 0 unlimited.
	Parallel *int
	Plain    bool
	Self     string // path of the swim binary, exported to lane scripts as SWIM_BIN
	Out      *os.File
	Stdin    *os.File
	// Finished, if set, is called once after the summary prints (the live
	// view is gone by then), e.g. to learn the run was interrupted.
	Finished func(Finish)
	// YAML, if set, receives the run as a stream of YAML documents (swim
	// run --yaml) instead of the live view and summary table on Out;
	// YAMLOutput adds lane output lines to it.
	YAML       io.Writer
	YAMLOutput bool
	// Interrupt overrides config interrupt for this run: graceful or
	// immediate (swim run --interrupt).
	Interrupt string
	// Prompt lets a guard whose flag isn't set ask the operator (approval
	// broker, approval.go) when the display can: stdin and stdout are a
	// terminal and this isn't the YAML stream.
	Prompt bool
	// AutoApprove (swim run -y) approves every such guard without asking,
	// terminal or not; each approval is logged as by -y. ApprovedBy and
	// ApprovedVia name who and what approved (default: this user, "swim run
	// -y"; a queued batch: who queued it, "queue <id> -y").
	AutoApprove bool
	// ScheduledFor (UTC) is the start time of the scheduled batch this run
	// is (swim schedule), recorded in its attestation.
	ScheduledFor            string
	ApprovedBy, ApprovedVia string
	// Asker, if set, asks when no one at a terminal can: after a
	// pre-answer and -y, a guard nothing else approves is put to it (swim
	// watch --headless: the hub). The launcher still decides: what it
	// answers is checked against the run's rules (authority.go).
	Asker approval.Asker
	// FailUnanswerable fails a guard no one can ever answer in this run (a
	// guard its script doesn't declare; the Asker says so for the rest)
	// rather than dry-running it: for a headless worker, where a dry run
	// would look like a passed change.
	FailUnanswerable bool
	// Pins holds the job each lane must still hold when it starts (lanes
	// named by job id, and every lane swim ci selects): a lane script
	// rewritten in the meantime is skipped, not run.
	Pins map[int]string
	// PinSHA holds the exact script (SHA-256) a lane must hold when it
	// starts (queued batches): an edit since it was queued skips it.
	PinSHA map[int]string
	// CheckOnly is swim plan --check: every selected lane runs at once (After:
	// is ignored; Locks: still apply) up to where change would begin, logs
	// to its check log and leaves lane status alone (lane.CheckEnv is set).
	// Lanes with no stages, and compiled lanes, are refused.
	CheckOnly bool
	// ShowChecks makes the plan show each lane's last check (swim plan
	// --check, after the checks ran).
	ShowChecks bool
	ChecksRan  bool // the checks just ran: the plan sums them up
	// CheckTimeout caps the whole check (0: none): past it the run is
	// force-quit like a second Ctrl-C.
	CheckTimeout time.Duration
	// PinEnv holds the env files' hash (envfile.Resolved.SHA256) a lane
	// must still resolve to when it starts (queued batches).
	PinEnv map[int]string
	// PinManifest holds the execution manifest (manifest_sha256) a lane
	// must still hold when it starts (queued batches).
	PinManifest map[int]string
	// Heartbeat, if set, prints "[N] still running: <step> (<elapsed>)"
	// for a running lane silent this long, so a CI log never looks hung.
	Heartbeat time.Duration

	// Signals, if set, is the signal catcher the caller started first thing
	// (CatchSignals): Run takes it over instead of starting its own.
	Signals *EarlySignals

	beat         *heartbeat
	skipped      *statusBatch // skipped lanes' status, written together (statusbatch.go)
	approvalSock string
	approvalSrv  *approval.Server
	manifests    map[int]*manifest.Manifest // each lane's, staged (stage.go)
	auth         *runAuthority              // decides and records the run's approvals (authority.go)
	runKey       *sign.Key                  // signs the run's entries (runKey, authority.go)
	unsigned     bool                       // the runner key didn't vouch for a run key: record unsigned
	secrets      *runSecrets                // the run's decrypted secrets and broker; nil for none
	env          map[int]*envfile.Resolved  // each lane's env from env files

	// Interactive allows the interactive view (keys pick a lane's log) when
	// more than one lane runs on a terminal; the view then reads Stdin.
	Interactive bool
	// SecretsPrompt, if set, asks the secrets key's passphrase (at path) on
	// the terminal; nil never asks (a protected key then serves no secrets).
	SecretsPrompt func(path string) func() ([]byte, error)
	// HubSecrets, if set, gets the secrets a hub granted this directory
	// (decrypted with its worker key): they resolve under the local store's.
	HubSecrets func() (map[string]secrets.Granted, error)
	// Unattended is a run nobody answers for (a batch swim watch took from
	// the queue): the view's keys only move around, never pre-answer a
	// guard, and the view closes when the run ends.
	Unattended bool
	// NoHangup leaves SIGHUP to the caller (swim watch --headless reopens
	// its log on it): it doesn't force the run the way a closed terminal
	// does.
	NoHangup bool

	ev *events
}

// Finish is a completed run's outcome, passed to Options.Finished.
type Finish struct {
	Code        int            // Run's exit code: 0 only if every lane passed (ExitDryRun: nothing failed, a guard dry-ran)
	Interrupted bool           // Ctrl-C or a signal arrived during the run
	Counts      map[string]int // lanes by final state (status.Passed, ...)
	Elapsed     time.Duration
}

// Outcome is one lane's result.
type Outcome struct {
	N       int
	Job     string // job id from the script (empty for scripts without one)
	State   string
	Exit    int
	Reason  string
	Round   string
	Blocked bool // refused before starting (a blocked command, or no runner)
	// DryRun: skipped only for want of an approval: the round dry-ran a
	// guarded step, or waited on one that did.
	DryRun  bool
	Elapsed time.Duration
	// When the lane's dependencies were met, its locks and slot taken, its
	// process started and finished (zero when it never got that far). The
	// run record (.swim/runs/) keeps them.
	Ready, Locked, Slot, Start, End time.Time
}

// Select resolves which lanes to run and checks none is already running.
// With no explicit lanes it picks every pending round that hasn't already
// passed (unless Rerun), and returns the passed ones as done. Either way
// it adds the Always lanes the selection needs (expandAlways).
func Select(o Options) (sel, done []int, err error) {
	if len(o.Lanes) > 0 {
		seen := map[int]bool{}
		for _, n := range o.Lanes {
			if !o.Cfg.ValidLane(n) {
				return nil, nil, fmt.Errorf("no swim %d: lanes are numbered 1..%d", n, o.Cfg.Lanes)
			}
			info, err := lane.ReadScript(o.Root, n)
			if err != nil {
				return nil, nil, err
			}
			if !info.Pending() {
				return nil, nil, fmt.Errorf("swim %d has no pending round (%s is missing, a stub, or has no Round: line)", n, filepath.Base(lane.Script(o.Root, n)))
			}
			if !seen[n] {
				sel = append(sel, n)
				seen[n] = true
			}
		}
	} else {
		st, _ := status.Load(o.Root)
		for n := 1; n <= o.Cfg.Lanes; n++ {
			info, err := lane.ReadScript(o.Root, n)
			if err != nil || !info.Pending() {
				continue
			}
			var last *status.Lane
			if st != nil {
				last = st.Get(n)
			}
			if !o.Rerun && AlreadyPassed(info, last) {
				done = append(done, n)
				continue
			}
			sel = append(sel, n)
		}
	}
	for _, n := range sel {
		if pid, ok := lane.Running(o.Root, n); ok && !o.DryRun {
			return nil, nil, lane.ErrRunning{Lane: n, PID: pid}
		}
	}
	return sel, done, nil
}

// Run launches the selected lanes and returns 0 only if all passed.
func Run(o Options) (int, error) {
	if !o.DryRun && !o.CheckOnly && o.Self != "" {
		// A lane left running by a process that died is ended first, so
		// it's selected (and shown) as the interrupted round it is.
		for _, r := range ReapDead(o.Root, o.Cfg, o.Self) {
			fmt.Fprintln(os.Stderr, "swim: "+r.String())
		}
	}
	sel, passed, err := Select(o)
	if err != nil {
		return 2, err
	}
	o.ev = newEvents(o.YAML, o.YAMLOutput)
	o.beat = &heartbeat{last: map[int]time.Time{}, started: map[int]time.Time{}}
	if o.RunID == "" {
		o.RunID = lane.NewRunID()
	}
	if len(sel) == 0 {
		if o.ev != nil {
			note := "nothing pending (every lane is a stub or has no lane script)"
			if len(passed) > 0 {
				note = "every pending job has already passed (swim " + joinInts(passed) + ")"
			}
			o.ev.nothing(o, note)
			return 0, nil
		}
		if len(passed) > 0 {
			fmt.Fprintf(o.Out, "swim: nothing to run: every pending job has already passed (swim %s). Rerun one with `swim run N`, or all with `swim all --rerun`.\n", joinInts(passed))
		} else {
			fmt.Fprintln(o.Out, "swim: nothing pending (every lane is a stub or has no lane script). Write one with `swim new N \"<goal>\"`.")
		}
		return 0, nil
	}
	// Signals are caught from here (earlysig.go): one during setup stops
	// the run before any lane starts, and the interrupter gets the rest.
	sigs := o.Signals
	if sigs == nil {
		sigs = CatchSignals(os.Stderr)
	}
	if !o.NoHangup {
		sigs.alsoHangup()
	}
	defer sigs.Stop()
	isDone := map[int]bool{}
	for _, n := range passed {
		isDone[n] = true
	}
	selected := map[int]bool{}
	for _, n := range sel {
		selected[n] = true
	}
	deps, err := ResolveDeps(o.Root, o.Cfg, sel)
	if err != nil {
		return 2, err
	}
	refusedCheck := map[int]string{}
	if o.CheckOnly {
		// Nothing changes, so nothing waits: every check runs at once.
		os.Setenv(lane.CheckEnv, "1")
		deps = map[int][]Dep{}
		for _, n := range sel {
			if why := uncheckable(o.Root, n); why != "" {
				refusedCheck[n] = why
			}
		}
	}
	blocked := BlockedLanes(o.Root, o.Cfg, sel)
	// Env files: every selected lane's env resolves, and none sets a guard
	// flag, or nothing starts.
	lk, _ := lookup.Load(o.Root)
	o.env = map[int]*envfile.Resolved{}
	for _, n := range sel {
		info, _ := lane.ReadScript(o.Root, n)
		r, err := envfile.Check(o.Root, o.Cfg, n, info, lk)
		if err != nil {
			return 2, err
		}
		o.env[n] = r
	}
	// What each lane runs is fixed here, before any waiting: its manifest is
	// built and its files staged, and it runs the staged copy.
	stageLanes(&o, sel, blocked)
	// Secrets (swim secret): decrypted once, now, before the live view
	// takes the terminal; a Secrets: entry that won't resolve stops the run.
	rs, err := openSecrets(&o, sel)
	if err != nil {
		return 2, err
	}
	if rs != nil {
		defer rs.srv.Close()
		o.secrets = rs
	}

	// The panel shows the lanes in use (and any this run starts).
	views := make([]display.LaneView, 0, o.Cfg.Lanes)
	for n := 1; n <= o.Cfg.Lanes; n++ {
		if !o.Cfg.IsActive(n) && !selected[n] {
			continue
		}
		info, _ := lane.ReadScript(o.Root, n)
		v := display.LaneView{N: n, State: display.Idle, Round: info.Round, Job: info.Job,
			KnownGuards: info.GuardFlags(), UnknownGuards: info.DynamicGuards}
		if isDone[n] {
			v.State = display.Done
		}
		if selected[n] {
			v.State = display.Starting
			for _, d := range deps[n] {
				if selected[d.Lane] {
					v.State = display.Waiting
					v.WaitingOn = append(v.WaitingOn, d.Lane)
				}
			}
		}
		views = append(views, v)
	}
	start := time.Now()
	ref := step.GitRef(o.Root)
	cap := o.Cfg.Parallel()
	if o.Parallel != nil {
		cap = *o.Parallel
	}
	sl := newSlots(cap, chainBelow(sel, deps))
	lt := newLockTable()
	var ir *interrupter
	screen, summaryOut := o.Out, io.Writer(o.Out)
	if o.ev != nil {
		// The YAML stream owns stdout: no live view, no table.
		if devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0); err == nil {
			defer devnull.Close()
			screen = devnull
		}
		summaryOut = io.Discard
	}
	pre := newPreAnswers(selected)
	onPre := func(n int, choice string) string { return pre.set(o, n, choice) }
	if o.Unattended {
		onPre = nil // the view says pre-answers aren't available
	}
	disp := display.New(display.Options{
		Out:   screen,
		Plain: o.Plain || o.ev != nil,
		Cap:   cap,
		Title: func(now time.Time) string {
			t := fmt.Sprintf("swim · %s · %s · %s", filepath.Base(o.Root), ref, display.Elapsed(now.Sub(start)))
			if n := ir.Notice(); n != "" {
				t += " · " + n
			}
			return t
		},
		// The interactive view takes the keyboard only when lanes get no stdin
		// (more than one lane, see runLane). Ctrl-C typed in it (raw mode)
		// goes to the same escalation as the terminal's own (interrupt.go).
		Interactive: o.Interactive && len(sel) > 1 && o.ev == nil,
		In:          o.Stdin,
		LogPath:     func(n int) string { return viewLog(o.Root, n) },
		LogLabel: func(n int) string {
			return filepath.ToSlash(filepath.Join(".swim", "logs", filepath.Base(viewLog(o.Root, n))))
		},
		OnInterrupt: func() { ir.handle(syscall.SIGINT) },
		OnPre:       onPre,
		OnPause:     func(n int) string { return togglePause(o, sel, n) },
		NoHold:      o.Unattended,
	}, views)
	pre.disp = disp
	if o.secrets != nil && o.secrets.note != "" {
		disp.Message(o.secrets.note)
	}
	ir = newInterrupter(o, disp, sl, lt)
	defer ir.stop()
	// Lanes this run leaves out may be running in another swim right now.
	stopOthers := watchOtherRuns(o, selected, disp)
	if o.CheckOnly && o.CheckTimeout > 0 {
		t := time.AfterFunc(o.CheckTimeout, func() {
			disp.Message(fmt.Sprintf("swim: the check's --timeout %s ran out", o.CheckTimeout))
			ir.handle(syscall.SIGINT)
			ir.handle(syscall.SIGINT)
		})
		defer t.Stop()
	}
	// The approval broker always runs: every guard asks the launcher, which
	// decides from what it saw as the run started, or asks the operator
	// when the display can (or approves with -y). With no one to ask, a
	// guard nothing else approves is a dry run.
	var ask approval.Asker
	prompt := o.Prompt && o.ev == nil && disp.CanAsk()
	if o.AutoApprove || prompt || o.Asker != nil {
		if o.AutoApprove {
			disp.Message("swim: -y: every guard whose flag isn't set is approved without asking (logged as by -y)")
		}
		ask = func(req approval.Request, cancel <-chan struct{}) approval.Response {
			// The operator's answer given ahead in the live view (pinned to
			// the manifest) comes before -y and instead of asking.
			if resp, ok := pre.use(o, req); ok {
				word := "pre-approved"
				if !resp.Approved {
					word = "pre-declined"
				}
				disp.Message(fmt.Sprintf("%s%s: %s (%s)", display.Prefix(req.Lane, disp.Color()), word, req.Desc, req.Flag))
				o.ev.emit(Event{Event: "approved", Lane: req.Lane, Label: req.Desc, Detail: req.Flag + " (" + word + ")", At: now()})
				return resp
			}
			if o.AutoApprove {
				disp.Message(fmt.Sprintf("%sapproved by -y: %s (%s)", display.Prefix(req.Lane, disp.Color()), req.Desc, req.Flag))
				o.ev.emit(Event{Event: "approved", Lane: req.Lane, Label: req.Desc, Detail: req.Flag + " (-y)", At: now()})
				return approval.Response{Approved: true, By: or(o.ApprovedBy, operator()), Auto: true, Via: or(o.ApprovedVia, "swim run -y")}
			}
			if !prompt {
				// No terminal: the caller's asker (the hub), which says
				// where the question waits.
				disp.Set(req.Lane, func(v *display.LaneView) { v.Approval = req.Desc })
				defer disp.Set(req.Lane, func(v *display.LaneView) { v.Approval = "" })
				return o.Asker(req, cancel)
			}
			disp.Set(req.Lane, func(v *display.LaneView) { v.Approval = req.Desc })
			defer disp.Set(req.Lane, func(v *display.LaneView) { v.Approval = "" })
			yes := disp.Ask(req.Lane, approval.Question(req), cancel)
			return approval.Response{Approved: yes, By: operator()}
		}
	}
	if err := runKey(&o); err != nil {
		return 2, err
	}
	authErr := ""
	if srv, err := startAuthority(&o, ask, disp); err != nil {
		// Lanes still run; their guards fail closed (swim _approve sees it
		// was started by swim run, without the channel), and the
		// attestation says why.
		authErr = err.Error()
		disp.Message("swim: approvals: " + authErr + "; every guard runs as a dry run")
	} else {
		defer srv.Close()
		ir.approvals = srv
		o.approvalSock = srv.Path()
		o.approvalSrv = srv
		o.secrets.attach(srv, o.auth, disp)
	}

	var (
		mu       sync.Mutex
		outcomes = map[int]*Outcome{}
		done     = map[int]chan struct{}{}
	)
	for _, n := range sel {
		done[n] = make(chan struct{})
	}

	// Lanes run in their own process groups, so Ctrl-C reaches only swim,
	// which decides what the lanes get (interrupt.go): the first press stops
	// them at the next step boundary, the second interrupts and then kills
	// them, the third kills them at once. Lanes not yet started are skipped,
	// and the summary still prints.
	if sigs.handOver(ir.handle) {
		fmt.Fprintln(o.Out, "swim: interrupted before any lane started; nothing ran")
		return 130, nil
	}

	status.UpdateFile(o.Root, o.Cfg.Lanes, func(f *status.File) error { f.LastRun = o.RunID; return nil })
	runDetail := "swim " + joinInts(sel)
	if cap > 0 {
		runDetail += fmt.Sprintf("  max_parallel=%d", cap)
	}
	history.Log(o.Root, history.Entry{Event: history.Run, Run: o.RunID, Detail: runDetail})
	o.ev.emit(Event{Event: "run", Run: o.RunID, Lanes: sel, MaxParallel: cap, At: now()})
	disp.Start()
	defer disp.Restore()
	if o.Heartbeat > 0 {
		stopBeat := o.beat.run(o, disp)
		defer stopBeat()
	}

	o.skipped = newStatusBatch(o.Root, o.Cfg.Lanes)
	var wg sync.WaitGroup
	for _, n := range sel {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			defer close(done[n])
			var out *Outcome
			if reason := blocked[n]; reason != "" {
				out = refuseBlocked(o, n, reason, disp)
			} else if why := refusedCheck[n]; why != "" {
				out = refuseCheck(o, n, why, disp)
			} else {
				out = runLane(o, n, sel, selected, deps[n], sl, lt, disp, done, outcomes, &mu, ir)
			}
			o.ev.laneDone(o, out)
			mu.Lock()
			outcomes[n] = out
			mu.Unlock()
		}(n)
	}
	wg.Wait()
	o.skipped.flush()
	stopOthers()
	disp.Stop()
	if !o.CheckOnly {
		if authErr != "" {
			attestUnavailable(o, sel, authErr)
		} else {
			o.auth.attest(o, sel)
		}
	}
	if unused := pre.unused(); len(unused) > 0 {
		fmt.Fprintf(summaryOut, "swim: unused pre-answers (the lane never reached a guard, or its script changed): %s\n", strings.Join(unused, ", "))
	}

	// The run passes when every lane passed. One where nothing failed but
	// a guarded step dry-ran exits ExitDryRun: work is waiting on an
	// approval.
	code, dry := 0, false
	counts := map[string]int{}
	for _, n := range sel {
		st := outcomes[n].State
		counts[st]++
		switch {
		case st == status.Passed:
		case st == status.Skipped && outcomes[n].DryRun:
			dry = true
		default:
			code = 1
		}
	}
	if code == 0 && dry {
		code = ExitDryRun
	}
	runDone := fmt.Sprintf("swim %s  passed=%d failed=%d skipped=%d interrupted=%d", joinInts(sel), counts[status.Passed], counts[status.Failed], counts[status.Skipped], counts[status.Interrupted])
	history.Log(o.Root, history.Entry{Event: history.RunDone, Run: o.RunID, Detail: runDone + "  " + display.Elapsed(time.Since(start))})
	elapsed := time.Since(start)
	if err := writeRunRecord(o, start, cap, sel, selected, deps, outcomes); err != nil {
		fmt.Fprintln(os.Stderr, "swim: run record: "+err.Error())
	} else if !o.CheckOnly {
		// The run's observations (swim observe): derived, kept beside the
		// logs; one that can't be computed never fails the run.
		if _, err := observe.Record(o.Root, o.RunID, o.Cfg.ObserveThresholds()); err != nil {
			history.Log(o.Root, history.Entry{Event: history.Warn, Run: o.RunID, Detail: "observations not recorded: " + err.Error()})
		}
	}
	printSummary(summaryOut, o.Root, sel, outcomes, start, elapsed, disp.Color())
	stopped := ir.stopping()
	o.ev.summary(o, sel, outcomes, code, stopped, elapsed)
	if o.Finished != nil {
		o.Finished(Finish{Code: code, Interrupted: stopped, Counts: counts, Elapsed: elapsed})
	}
	return code, nil
}

func runLane(o Options, n int, sel []int, selected map[int]bool, deps []Dep, sl *slots, lt *lockTable, disp *display.Display,
	done map[int]chan struct{}, outcomes map[int]*Outcome, mu *sync.Mutex, ir *interrupter) *Outcome {

	var ready, locked, slotted time.Time
	stamp := func(oc *Outcome) *Outcome {
		oc.Ready, oc.Locked, oc.Slot = ready, locked, slotted
		if oc.End.IsZero() {
			oc.End = time.Now()
		}
		return oc
	}
	skip := func(reason string) *Outcome {
		disp.Set(n, func(v *display.LaneView) { v.State, v.Reason, v.WaitingOn = display.Skipped, reason, nil })
		info, _ := lane.ReadScript(o.Root, n)
		finished := status.Str(status.Now())
		o.skipped.update(n, func(l *status.Lane) {
			// Record which round was skipped: a later plan or retry must see
			// this job as attempted, not new. Counts from an older round go.
			l.ResetRun()
			l.State, l.Reason = status.Skipped, reason
			l.Round, l.Job, l.Run = info.Round, info.Job, o.RunID
			l.FinishedAt = finished
		})
		history.Log(o.Root, history.Entry{Event: history.Skip, Lane: n, Job: info.Job, Run: o.RunID, Detail: reason + "  " + info.Round})
		return stamp(&Outcome{N: n, Job: info.Job, Round: info.Round, State: status.Skipped, Exit: -1, Reason: reason})
	}
	// Its dependencies: those outside this run must already be satisfied
	// (outsideBlocker); those inside it are waited for (sched.go).
	var last *Outcome // the dependency waited on last: the one a skip is for
	dw := depWaiter{
		selected: selected,
		wait: func(d int) *Outcome {
			<-done[d]
			mu.Lock()
			defer mu.Unlock()
			last = outcomes[d]
			return last
		},
		outside: func(d Dep) string { return outsideBlocker(o.Root, d) },
		waiting: func(remaining []int) {
			disp.Set(n, func(v *display.LaneView) { v.State, v.WaitingOn = display.Waiting, remaining })
			status.Update(o.Root, n, o.Cfg.Lanes, func(l *status.Lane) {
				l.State, l.WaitingOn, l.Reason, l.PID, l.Host, l.Run = status.Waiting, remaining, "", os.Getpid(), proc.Host(), o.RunID
			})
			o.ev.emit(Event{Event: "waiting", Lane: n, On: remaining, At: now()})
		},
	}
	if why := dw.await(deps); why != "" {
		oc := skip(why)
		oc.DryRun = last != nil && last.DryRun
		return oc
	}
	ready = time.Now()
	if ir.stopsLane(n) {
		return skip("interrupted before start")
	}
	// Paused before it starts (swim pause N, or --all): it waits here,
	// holding no lock and no slot, until unpaused or the run stops.
	if waitPaused(o, n, disp, ir); ir.stopsLane(n) {
		return skip("interrupted before start")
	}
	// Take the round's resource locks (# Locks:), all at once: first from the
	// other lanes of this run, then across concurrent runs (lock files). A
	// lane waiting for a lock holds no slot.
	info := stagedInfo(o, n)
	var fileLocks lane.FileLocks
	laneLocks := info.Locks
	if len(laneLocks) > 0 {
		lockWait := func(text string) {
			disp.Set(n, func(v *display.LaneView) { v.State, v.LockWait, v.WaitingOn = display.Locked, text, nil })
			status.Update(o.Root, n, o.Cfg.Lanes, func(l *status.Lane) {
				l.State, l.WaitingOn, l.Reason, l.Run = status.Locked, []int{}, "waiting for lock "+text, o.RunID
			})
			history.Log(o.Root, history.Entry{Event: history.Locked, Lane: n, Job: info.Job, Run: o.RunID, Detail: "waiting for lock " + text})
			o.ev.emit(Event{Event: "locked", Lane: n, Lock: text, At: now()})
		}
		if !lt.acquire(n, laneLocks, func(name string, holder int) {
			if holder > 0 {
				lockWait(fmt.Sprintf("%s: swim %d", name, holder))
			} else {
				lockWait(name + ": queued behind another lane")
			}
		}) {
			return skip("interrupted before start")
		}
		defer lt.release(n, laneLocks)
		var ok bool
		var err error
		fileLocks, ok, err = lane.WaitFileLocks(o.Root, laneLocks, lane.LockHolder(n, info.Job, o.RunID),
			func(name, by string) { lockWait(fmt.Sprintf("%s: another run, %s", name, by)) },
			ir.stopping)
		if err != nil {
			disp.Line(n, "swim: lock files: "+err.Error())
			return skip("could not take locks: " + err.Error())
		}
		if !ok {
			return skip("interrupted before start")
		}
		defer fileLocks.Release()
		locked = time.Now()
		status.Update(o.Root, n, o.Cfg.Lanes, func(l *status.Lane) { l.Locks = append([]string(nil), laneLocks...) })
		defer status.Update(o.Root, n, o.Cfg.Lanes, func(l *status.Lane) { l.Locks = nil })
	}

	// What a queued batch pinned must be what was staged (checked again,
	// with the staged files, right before exec).
	if why, refuse := pinned(o, n); why != "" {
		if refuse {
			return stamp(refuseBlocked(o, n, why, disp))
		}
		return skip(why)
	}
	// A lane named by job id runs only if its script still holds that job.
	if pin := o.Pins[n]; pin != "" {
		if now, _ := lane.ReadScript(o.Root, n); !lane.MatchJob(now.Job, pin) {
			return skip(fmt.Sprintf("%s was rewritten: it holds job %s, not the pinned %s", lane.ScriptName(o.Root, n), or(lane.ShortJob(now.Job), "none"), lane.ShortJob(pin)))
		}
	}

	// The round's caches (# Cache:), held while it runs; its process
	// inherits the hold with its lock files.
	held, cerr := holdCaches(o, n, info)
	if cerr != nil {
		return stamp(refuseStart(o, n, cerr, disp))
	}
	defer held.Release()
	fileLocks = append(fileLocks, held...)

	// Wait for a free slot (max_parallel); queued lanes don't hold one.
	loggedQueue := false
	if !sl.acquire(n, func(ahead int) {
		disp.Set(n, func(v *display.LaneView) { v.State, v.QueuePos, v.WaitingOn = display.Queued, ahead, nil })
		status.Update(o.Root, n, o.Cfg.Lanes, func(l *status.Lane) {
			l.State, l.WaitingOn, l.Reason, l.Run = status.Queued, []int{}, fmt.Sprintf("%d ahead", ahead), o.RunID
		})
		if !loggedQueue {
			loggedQueue = true
			info, _ := lane.ReadScript(o.Root, n)
			history.Log(o.Root, history.Entry{Event: history.Queued, Lane: n, Job: info.Job, Run: o.RunID, Detail: fmt.Sprintf("%d ahead", ahead)})
			o.ev.emit(Event{Event: "queued", Lane: n, Ahead: intp(ahead), At: now()})
		}
	}) {
		return skip("interrupted before start")
	} else {
		defer sl.release()
	}
	slotted = time.Now()

	// How the script runs: bash for lane.N.sh, its #! line, a confirmed
	// runner, or itself; a script that doesn't load the bash library runs
	// inside swim's wrapper, which opens and closes its round.
	plan, perr := runner.Resolve(o.Cfg, o.manifests[n].Script())
	if perr != nil {
		return stamp(refuseStart(o, n, perr, disp))
	}
	// The last check of what runs: the staged files and every pin, after
	// the waiting is over.
	if why, refuse := pinned(o, n); why != "" {
		if refuse {
			return stamp(refuseBlocked(o, n, why, disp))
		}
		return skip(why)
	}
	cmd, ownGroup, w, tok := laneCommand(o, n, sel, plan, "", approval.Caller{Job: info.Job}, disp, fileLocks)
	defer exited(o, tok)

	// The last moment to honour a Ctrl-C that came while this lane was
	// getting ready (runner, env, slot): after this it starts, and gets the
	// stop request as a running lane.
	if ir.stopsLane(n) {
		return skip("interrupted before start")
	}
	releaseClaim(o, n, disp)
	launched := time.Now()
	launchedTS := launched.UTC().Format(time.RFC3339)
	disp.Set(n, func(v *display.LaneView) {
		v.State, v.WaitingOn, v.Started, v.Round, v.Job = display.Running, nil, launched, info.Round, info.Job
	})
	if err := cmd.Start(); err != nil {
		disp.Line(n, "swim: cannot start lane script: "+err.Error())
		oc := finish(o, n, disp, launched, launchedTS, 127, []string{"swim: cannot start lane script: " + err.Error()})
		oc.Job, oc.Round, oc.Start = info.Job, info.Round, launched
		return stamp(oc)
	}
	started(o, tok, cmd.Process.Pid)
	o.ev.emit(Event{Event: "start", Lane: n, Job: info.Job, Round: info.Round, At: launchedTS})
	ir.add(n, &laneProc{proc: cmd.Process, group: ownGroup, started: launched})
	o.beat.begin(n, launched)
	stopMsgs := watchMsgs(o, n, disp)
	err := waitLane(cmd)
	stopMsgs()
	w.Flush()
	o.beat.end(n)
	ir.remove(n)

	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				code = 128 + int(ws.Signal())
			}
		} else {
			code = 1
		}
	}
	end := time.Now()
	oc := finish(o, n, disp, launched, launchedTS, code, w.printed)
	oc.Job, oc.Round, oc.Start, oc.End = info.Job, info.Round, launched, end
	return stamp(oc)
}

// finish records how lane n's process ended: a round the script ended
// itself is already recorded; one that exited before lane_init's round
// header gets its reason, from the last line it printed (printed), in
// status and a round of its own in the log.
func finish(o Options, n int, disp *display.Display, started time.Time, startedTS string, code int, printed []string) *Outcome {
	state := status.Passed
	switch {
	case code == 130 || code == 143 || code == 129 || code == 137:
		state = status.Interrupted
	case code != 0:
		state = status.Failed
	}
	// A round that exited 0 may still have only dry-run its guarded steps:
	// its summary recorded that as skipped, and so does the outcome.
	reason := ""
	if state == status.Passed {
		if st, _ := status.Load(o.Root); st != nil {
			if l := st.Get(n); l != nil && l.State == status.Skipped && l.Run == o.RunID {
				state, reason = status.Skipped, l.Reason
			}
		}
	}
	now := time.Now()
	disp.Set(n, func(v *display.LaneView) {
		v.Exit, v.Finished = code, now
		switch state {
		case status.Passed:
			v.State = display.Passed
		case status.Skipped:
			v.State, v.Reason = display.Skipped, reason
		case status.Interrupted:
			v.State = display.Interrupted
		default:
			v.State = display.Failed
		}
	})
	// The lane script records its own final state. If it died before
	// lane_init (syntax error, missing swim), record it here instead.
	why := notStartedWhy(code, printed)
	died := false
	status.Update(o.Root, n, o.Cfg.Lanes, func(l *status.Lane) {
		if l.StartedAt != nil && *l.StartedAt >= startedTS {
			return
		}
		died = true
		l.ResetRun()
		l.State = state
		l.StartedAt = status.Str(startedTS)
		l.FinishedAt = status.Str(status.Now())
		l.DurationS = status.Float(now.Sub(started).Round(100 * time.Millisecond).Seconds())
		l.ExitCode = status.Int(code)
		l.Reason = "not started: " + why
		l.FailedSteps = []string{logparse.ResultLine(logparse.Stop, "not started: "+why, "")}
	})
	if !died && code > 128 {
		closeKilledRound(o, n, code)
	}
	if died {
		info, _ := lane.ReadScript(o.Root, n)
		NotStarted(o.Root, n, lane.Log(o.Root, n), info.Job, info.Round, o.RunID, why, printed, code)
	}
	return &Outcome{N: n, State: state, Exit: code, Reason: reason, DryRun: reason != "", Elapsed: now.Sub(started)}
}

// notStartedWhy is the reason for a lane process that exited (code) before
// its round started: the last line it printed says most.
func notStartedWhy(code int, printed []string) string {
	why := fmt.Sprintf("the lane script exited before lane_init (exit %d)", code)
	if l := lastLine(printed); l != "" {
		why += ": " + l
	}
	return why
}

// lineWriter splits a byte stream into lines for the display.
type lineWriter struct {
	buf  []byte
	emit func(string)
	// printed are the last lines the lane printed, as shown (masked), for
	// a round that never starts.
	printed []string
}

// keep records a line the lane printed, keeping the last printedKept.
func (w *lineWriter) keep(s string) {
	w.printed = append(w.printed, s)
	if len(w.printed) > printedKept {
		w.printed = w.printed[len(w.printed)-printedKept:]
	}
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := indexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.emit(strings.TrimRight(string(w.buf[:i]), "\r"))
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

// Flush emits any trailing partial line.
func (w *lineWriter) Flush() {
	if len(w.buf) > 0 {
		w.emit(strings.TrimRight(string(w.buf), "\r"))
		w.buf = nil
	}
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

func printSummary(out io.Writer, root string, sel []int, outcomes map[int]*Outcome, start time.Time, total time.Duration, color bool) {
	p := ui.Painter{On: color}
	fmt.Fprintln(out)
	fmt.Fprintln(out, p.Paint(ui.Bold, "swim summary · "+display.Span(start, start.Add(total))))
	fmt.Fprintln(out, p.Paint(ui.Dim, fmt.Sprintf("  %-8s %-8s %-12s %4s %5s %5s %5s %6s %7s", "lane", "job", "result", "exit", "pass", "fail", "skip", "drift", "time")))
	for _, n := range sel {
		oc := outcomes[n]
		label := p.Paint(ui.LaneColor(n)+ui.Bold, fmt.Sprintf("%-8s", fmt.Sprintf("swim %d", n)))
		r, _ := logparse.ParseFile(lane.Log(root, n))
		job := oc.Job
		if job == "" && (oc.State != status.Skipped || oc.Exit >= 0) {
			job = r.Job
		}
		if job == "" {
			job = "-"
		}
		label += " " + p.Paint(ui.Dim, fmt.Sprintf("%-8s", lane.ShortJob(job)))
		if oc.Blocked { // refused before starting: no round of its own in the log
			fmt.Fprintf(out, "  %s %s %4d %5s %5s %5s %6s %7s\n", label, p.Paint(ui.Red, fmt.Sprintf("%-12s", "FAIL")), oc.Exit, "-", "-", "-", "-", "-")
			fmt.Fprintf(out, "  %-17s %s\n", "", p.Paint(ui.Red, oc.Reason))
			continue
		}
		result := strings.ToUpper(oc.State)
		if oc.State == status.Passed {
			result = "PASS"
		} else if oc.State == status.Failed {
			result = "FAIL"
		} else if oc.State == status.Skipped {
			result = "SKIP"
		}
		res := p.Paint(ui.StateColor(result), fmt.Sprintf("%-12s", result))
		if oc.State == status.Skipped && oc.Exit < 0 { // never started
			fmt.Fprintf(out, "  %s %s %4s %5s %5s %5s %6s %7s  %s\n", label, res, "-", "-", "-", "-", "-", "-", oc.Reason)
			continue
		}
		fmt.Fprintf(out, "  %s %s %4d %5d %5d %5d %6d %7s\n", label, res, oc.Exit, r.Pass, r.Fail, r.Skip, r.Drift, display.Elapsed(oc.Elapsed))
		if oc.State == status.Skipped {
			fmt.Fprintf(out, "  %-17s %s\n", "", p.Paint(ui.Yellow, oc.Reason))
		}
		for _, f := range r.Failed() {
			fmt.Fprintf(out, "  %-17s %s\n", "", p.Paint(ui.Red, f.Text()))
		}
	}
	fmt.Fprintln(out, p.Paint(ui.Dim, "  logs: swim log N (or "+logList(sel)+")   status: swim status"))
}

func logList(sel []int) string {
	parts := make([]string, len(sel))
	for i, n := range sel {
		parts[i] = fmt.Sprintf(".swim/logs/agent%d.log", n)
	}
	return strings.Join(parts, " ")
}

// joinInts joins lane numbers with sep (default ",").
func joinInts(ns []int, sep ...string) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = fmt.Sprint(n)
	}
	s := ","
	if len(sep) > 0 {
		s = sep[0]
	}
	return strings.Join(parts, s)
}

// writeRunRecord saves when each lane became ready, started and finished,
// and the dependencies this run resolved, as they were even after its
// scripts change.
func writeRunRecord(o Options, start time.Time, cap int, sel []int, selected map[int]bool, deps map[int][]Dep, outcomes map[int]*Outcome) error {
	r := &runrec.Run{Run: o.RunID, Started: start.UTC(), Finished: time.Now().UTC(), MaxParallel: cap}
	for _, n := range sel {
		oc := outcomes[n]
		l := runrec.Lane{Lane: n, Job: oc.Job, Round: oc.Round, State: oc.State, Reason: oc.Reason, After: []int{}}
		for _, d := range deps[n] {
			if selected[d.Lane] {
				l.After = append(l.After, d.Lane)
			}
		}
		if info, err := lane.ReadScript(o.Root, n); err == nil {
			l.Locks = info.Locks
		}
		if st, _ := status.Load(o.Root); st != nil {
			if s := st.Get(n); s != nil && s.Run == o.RunID {
				l.Paused = s.PausedS
				for _, p := range s.Pauses {
					if at, err := time.Parse(time.RFC3339Nano, p.At); err == nil {
						l.Pauses = append(l.Pauses, runrec.Span{Start: *runrec.Since(start, at), Dur: p.S})
					}
				}
			}
		}
		if oc.Exit >= 0 {
			e := oc.Exit
			l.Exit = &e
		}
		l.Ready = runrec.Since(start, oc.Ready)
		l.Locked = runrec.Since(start, oc.Locked)
		l.Slot = runrec.Since(start, oc.Slot)
		l.Start = runrec.Since(start, oc.Start)
		l.End = runrec.Since(start, oc.End)
		r.Lanes = append(r.Lanes, l)
	}
	return runrec.Write(o.Root, r)
}

// closeKilledRound finishes the round of a lane killed by a signal (SIGKILL
// is a force quit's last resort): its bash never ran _finish, so the step
// output in flight is recovered and the round's END and final state are
// recorded. A lane that ended its round itself is left alone.
func closeKilledRound(o Options, n, code int) {
	if unlock, ok := reapLock(o.Root); ok {
		defer unlock()
	}
	st, _ := status.Load(o.Root)
	if st == nil || st.Get(n) == nil || st.Get(n).State != status.Running {
		return
	}
	cmd := exec.Command(o.Self, "_finish", fmt.Sprint(n), "--exit", fmt.Sprint(code))
	cmd.Dir = o.Root
	cmd.Env = append(os.Environ(), "SWIM_COLOR=0")
	if o.approvalSrv != nil {
		// The launcher reports for the lane: a token of its own for lane n
		// (no process tree to check: swim runs it itself).
		tok := o.approvalSrv.Register(approval.Caller{Lane: n, Run: o.RunID})
		defer o.approvalSrv.Revoke(tok)
		cmd.Env = append(cmd.Env, "SWIM_LAUNCHED=1", approval.EnvVar+"="+o.approvalSock, approval.TokenEnv+"="+tok)
	}
	cmd.Run()
}

// operator is who answers approvals at this terminal.
func operator() string {
	for _, k := range []string{"SWIM_USER", "USER", "LOGNAME"} {
		if u := os.Getenv(k); u != "" {
			return u
		}
	}
	return "operator"
}

// laneCommand builds lane n's process: its runner (inside swim's wrapper
// for scripts that don't load the bash library), its environment (env
// files under the shell's, the run's SWIM_* variables), stdin, process
// group and output.
func laneCommand(o Options, n int, sel []int, plan runner.Plan, job string, who approval.Caller, disp *display.Display, fileLocks lane.FileLocks) (*exec.Cmd, bool, *lineWriter, string) {
	argv := plan.Argv
	if plan.Wrapped {
		argv = append([]string{o.Self, "_wrap", strconv.Itoa(n), "--"}, argv...)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = o.Root
	if m := o.manifests[n]; m != nil {
		// It runs in its staged copy: relative paths read what was pinned.
		cmd.Dir = m.Workspace(o.Root)
	}
	// The env files' values go under the shell's: what the operator typed
	// wins. Keys the shell doesn't override are masked whatever their names.
	var fileKeys []string
	env := []string{}
	if r := o.env[n]; r != nil {
		for _, v := range r.Vars {
			if _, set := os.LookupEnv(v.Key); !set {
				fileKeys = append(fileKeys, v.Key)
			}
		}
		env = append(env, r.Env()...)
	}
	env = append(env, os.Environ()...)
	env = append(env, config.EnvKeysVar+"="+strings.Join(fileKeys, " "))
	env = append(env, "SWIM_BIN="+o.Self, "SWIM_LAUNCHED=1",
		"SWIM_RUN="+o.RunID, "SWIM_RUN_LANES="+joinInts(sel, " "), approval.EnvVar+"="+o.approvalSock)
	if disp.Color() {
		env = append(env, "SWIM_COLOR=1")
	} else {
		env = append(env, "SWIM_COLOR=0")
	}
	env = append(env, cacheEnv(o, n)...) // # Cache: (cache.go)
	staged, tok := stageEnv(o, n, who)
	env = append(env, staged...)
	if o.secrets != nil {
		env = append(o.secrets.stripGuarded(env), o.secrets.env(o, n)...)
	}
	cmd.Env = env
	// Parallel lanes can't share a keyboard: give them no stdin so
	// fail-closed prompts stop instead of hanging. A lone lane may prompt.
	if len(sel) == 1 && o.Stdin != nil {
		cmd.Stdin = o.Stdin
	}
	// Its own process group, so the terminal's Ctrl-C reaches only swim.
	// Not for a lane reading the terminal: a background group that reads
	// it is stopped (SIGTTIN). That lane gets Ctrl-C from the terminal
	// directly, at once, as before.
	ownGroup := cmd.Stdin == nil || !ui.IsTTY(o.Stdin)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: ownGroup}
	// The launcher holds the lane's lock files (# Locks:, caches) until the
	// lane ends. They aren't passed to the lane: anything it left running
	// would hold them for good.
	_ = fileLocks
	// The launcher holds every secret of the run: it masks them all itself
	// (asking its own broker would be asking as the lane).
	mk := o.Cfg.MaskerWith(env, o.secrets.labeled())
	w := &lineWriter{}
	w.emit = func(s string) {
		o.beat.saw(n)
		w.keep(mk.String(s))
		s = mk.String(s)
		disp.JobLine(n, job, s)
		if job != "" {
			s = "[" + job + "] " + s // the YAML stream says which job too
		}
		if o.ev.wantsOutput() {
			o.ev.emit(Event{Event: "output", Lane: n, Line: &s})
		}
	}
	cmd.Stdout, cmd.Stderr = w, w
	// A lane that leaves a process behind holding its output (a daemon it
	// started) mustn't hold up the run once the lane itself has exited.
	cmd.WaitDelay = 2 * time.Second
	return cmd, ownGroup, w, tok
}

// exitCode is a finished process's exit code (128+signal if a signal
// killed it).
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	}
	return 1
}

// watchMsgs follows lane n's status message (swim msg) while it runs: the
// panel shows it and the YAML stream gets a msg event for each change. The
// returned func stops it and clears the panel's message.
func watchMsgs(o Options, n int, disp *display.Display) func() {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		last, lastPaused := "", ""
		for first := true; ; first = false {
			if !first {
				select {
				case <-done:
					return
				case <-t.C:
				}
			}
			// swim pause: the row says paused while the round holds.
			if p := pausedText(o, n); p != lastPaused {
				lastPaused = p
				disp.Set(n, func(v *display.LaneView) { v.Paused = p })
			}
			m, ok := lane.ReadMsg(o.Root, n)
			if ok && m.Run != o.RunID {
				ok = false // an earlier round's
			}
			cur := ""
			if ok {
				cur = m.Display()
			}
			if cur == last {
				continue
			}
			last = cur
			disp.Set(n, func(v *display.LaneView) { v.Message = cur })
			if cur != "" {
				disp.Line(n, "… "+cur)
			}
			text := m.Text
			o.ev.emit(Event{Event: "msg", Lane: n, Message: &text, Progress: m.Progress, At: now()})
		}
	}()
	return func() {
		close(done)
		<-finished
		disp.Set(n, func(v *display.LaneView) { v.Message, v.Paused = "", "" })
	}
}

// togglePause is p (lane n) and P (n 0: every lane of this run) in the
// live view: it pauses running or waiting lanes before their next step, or
// lets paused ones go on (swim pause, swim unpause).
func togglePause(o Options, sel []int, n int) string {
	lanes := sel
	if n != 0 {
		lanes = []int{n}
	}
	paused := 0
	for _, l := range lanes {
		if _, ok := lane.LanePause(o.Root, l, o.RunID); ok {
			paused++
		}
	}
	by := operator()
	if paused > 0 {
		for _, l := range lanes {
			if _, ok := lane.LanePause(o.Root, l, o.RunID); ok {
				os.WriteFile(lane.ResumedPath(o.Root, l), []byte(by+"\n"), 0o644)
				lane.ClearPause(o.Root, l)
				history.Log(o.Root, history.Entry{Event: history.Unpause, Lane: l, Run: o.RunID, Detail: "by " + by + " (live view)"})
			}
		}
		if n != 0 {
			return fmt.Sprintf("swim %d: resumed", n)
		}
		return fmt.Sprintf("resumed %d paused lanes", paused)
	}
	st, _ := status.Load(o.Root)
	done := 0
	for _, l := range lanes {
		s := (*status.Lane)(nil)
		if st != nil {
			s = st.Get(l)
		}
		if s == nil || s.Run != o.RunID {
			continue // not started in this run, or already over
		}
		switch s.State {
		case status.Running, status.Waiting, status.Queued, status.Locked:
		default:
			continue
		}
		if lane.RequestPause(o.Root, l, lane.PauseReq{Run: o.RunID, By: by}) == nil {
			history.Log(o.Root, history.Entry{Event: history.Pause, Lane: l, Job: s.Job, Run: o.RunID, Detail: fmt.Sprintf("after %q  (live view)", s.CurrentStep)})
			done++
		}
	}
	switch {
	case done == 0 && n != 0:
		return fmt.Sprintf("swim %d isn't running", n)
	case done == 0:
		return "no lane of this run is running"
	case n != 0:
		return fmt.Sprintf("swim %d: pausing before its next step (p again lets it go on)", n)
	}
	return fmt.Sprintf("pausing %d lanes before their next step (P again lets them go on)", done)
}

// releaseClaim spends lane n's claim (swim claim) as its round starts:
// running-lane protection takes over. Another session's claim is no bar
// (the operator decides), but the run says so.
func releaseClaim(o Options, n int, disp *display.Display) {
	c := lane.ReadClaim(o.Root, n)
	if c == nil {
		return
	}
	if c.Session != lane.SessionID() {
		disp.Line(n, fmt.Sprintf("swim: swim %d was claimed by %s (session %s) until %s: it may still have been being written; running it released the claim",
			n, c.Owner, c.Session, c.ExpiresAt().Local().Format("15:04")))
	}
	lane.DropClaim(o.Root, n)
}

// pausedText is what lane n's row says while its round holds (swim
// pause): "after <step>", or "" when it isn't holding. Only a pause
// request costs a read of status.yml.
func pausedText(o Options, n int) string {
	if _, ok := lane.Paused(o.Root, n, o.RunID); !ok {
		return ""
	}
	st, _ := status.Load(o.Root)
	if st == nil {
		return ""
	}
	l := st.Get(n)
	if l == nil || l.PausedAt == "" {
		return ""
	}
	if l.PausedAfter == "" {
		return "(swim unpause " + strconv.Itoa(n) + ")"
	}
	return fmt.Sprintf("after %s (swim unpause %d)", l.PausedAfter, n)
}

// waitPaused holds lane n before it starts while a pause holds it, until
// it's lifted or the run stops for it.
func waitPaused(o Options, n int, disp *display.Display, ir *interrupter) {
	req, ok := lane.Paused(o.Root, n, o.RunID)
	if !ok {
		return
	}
	text := fmt.Sprintf("before it starts (swim unpause %d)", n)
	disp.Set(n, func(v *display.LaneView) { v.Paused = text })
	status.Update(o.Root, n, o.Cfg.Lanes, func(l *status.Lane) {
		l.PausedAt, l.PausedBy, l.PauseNote, l.NextStep = status.Now(), req.By, req.Note, "(its first step)"
	})
	for !ir.stopsLane(n) {
		if _, ok := lane.Paused(o.Root, n, o.RunID); !ok {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	disp.Set(n, func(v *display.LaneView) { v.Paused = "" })
	status.Update(o.Root, n, o.Cfg.Lanes, func(l *status.Lane) {
		l.PausedAt, l.PausedBy, l.PauseNote, l.NextStep = "", "", "", ""
	})
}

// watchOtherRuns keeps the panel's rows for lanes this run doesn't include
// in step with status.yml: a lane another swim in the repo (a concurrent
// swim run, swim watch running a queued batch) is running shows as running
// elsewhere, with its round and elapsed time, and its result once it ends.
// The returned func stops it.
func watchOtherRuns(o Options, selected map[int]bool, disp *display.Display) func() {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(time.Second)
		defer t.Stop()
		elsewhere := map[int]bool{}
		for first := true; ; first = false {
			if !first {
				select {
				case <-done:
					return
				case <-t.C:
				}
			}
			st, err := status.Load(o.Root)
			if err != nil || st == nil {
				continue
			}
			for n := 1; n <= o.Cfg.Lanes; n++ {
				if selected[n] {
					continue
				}
				l := st.Get(n)
				if l == nil {
					continue
				}
				active := l.Run != o.RunID && l.Alive() &&
					(l.State == status.Running || l.State == status.Waiting || l.State == status.Queued || l.State == status.Locked)
				switch {
				case active:
					started := time.Now()
					if l.StartedAt != nil {
						if ts, err := time.Parse(time.RFC3339, *l.StartedAt); err == nil {
							started = ts
						}
					}
					elsewhere[n] = true
					round, job := l.Round, l.Job
					disp.Set(n, func(v *display.LaneView) {
						v.State, v.Started, v.Round, v.Job = display.Elsewhere, started, round, job
					})
				case elsewhere[n]:
					// It ended over there: show how.
					delete(elsewhere, n)
					state, exit := display.Idle, 0
					switch l.State {
					case status.Passed:
						state = display.Passed
					case status.Failed:
						state = display.Failed
					case status.Interrupted:
						state = display.Interrupted
					case status.Skipped:
						state = display.Skipped
					}
					if l.ExitCode != nil {
						exit = *l.ExitCode
					}
					fin := time.Now()
					if l.FinishedAt != nil {
						if ts, err := time.Parse(time.RFC3339, *l.FinishedAt); err == nil {
							fin = ts
						}
					}
					disp.Set(n, func(v *display.LaneView) { v.State, v.Exit, v.Finished = state, exit, fin })
				}
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

// preAnswers are the operator's answers, given ahead in the live view, to
// lanes' guards: per lane, approve or decline, pinned to the script it was
// given for (a changed script asks again), for this run only.
type preAnswers struct {
	mu       sync.Mutex
	selected map[int]bool
	by       map[int]*preAnswer
	disp     *display.Display
}

type preAnswer struct {
	approve bool
	sha     string
	at      string
	used    bool
}

func newPreAnswers(selected map[int]bool) *preAnswers {
	return &preAnswers{selected: selected, by: map[int]*preAnswer{}}
}

// manifestSum is the manifest lane n runs in this run ("" if none).
func manifestSum(o Options, n int) string {
	if m := o.manifests[n]; m != nil {
		return m.SHA256
	}
	return ""
}

// runningJob is the job id lane n runs in this run, from status, for a
// round whose script names none (its id is made as it starts).
func runningJob(o Options, n int) string {
	st, _ := status.Load(o.Root)
	if st == nil || st.Get(n) == nil {
		return ""
	}
	l := st.Get(n)
	if l.Run == o.RunID {
		return l.Job
	}
	return ""
}

// laneEnv is lane n's environment as its process gets it (env files under
// the shell), for its Observe: probes.
func laneEnv(o Options, n int) []string {
	var env []string
	if r := o.env[n]; r != nil {
		env = append(env, r.Env()...)
	}
	return append(env, os.Environ()...)
}

// set records (choice "approved" / "declined") or clears ("") lane n's
// pre-answer and says what happened.
func (p *preAnswers) set(o Options, n int, choice string) string {
	if !p.selected[n] {
		return fmt.Sprintf("swim %d isn't in this run", n)
	}
	info, _ := lane.ReadScript(o.Root, n)
	if st, _ := status.Load(o.Root); st != nil {
		if l := st.Get(n); l != nil && l.Run == o.RunID && (l.State == status.Passed || l.State == status.Failed || l.State == status.Skipped || l.State == status.Interrupted) {
			return fmt.Sprintf("swim %d already finished", n)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if choice == "" {
		delete(p.by, n)
		p.disp.Set(n, func(v *display.LaneView) { v.Pre = "" })
		return fmt.Sprintf("swim %d: pre-answer cleared; it will ask", n)
	}
	if len(info.Guards) == 0 {
		return fmt.Sprintf("swim %d has no guards to approve", n)
	}
	if prev := p.by[n]; prev != nil && prev.used {
		return fmt.Sprintf("swim %d already used its pre-answer", n)
	}
	p.by[n] = &preAnswer{approve: choice == "approved", sha: manifestSum(o, n), at: time.Now().Format("15:04:05")}
	p.disp.Set(n, func(v *display.LaneView) { v.Pre = choice })
	event, word := "preapproved", "pre-approved"
	if choice == "declined" {
		event, word = "predeclined", "pre-declined (dry runs)"
	}
	o.ev.emit(Event{Event: event, Lane: n, Detail: strings.Join(info.GuardFlags(), " "), At: now()})
	msg := fmt.Sprintf("swim %d: %s (%d guards)", n, word, len(info.Guards))
	if info.DynamicGuards > 0 {
		msg += fmt.Sprintf("; %d named at run time will still ask", info.DynamicGuards)
	}
	return msg
}

// use answers req from its lane's pre-answer, if it has one that covers
// req's guard and its script hasn't changed.
func (p *preAnswers) use(o Options, req approval.Request) (approval.Response, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a := p.by[req.Lane]
	if a == nil || req.EvidenceRequired {
		// An evidence-bound guard asks: an answer given before the plan
		// existed can't approve the plan.
		return approval.Response{}, false
	}
	known := false
	if m := o.manifests[req.Lane]; m != nil {
		for _, f := range m.Guards {
			known = known || f == req.Flag
		}
	}
	if !known || manifestSum(o, req.Lane) != a.sha {
		return approval.Response{}, false
	}
	a.used = true
	return approval.Response{Approved: a.approve, By: operator(), Pre: true, At: a.at}, true
}

// unused lists the lanes whose pre-answers were never used.
func (p *preAnswers) unused() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for n, a := range p.by {
		if !a.used {
			out = append(out, fmt.Sprintf("swim %d", n))
		}
	}
	sort.Strings(out)
	return out
}

// viewLog is the log the live view shows for lane n.
func viewLog(root string, n int) string { return lane.Log(root, n) }

// waitLane waits for a lane's process. A process it left behind holding
// its output is cut off after WaitDelay: the lane's own exit status counts,
// not exec.ErrWaitDelay.
func waitLane(cmd *exec.Cmd) error {
	err := cmd.Wait()
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		return nil
	}
	return err
}

// stagedInfo is lane n's header as staged (what was approved and will run),
// so an edit to the working tree while it waits can't change its locks;
// the working tree's if it wasn't staged.
func stagedInfo(o Options, n int) lane.Info {
	if m := o.manifests[n]; m != nil && m.Dir != "" {
		if info, err := lane.ReadScript(m.Dir, n); err == nil && info.Exists {
			return info
		}
	}
	info, _ := lane.ReadScript(o.Root, n)
	return info
}
