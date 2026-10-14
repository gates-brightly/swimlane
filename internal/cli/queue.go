package cli

import (
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/envfile"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/launcher"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/manifest"
	"github.com/gates-brightly/swimlane/internal/queue"
	"github.com/gates-brightly/swimlane/internal/sigfile"
	"github.com/gates-brightly/swimlane/internal/sign"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// cmdQueue manages queued batches: swim queue [list|add|show|rm|requeue].
// swim queue 1 2 3 is shorthand for swim queue add 1 2 3.
func cmdQueue(args []string) error {
	sub := "list"
	if len(args) > 0 {
		switch {
		case looksLikeLaneRef(args[0]):
			sub = "add"
		case !strings.HasPrefix(args[0], "-"):
			sub, args = args[0], args[1:]
		}
	}
	switch sub {
	case "list", "ls":
		return queueList(args)
	case "add":
		return queueAdd(args)
	case "show":
		return queueShow(args)
	case "rm", "remove":
		return queueRemove(args)
	case "requeue":
		return queueRequeue(args)
	}
	return usagef("usage: swim queue [list|add|show|rm|requeue] (swim queue --help)")
}

// queueAdd queues a batch: the named lanes (or every pending round that
// hasn't passed), pinned to the jobs they hold now, with approvals.
func queueAdd(args []string) error {
	var approve []string
	var note, parallel, keyPath, timeout string
	var yes, wait bool
	// --approve repeats; -y is the short --yes.
	var rest0 []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--approve" && i+1 < len(args):
			approve = append(approve, strings.Split(args[i+1], ",")...)
			i++
		case strings.HasPrefix(a, "--approve="):
			approve = append(approve, strings.Split(strings.TrimPrefix(a, "--approve="), ",")...)
		case a == "-y":
			yes = true
		default:
			rest0 = append(rest0, a)
		}
	}
	rest, err := flags{bools: map[string]*bool{"yes": &yes, "wait": &wait}, strs: map[string]*string{"note": &note, "parallel": &parallel, "key": &keyPath, "timeout": &timeout}}.parse(rest0)
	if err != nil {
		return err
	}
	limit, err := durationFlag("timeout", timeout, 0, "30m")
	if err != nil {
		return err
	}
	if limit > 0 {
		wait = true // --timeout bounds the wait, so it implies it
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	if err := refreshStatus(root, cfg); err != nil {
		return err
	}
	var lanes []int
	for _, a := range rest {
		n, err := laneRef(root, cfg, a, false)
		if err != nil {
			return err
		}
		lanes = append(lanes, n)
	}
	if len(lanes) == 0 {
		if lanes, _, err = launcher.Select(launcher.Options{Root: root, Cfg: cfg, DryRun: true}); err != nil {
			return err
		}
	}
	if len(lanes) == 0 {
		return fmt.Errorf("nothing to queue: no lane holds a pending round that hasn't passed (name lanes to queue them anyway)")
	}
	o := queueOpts{approve: approve, note: note, yes: yes, keyPath: keyPath}
	if parallel != "" {
		n, err := strconv.Atoi(parallel)
		if err != nil || n < 0 {
			return usagef("--parallel needs 0 (unlimited) or a positive number, got %q", parallel)
		}
		o.parallel = &n
	}
	b, err := addBatch(root, cfg, lanes, o)
	if err != nil {
		return err
	}
	if b.warn != "" {
		fmt.Fprintln(os.Stderr, "swim queue: "+b.warn)
	}
	e, guards, signedAs := b.entry, b.guards, b.signedAs
	seen := map[string]bool{}
	for _, f := range e.Approve {
		seen[f] = true
	}
	fmt.Printf("queued %s (#%d): %d lanes\n", e.ID, e.Seq, len(e.Lanes))
	if signedAs != "" {
		fmt.Printf("  signed by %s (standing approvals for %s apply)\n", signedAs, signedAs)
	} else {
		fmt.Println("  unsigned (no signing key): standing approvals don't apply")
	}
	for _, l := range e.Lanes {
		fmt.Printf("  swim %-2d %s  %s\n", l.Lane, lane.ShortJob(l.Job), l.Round)
	}
	var unapproved []string
	for g := range guards {
		if !e.ApproveAll && !seen[g] {
			unapproved = append(unapproved, g)
		}
	}
	sort.Strings(unapproved)
	switch {
	case e.ApproveAll:
		fmt.Println("  approvals: every guard (-y)")
	case len(e.Approve) > 0:
		fmt.Println("  approvals: " + strings.Join(e.Approve, ", "))
	}
	if len(unapproved) > 0 {
		fmt.Println("  not approved (dry runs; a queued batch never asks): " + strings.Join(unapproved, ", "))
	}
	if why := launcher.Busy(root, cfg); why != "" {
		fmt.Printf("  runs when swim is idle (now: %s) and a worker is up: swim watch\n", why)
	} else {
		fmt.Println("  runs when a worker takes it: swim watch (or swim watch --once)")
	}
	if !wait {
		return nil
	}
	if err := waitIdle(root, pollEvery, limit); err != nil {
		// Timed out: the batch stays queued (or running) for a worker.
		if cur, ferr := queue.Find(root, e.ID); ferr == nil {
			return fmt.Errorf("%v; %s (#%d) is still %s", err, e.ID, e.Seq, cur.State)
		}
		return err
	}
	// Exit with the batch's result, like swim log --until-done.
	done, err := queue.Find(root, e.ID)
	if err != nil {
		return fmt.Errorf("%s: %v (removed before it ran?)", e.ID, err)
	}
	if done.State != queue.Done || done.Result == nil {
		return fmt.Errorf("%s is %s, not done", e.ID, done.State)
	}
	fmt.Printf("%s (#%d): %s\n", e.ID, e.Seq, done.Result.Result)
	if done.Result.Result != status.Passed {
		return exitError{1}
	}
	return nil
}

// queueOpts are what a batch holds besides its lanes (swim queue add's
// flags).
type queueOpts struct {
	approve  []string
	note     string
	yes      bool
	parallel *int
	keyPath  string
}

// batch is a batch just queued: its entry, the guards its lanes hold, who
// signed it ("" unsigned) and why it isn't signed, if a key failed.
type batch struct {
	entry    *queue.Entry
	guards   map[string]bool
	signedAs string
	warn     string
}

// addBatch queues lanes as one batch, pinned to the scripts and env they
// hold now, signed with the operator's key when there is one, and logs it.
func addBatch(root string, cfg *config.Config, lanes []int, o queueOpts) (batch, error) {
	e := &queue.Entry{By: operator(), Note: o.note, ApproveAll: o.yes, Parallel: o.parallel}
	b := batch{entry: e, guards: map[string]bool{}}
	for _, n := range lanes {
		l, info, err := pinLane(root, cfg, n)
		if err != nil {
			return b, err
		}
		e.Lanes = append(e.Lanes, l)
		for _, g := range info.Guards {
			b.guards[g.Flag] = true
		}
	}
	// An approval must name a guard in the batch: a typo would otherwise
	// approve nothing, silently.
	seen := map[string]bool{}
	for _, f := range o.approve {
		f = strings.TrimSpace(f)
		if f == "" || seen[f] {
			continue
		}
		if !b.guards[f] {
			return b, fmt.Errorf("--approve %s: no lane in this batch has a guard %s (its guards: %s)", f, f, or(strings.Join(sortedKeys(b.guards), ", "), "none"))
		}
		seen[f] = true
		e.Approve = append(e.Approve, f)
	}
	sort.Strings(e.Approve)
	// Sign the batch: it proves who queued it, so standing approvals for
	// that person (swim approve --auto) apply to it.
	e.ID = queue.NewID()
	if key, err := sign.LoadKey(or(o.keyPath, cfg.SigningKey)); err == nil && sign.Available() {
		if sig, err := sign.Sign(key, []byte(e.Statement())); err == nil {
			e.QueuedKey, e.QueueSig = key.Public, sig
			b.signedAs = sign.Fingerprint(key.Public)
			if lk, err := lookup.Load(root); err == nil {
				if u, ok := lk.Who(key.Public); ok {
					b.signedAs = u
					e.By = u
				}
			}
		} else {
			b.warn = "not signed (" + err.Error() + "); standing approvals won't apply to it"
		}
	} else if o.keyPath != "" {
		return b, err
	}
	if err := queue.Add(root, e); err != nil {
		return b, err
	}
	var ns []string
	for _, l := range e.Lanes {
		ns = append(ns, strconv.Itoa(l.Lane))
	}
	detail := fmt.Sprintf("%s: swim %s", e.ID, strings.Join(ns, ","))
	if len(e.Approve) > 0 {
		detail += "  approve=" + strings.Join(e.Approve, ",")
	}
	if e.ApproveAll {
		detail += "  approve=all (-y)"
	}
	if e.Note != "" {
		detail += "  " + e.Note
	}
	history.Log(root, history.Entry{Event: history.QueueAdd, Detail: detail})
	return b, nil
}

// pinLane is lane n as a queue entry holds it: its job and round, pinned
// to the script, env and manifest it has now.
func pinLane(root string, cfg *config.Config, n int) (queue.Lane, lane.Info, error) {
	info, err := lane.ReadScript(root, n)
	if err != nil {
		return queue.Lane{}, info, err
	}
	if !info.Pending() {
		return queue.Lane{}, info, fmt.Errorf("swim %d holds no pending round (a stub, or no Round: line); write one before queueing it", n)
	}
	data, err := lane.ScriptBytes(root, n)
	if err != nil {
		return queue.Lane{}, info, err
	}
	r, err := envfile.Check(root, cfg, n, info, nil)
	if err != nil {
		return queue.Lane{}, info, err
	}
	envSum := r.SHA256()
	m, err := manifest.Build(root, cfg, n, envSum)
	if err != nil {
		return queue.Lane{}, info, err
	}
	return queue.Lane{Lane: n, Job: info.Job, Round: info.Round, SHA256: chain.Hash(data), EnvSHA256: &envSum, Manifest: m.SHA256}, info, nil
}

// cmdWait blocks until swim is idle and nothing is about to start: no lane
// running, no swim run waiting on lanes, and no queued or running batch.
func cmdWait(args []string) error {
	var interval, timeout string
	rest, err := flags{strs: map[string]*string{"interval": &interval, "timeout": &timeout}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return usagef("usage: swim wait [--interval D] [--timeout D]")
	}
	every, err := durationFlag("interval", interval, pollEvery, "2s")
	if err != nil {
		return err
	}
	limit, err := durationFlag("timeout", timeout, 0, "30m")
	if err != nil {
		return err
	}
	root, _, err := repo()
	if err != nil {
		return err
	}
	return waitIdle(root, every, limit)
}

// pollEvery is how often swim wait and swim watch check, by default.
const pollEvery = 2 * time.Second

// durationFlag parses a --name duration flag's value v, or def when unset.
func durationFlag(name, v string, def time.Duration, example string) (time.Duration, error) {
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, usagef("--%s takes a duration like %s, got %q", name, example, v)
	}
	return d, nil
}

// sayer prints a dim status line only when it differs from the last one.
type sayer struct {
	p            ui.Painter
	prefix, said string
}

func (s *sayer) say(msg string) {
	if msg != s.said {
		fmt.Println(s.p.Paint(ui.Dim, s.prefix+msg))
		s.said = msg
	}
}

// waitIdle polls until swim is idle and the queue has nothing left to run,
// saying what it waits on whenever that changes. A limit > 0 gives up after
// that long. It checks quickly at first, backing off to every.
func waitIdle(root string, every, limit time.Duration) error {
	s := sayer{p: ui.Painter{On: ui.ColorEnabled(os.Stdout)}, prefix: "swim wait: "}
	start, nap := time.Now(), min(every, 100*time.Millisecond)
	for {
		cfg, err := loadCfg(root)
		if err != nil {
			return err
		}
		why := launcher.Busy(root, cfg)
		if why == "" {
			if why, err = queue.Busy(root); err != nil {
				return err
			}
		}
		if why == "" {
			s.say("idle")
			return nil
		}
		if limit > 0 && time.Since(start) >= limit {
			return fmt.Errorf("still busy after %s: %s", limit, why)
		}
		s.say("waiting: " + why)
		time.Sleep(nap)
		nap = min(2*nap, every)
	}
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func queueList(args []string) error {
	var all, asYAML bool
	rest, err := flags{bools: map[string]*bool{"all": &all, "yaml": &asYAML}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return usagef("usage: swim queue [list] [--all] [--yaml]")
	}
	root, _, err := repo()
	if err != nil {
		return err
	}
	entries, err := queue.List(root, true)
	if err != nil {
		return err
	}
	if !all { // the last 5 consumed entries, every other
		var done []*queue.Entry
		var rest []*queue.Entry
		for _, e := range entries {
			if e.State == queue.Done {
				done = append(done, e)
			} else {
				rest = append(rest, e)
			}
		}
		if len(done) > 5 {
			done = done[len(done)-5:]
		}
		entries = append(done, rest...)
	}
	if asYAML {
		type row struct {
			*queue.Entry `yaml:",inline"`
			State        string `yaml:"state"`
		}
		var rows []row
		for _, e := range entries {
			rows = append(rows, row{e, e.State})
		}
		return yaml.NewEncoder(os.Stdout).Encode(map[string]any{"schema": "swim.queue-list/v1", "entries": rows})
	}
	p := ui.Painter{On: ui.ColorEnabled(os.Stdout)}
	if len(entries) == 0 {
		fmt.Println("queue is empty. Queue a batch: swim queue add [N|JOB ...] [--approve FLAG] [-y]")
		return nil
	}
	for _, e := range entries {
		var ns []string
		for _, l := range e.Lanes {
			ns = append(ns, strconv.Itoa(l.Lane))
		}
		state, color := e.State, ""
		switch e.State {
		case queue.Running:
			state, color = fmt.Sprintf("running (pid %d)", e.Worker.PID), ui.Cyan
		case queue.Orphaned:
			state, color = "orphaned: its worker is gone (swim queue requeue "+strconv.Itoa(e.Seq)+")", ui.Red
		case queue.Done:
			state, color = "done: "+e.Result.Result, ui.StateColor(e.Result.Result)
		case queue.Queued:
			color = ui.Yellow
		}
		appr := ""
		switch {
		case e.ApproveAll:
			appr = "  approve all"
		case len(e.Approve) > 0:
			appr = "  approve " + strings.Join(e.Approve, ",")
		}
		fmt.Printf("#%-3d %s  %s  swim %s%s  %s\n", e.Seq, e.ID, p.Paint(color, state), strings.Join(ns, ","), appr, p.Paint(ui.Dim, e.Note))
	}
	return nil
}

func queueShow(args []string) error {
	if len(args) != 1 {
		return usagef("usage: swim queue show ID")
	}
	root, _, err := repo()
	if err != nil {
		return err
	}
	e, err := queue.Find(root, args[0])
	if err != nil {
		return err
	}
	data, err := os.ReadFile(e.Path)
	if err != nil {
		return err
	}
	fmt.Printf("# %s (%s)\n%s", rel(root, e.Path), e.State, data)
	return nil
}

func queueRemove(args []string) error {
	if len(args) != 1 {
		return usagef("usage: swim queue rm ID")
	}
	root, _, err := repo()
	if err != nil {
		return err
	}
	e, err := queue.Find(root, args[0])
	if err != nil {
		return err
	}
	if err := queue.Remove(e); err != nil {
		return err
	}
	fmt.Printf("removed %s (#%d, %s)\n", e.ID, e.Seq, e.State)
	return nil
}

func queueRequeue(args []string) error {
	if len(args) != 1 {
		return usagef("usage: swim queue requeue ID")
	}
	root, _, err := repo()
	if err != nil {
		return err
	}
	e, err := queue.Find(root, args[0])
	if err != nil {
		return err
	}
	if e.State == queue.Queued {
		return fmt.Errorf("%s is already waiting in the queue", e.ID)
	}
	from := e.State
	if err := queue.Requeue(root, e); err != nil {
		return err
	}
	fmt.Printf("requeued %s (was %s) as #%d\n", e.ID, from, e.Seq)
	return nil
}

// cmdWatch is the queue's worker: it runs each queued batch, oldest first,
// as soon as swim is idle, and consumes it when it ends.
func cmdWatch(args []string) error {
	var once, yes, approved, noTUI, headless bool
	var hf headlessFlags
	var interval string
	orig := args
	args = append([]string(nil), args...)
	for i, a := range args {
		if a == "-y" {
			args[i] = "--yes"
		}
	}
	rest, err := flags{bools: map[string]*bool{"once": &once, "yes": &yes, "approved": &approved, "no-tui": &noTUI,
		"headless": &headless, "detach": &hf.detach, "status": &hf.status, "stop": &hf.stop, "uninstall": &hf.uninstall, "i-mean-it": &hf.iMeanIt},
		strs: map[string]*string{"interval": &interval, "install": &hf.install}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return usagef("usage: swim watch [--once] [-y] [--approved] [--interval D] [--no-tui] [--headless [--detach|--status|--stop|--install systemd|launchd|--uninstall]]")
	}
	if !headless && hf.any() {
		return usagef("--detach, --status, --stop, --install, --uninstall and --i-mean-it go with --headless")
	}
	if headless {
		n := 0
		for _, on := range []bool{hf.detach, hf.status, hf.stop, hf.uninstall, hf.install != ""} {
			if on {
				n++
			}
		}
		switch {
		case n > 1:
			return usagef("pick one of --detach, --status, --stop, --install and --uninstall")
		case hf.iMeanIt && hf.install == "":
			return usagef("--i-mean-it goes with --install")
		case once && (hf.detach || hf.install != ""):
			return usagef("--once runs the queue and exits: it doesn't go with --detach or --install")
		}
	}
	every, err := durationFlag("interval", interval, pollEvery, "2s")
	if err != nil {
		return err
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	if err := requireVersion(root, true); err != nil {
		return err
	}
	if headless {
		switch {
		case hf.status:
			return headlessStatus(root)
		case hf.stop:
			return headlessStop(root, cfg)
		case hf.uninstall:
			return headlessUninstall(root, cfg)
		}
		if _, err := loadHub(root); err != nil {
			return headlessNoHub(err)
		}
		switch {
		case hf.install != "":
			return headlessInstall(root, cfg, hf.install, yes, hf.iMeanIt, approved, interval)
		case hf.detach:
			var child []string
			for _, a := range orig {
				if a != "--detach" && !strings.HasPrefix(a, "--detach=") {
					child = append(child, a)
				}
			}
			return headlessDetach(root, child)
		}
		release, err := headlessStart(root, yes, approved)
		if err != nil {
			return err
		}
		defer release()
	}
	p := ui.Painter{On: ui.ColorEnabled(os.Stdout)}
	sigs := make(chan os.Signal, 4)
	if headless {
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM) // SIGHUP reopens its log (headlessStart)
	} else {
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	}
	defer signal.Stop(sigs)
	stop := false
	stopped := func() bool {
		for {
			select {
			case <-sigs:
				stop = true
			default:
				return stop
			}
		}
	}
	sleep := func() {
		select {
		case <-sigs:
			stop = true
		case <-time.After(every):
		}
	}
	mode := "watching .swim/queue/ (Ctrl-C stops after the batch in hand)"
	switch {
	case once:
		mode = "running every queued batch, then exiting"
	case headless:
		mode = fmt.Sprintf("headless (%s), pid %d · watching .swim/queue/ (SIGTERM stops after the batch in hand; SIGHUP reopens the log)", headlessInfo.Mode, os.Getpid())
	}
	fmt.Println(p.Paint(ui.Bold, "swim watch · "+mode))
	if yes {
		fmt.Println(p.Paint(ui.Yellow+ui.Bold, "swim watch: -y: every guard in every batch is approved, beyond what each batch approves (logged as approved by swim watch -y ("+operator()+"))"))
	}
	code, s := 0, sayer{p: p, prefix: "swim watch: "}
	// Enrolled with a hub (swim hub enroll): report there as we go.
	setActivity, stopReporting := hubReporter(root, every)
	defer stopReporting()
	if headless {
		// Commands from the hub (hubcmds.go); drain stops it as SIGTERM does.
		stopCommands := headlessCommands(root, every, func() {
			select {
			case sigs <- syscall.SIGTERM:
			default:
			}
		})
		defer stopCommands()
	}
	// On a terminal, waiting shows the jobs and the queue: r runs one now,
	// q queues one (watchview.go).
	var view *watchView
	if !once && !headless && watchViewAllowed(noTUI) {
		view = newWatchView(root, cfg, yes)
		quietHub(true) // the screen shows the hub link; printing would scroll it
		defer quietHub(false)
		sleep = func() {
			ok := view.wait(func() bool {
				if stopped() {
					return true
				}
				// Back to the loop when there's a batch to take.
				c, err := loadCfg(root)
				if err != nil {
					return true
				}
				waiting, _ := queue.List(root, false)
				for _, e := range waiting {
					if e.State == queue.Queued {
						return launcher.Busy(root, c) == ""
					}
				}
				return false
			})
			if !ok {
				stop = true
			}
		}
	}
	say := func(msg string) {
		setActivity(msg)
		if view != nil {
			view.activity = "worker: " + msg
			return
		}
		s.say(msg)
	}
	for !stopped() {
		if approved {
			for _, msg := range queueApproved(root, cfg) {
				fmt.Println(p.Paint(ui.Cyan, "swim watch: "+msg))
			}
		}
		waiting, err := queue.List(root, false)
		if err != nil {
			return err
		}
		n := 0
		for _, e := range waiting {
			if e.State == queue.Queued {
				n++
			}
		}
		if n == 0 {
			if once {
				break
			}
			say("queue empty; waiting for swim queue add")
			sleep()
			continue
		}
		if cfg, err = loadCfg(root); err != nil {
			return err
		}
		if why := launcher.Busy(root, cfg); why != "" {
			say(fmt.Sprintf("%d queued; waiting for swim to be idle: %s", n, why))
			sleep()
			continue
		}
		unlock, ok, err := queue.LockWorker(root)
		if err != nil {
			return err
		}
		if !ok {
			say(fmt.Sprintf("%d queued; another worker is running a batch", n))
			sleep()
			continue
		}
		e, err := queue.ClaimNext(root, operator())
		if err != nil || e == nil {
			unlock()
			if err != nil {
				return err
			}
			continue
		}
		s.said = ""
		setActivity(fmt.Sprintf("running %s (#%d)", e.ID, e.Seq))
		res, interrupted := runClaimed(root, cfg, e, yes, view != nil, headless)
		setActivity(fmt.Sprintf("%s (#%d) %s", e.ID, e.Seq, res.Result))
		unlock()
		if view != nil {
			view.picker.Note = fmt.Sprintf("#%d %s", e.Seq, res.Result)
		}
		if res.Exit != 0 {
			code = 1
		}
		if interrupted {
			stop = true
		}
	}
	// A headless worker told to stop exits 0 (a failed batch is in its
	// queue entry), so a service manager doesn't restart it.
	if code != 0 && (once || !headless) {
		return exitError{code}
	}
	return nil
}

// runClaimed runs a batch this worker claimed and consumes it with its
// result. view says the watch screen is up: the run's own view may then
// take the keyboard (to move between lanes; it never answers guards).
// headless: no terminal at all (swim watch --headless).
func runClaimed(root string, cfg *config.Config, e *queue.Entry, yes, view, headless bool) (queue.Result, bool) {
	p := ui.Painter{On: ui.ColorEnabled(os.Stdout)}
	res, interrupted := runEntry(root, cfg, e, yes, view, headless)
	if err := queue.Consume(root, e, res); err != nil {
		fmt.Fprintln(os.Stderr, "swim watch: consume "+e.ID+": "+err.Error())
	}
	history.Log(root, history.Entry{Event: history.QueueDone, Run: res.Run, Detail: fmt.Sprintf("%s: %s  exit=%d", e.ID, res.Result, res.Exit)})
	fmt.Println(p.Paint(ui.StateColor(res.Result)+ui.Bold, fmt.Sprintf("swim watch: %s (#%d) %s; consumed to %s", e.ID, e.Seq, res.Result, rel(root, e.Path))))
	return res, interrupted
}

// loadCfg reloads the repo's config (a long-running watch picks up edits).
func loadCfg(root string) (*config.Config, error) {
	_, cfg, err := repoNoMigrate()
	return cfg, err
}

// runEntry runs one claimed batch through the launcher with the batch's
// approvals and nothing else: a guard flag the batch doesn't approve is
// cleared from the environment for the run, so the queue file alone says
// what may change. It never prompts (nobody is watching a queue).
func runEntry(root string, cfg *config.Config, e *queue.Entry, workerYes, keys, headless bool) (queue.Result, bool) {
	started := time.Now().UTC()
	res := queue.Result{Started: started.Format(time.RFC3339)}
	var lanes []int
	pins := map[int]string{}
	guards := map[string]bool{}
	for _, l := range e.Lanes {
		lanes = append(lanes, l.Lane)
		if l.Job != "" {
			pins[l.Lane] = l.Job
		}
		if info, err := lane.ReadScript(root, l.Lane); err == nil {
			for _, g := range info.Guards {
				guards[g.Flag] = true
			}
		}
	}
	approved := map[string]bool{}
	for _, f := range e.Approve {
		approved[f] = true
	}
	restore := map[string]*string{}
	setenv := func(k, v string, unset bool) {
		if _, ok := restore[k]; !ok {
			if old, had := os.LookupEnv(k); had {
				restore[k] = &old
			} else {
				restore[k] = nil
			}
		}
		if unset {
			os.Unsetenv(k)
		} else {
			os.Setenv(k, v)
		}
	}
	for g := range guards {
		setenv(g, "1", !approved[g])
	}
	for f := range approved {
		setenv(f, "1", false)
	}
	setenv("SWIM_QUEUE", fmt.Sprintf("queue %s by %s", e.ID, e.By), false)
	setenv("SWIM_QUEUE_ENTRY", e.Path, false) // the guards check its signature for standing approvals
	setenv("SWIM_QUEUE_APPROVED", strings.Join(e.Approve, " "), false)
	defer func() {
		for k, v := range restore {
			if v == nil {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, *v)
			}
		}
	}()

	pinSHA, pinEnv, pinMan := map[int]string{}, map[int]string{}, map[int]string{}
	for _, l := range e.Lanes {
		if l.SHA256 != "" {
			pinSHA[l.Lane] = l.SHA256
		}
		if l.Manifest != "" {
			pinMan[l.Lane] = l.Manifest
		}
		if l.EnvSHA256 != nil {
			pinEnv[l.Lane] = *l.EnvSHA256
		}
	}
	o := launcher.Options{Root: root, Cfg: cfg, Lanes: lanes, Pins: pins, PinSHA: pinSHA, PinEnv: pinEnv, PinManifest: pinMan, Self: self(), Out: os.Stdout,
		RunID: lane.NewRunID(), Parallel: e.Parallel,
		AutoApprove: e.ApproveAll, ApprovedBy: e.By, ApprovedVia: "queue " + e.ID + " -y",
		Unattended: true,
		HubSecrets: func() (map[string]string, error) { return hubGrants(root) }}
	if headless {
		// Plain lines for the log; SIGHUP reopens it rather than forcing
		// the run as a closed terminal does.
		o.Plain, o.NoHangup = true, true
	}
	if keys && len(lanes) > 1 {
		// The live view's keys move between the batch's lanes. Parallel
		// lanes get no stdin, so nothing in the batch can prompt.
		o.Stdin, o.Interactive = os.Stdin, display.TUIAllowed(os.Getenv)
	}
	if !e.ApproveAll && workerYes {
		// The worker approves what the batch doesn't (the batch's own
		// approvals keep their credit): swim watch -y.
		o.AutoApprove, o.ApprovedBy, o.ApprovedVia = true, operator(), "swim watch -y"
		res.ApprovedBy = "swim watch -y (" + operator() + ")"
	}
	interrupted := false
	o.Finished = func(f launcher.Finish) { interrupted = f.Interrupted }
	res.Run = o.RunID
	var ns []string
	for _, n := range lanes {
		ns = append(ns, strconv.Itoa(n))
	}
	detail := fmt.Sprintf("%s: swim %s  queued by %s", e.ID, strings.Join(ns, ","), e.By)
	if res.ApprovedBy != "" {
		detail += "  every other guard approved by " + res.ApprovedBy
	}
	history.Log(root, history.Entry{Event: history.QueueRun, Run: o.RunID, Detail: detail})
	fmt.Printf("\nswim watch: running %s (#%d, queued by %s): swim %s\n", e.ID, e.Seq, e.By, strings.Join(ns, " "))

	code, err := launcher.Run(o)
	res.Finished = time.Now().UTC().Format(time.RFC3339)
	res.Exit = code
	switch {
	case err != nil:
		res.Result, res.Error, res.Exit = "error", err.Error(), 2
	case interrupted:
		res.Result = status.Interrupted
	case code == 0:
		res.Result = status.Passed
	default:
		res.Result = status.Failed
	}
	if st, _ := status.Load(root); st != nil {
		for _, n := range lanes {
			if l := st.Get(n); l != nil {
				res.Lanes = append(res.Lanes, queue.LaneResult{Lane: n, Result: l.State, Exit: l.ExitCode, Reason: l.Reason})
			}
		}
	}
	return res, interrupted
}

// queueApproved queues every lane whose exact script is fully approved
// (approvals/lane.N.<sha>.signature whose standing signers meet lookup.yml's
// rule for each guard it approves) and hasn't been queued or run for that
// approval yet. The run's guards then count the same approval, credited to
// its approvers.
func queueApproved(root string, cfg *config.Config) []string {
	lk, err := lookup.Load(root)
	if err != nil {
		return []string{"approvals: " + err.Error()}
	}
	files, _ := sigfile.All(root)
	done := map[string]bool{}
	if entries, err := queue.List(root, true); err == nil {
		for _, e := range entries {
			if e.Signature != "" {
				done[e.Signature] = true
			}
		}
	}
	ran := map[string]bool{} // scripts that already passed
	if links, err := chain.Read(root); err == nil {
		for _, l := range links {
			if l.Type == "round" && l.Result == status.Passed {
				ran[l.Script] = true
			}
		}
	}
	var msgs []string
	now := time.Now()
	mans := map[int]*manifest.Manifest{}
	for _, f := range files {
		if f.Kind != sigfile.Script {
			continue
		}
		id := f.StatementHash()
		if done[id] || ran[f.SHA256] || f.Expired(now) {
			continue
		}
		if staleWhy(root, cfg, f, mans) != "" {
			continue // stale: the script, or what its manifest pins, changed since it was approved
		}
		info, err := lane.ReadScript(root, f.Lane)
		if err != nil || !info.Pending() || f.Lane > cfg.Lanes {
			continue
		}
		flags := f.Approves
		if f.All {
			flags = info.GuardFlags()
		}
		ok := true
		users := map[string]bool{}
		for _, g := range flags {
			d := sigfile.Decide([]*sigfile.File{f}, lk, g, now)
			if !d.OK {
				ok = false
				break
			}
			for _, u := range d.Users {
				users[u] = true
			}
		}
		if !ok {
			continue // not fully approved yet
		}
		names := sortedKeys(users)
		e := &queue.Entry{By: "approval (" + strings.Join(names, ", ") + ")", Note: "approved " + rel(root, f.Path),
			Lanes: []queue.Lane{{Lane: f.Lane, Job: info.Job, Round: info.Round, SHA256: f.SHA256}}, Signature: id}
		if err := queue.Add(root, e); err != nil {
			msgs = append(msgs, "queue "+rel(root, f.Path)+": "+err.Error())
			continue
		}
		done[id] = true
		history.Log(root, history.Entry{Event: history.QueueAdd, Detail: fmt.Sprintf("%s: swim %d  approved by %s  (%s)", e.ID, f.Lane, strings.Join(names, ", "), rel(root, f.Path))})
		msgs = append(msgs, fmt.Sprintf("queued swim %d (%s): approved by %s", f.Lane, e.ID, strings.Join(names, ", ")))
	}
	return msgs
}
