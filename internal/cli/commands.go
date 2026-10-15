package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/assets"
	"github.com/gates-brightly/swimlane/internal/ci"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/evidence"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/launcher"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/policy"
	"github.com/gates-brightly/swimlane/internal/queue"
	"github.com/gates-brightly/swimlane/internal/secrets"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/step"
	"github.com/gates-brightly/swimlane/internal/ui"
	"github.com/gates-brightly/swimlane/internal/version"
)

const (
	gitignoreBegin = "# >>> swim >>>"
	gitignoreEnd   = "# <<< swim <<<"
)

func cmdInit(args []string) error {
	if len(args) > 0 {
		return usagef("init takes no arguments")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root := config.RepoRoot(cwd)
	path := config.Path()
	created, added, err := config.EnsureRepo(path, root)
	if err != nil {
		return err
	}
	cfg, err := config.Load(root)
	if err != nil {
		return err
	}
	switch {
	case created:
		fmt.Printf("created %s with a section for %s\n", path, root)
	case added:
		fmt.Printf("added %s to %s\n", root, path)
	default:
		fmt.Printf("%s already configured in %s\n", root, path)
	}

	changed, err := mergeGitignore(filepath.Join(root, ".gitignore"))
	if err != nil {
		return err
	}
	if changed {
		fmt.Println("updated .gitignore (lane.[0-9]*, .lane*.rc, .swim/, .swim.log)")
	} else {
		fmt.Println(".gitignore already has the swim block")
	}

	if err := refreshStatus(root, cfg); err != nil {
		return err
	}
	history.Log(root, history.Entry{Event: history.Init, Detail: "status " + rel(root, status.Path(root))})
	fmt.Printf("status: %s   project log: %s   (lanes: the ones in use; start one with swim new N)\n", rel(root, status.Path(root)), history.FileName)
	fmt.Println(`next: swim new 1 "<round goal>"   (AI agents: read swim --help first)`)
	return nil
}

// mergeGitignore writes or refreshes the marked swim block.
func mergeGitignore(path string) (bool, error) {
	block := gitignoreBegin + "\n" + assets.Gitignore + gitignoreEnd + "\n"
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	s := string(data)
	if i := strings.Index(s, gitignoreBegin); i >= 0 {
		j := strings.Index(s[i:], gitignoreEnd)
		if j < 0 {
			return false, fmt.Errorf("%s has %q without %q; fix it by hand", path, gitignoreBegin, gitignoreEnd)
		}
		end := i + j + len(gitignoreEnd)
		if end < len(s) && s[end] == '\n' {
			end++
		}
		updated := s[:i] + block + s[end:]
		if updated == s {
			return false, nil
		}
		return true, os.WriteFile(path, []byte(updated), 0o644)
	}
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	if s != "" {
		s += "\n"
	}
	return true, os.WriteFile(path, []byte(s+block), 0o644)
}

// refreshStatus records each lane's pending round from the lane scripts on disk.
func refreshStatus(root string, cfg *config.Config) error {
	// Scheduled batches waiting, and the batches they hold (swim schedule).
	entries, _ := queue.List(root, false)
	held := heldBy(root, cfg, entries)
	sched, holder := map[int]*queue.Entry{}, map[int]string{}
	for _, e := range entries {
		for _, l := range e.Lanes {
			switch {
			case e.State == queue.Queued && e.NotBefore != "":
				if sched[l.Lane] == nil {
					sched[l.Lane] = e
				}
			case held[e.ID] != nil:
				holder[l.Lane] = held[e.ID].ID
			}
		}
	}
	return status.UpdateFile(root, cfg.Lanes, func(f *status.File) error {
		for i := range f.Lanes {
			l := &f.Lanes[i]
			l.ScheduledFor, l.Schedule, l.HeldBy = "", "", holder[l.Lane]
			if e := sched[l.Lane]; e != nil {
				l.ScheduledFor, l.Schedule = e.NotBefore, e.ID
			}
			info, err := lane.ReadScript(root, l.Lane)
			if err != nil {
				return err
			}
			l.Pending, l.PendingJob = "", ""
			if info.Pending() {
				l.Pending, l.PendingJob = info.Round, info.Job
			}
			l.Script = lane.ScriptName(root, l.Lane)
		}
		return nil
	})
}

func cmdConfig(args []string) error {
	var pathOnly, toRepo bool
	var setLanes string
	rest, err := flags{bools: map[string]*bool{"path": &pathOnly, "repo": &toRepo}, strs: map[string]*string{"lanes": &setLanes}}.parse(args)
	if err != nil {
		return err
	}
	if pathOnly {
		if len(rest) > 0 || setLanes != "" {
			return usagef("--path takes no key or value")
		}
		fmt.Println(config.Path())
		return nil
	}
	if setLanes != "" {
		return errNoLaneCount
	}
	if len(rest) > 2 {
		return usagef("usage: swim config [KEY [VALUE]] [--repo] [--path]")
	}
	if len(rest) == 0 {
		if toRepo {
			return usagef("--repo is for setting a key: swim config KEY VALUE --repo")
		}
		return showConfig()
	}
	key := rest[0]
	if key == "lanes" {
		return errNoLaneCount
	}
	_, isRunner := config.RunnerKey(key)
	if !slices.Contains(config.Keys, key) && !isRunner {
		return usagef("unknown config key %q; keys: %s, runners.EXT", key, strings.Join(config.Keys, ", "))
	}
	if isRunner {
		toRepo = true // runners are per repo, like the confirmation that saves them
	}
	if len(rest) == 1 {
		_, cfg, err := repo()
		if err != nil {
			return err
		}
		v, err := cfg.Get(key)
		if err != nil {
			return err
		}
		fmt.Println(v)
		return nil
	}
	return setConfigKey(key, rest[1], toRepo)
}

// showConfig prints the effective configuration for this repo.
func showConfig() error {
	_, cfg, err := repo()
	if err != nil {
		return err
	}
	out, _ := yaml.Marshal(map[string]any{
		"config_file":  cfg.Path,
		"repo":         cfg.Root,
		"repo_section": cfg.HasRepo,
		"repo_file":    cfg.RepoFile,
		"effective":    cfg.Settings,
	})
	fmt.Print(string(out))
	ig, tg := cfg.Graces()
	fmt.Printf("interrupt: %s   # first Ctrl-C; interrupt_grace %s, term_grace %s\n", cfg.InterruptMode(), ig, tg)
	fmt.Println("blocked_commands:            # no lane command may contain these")
	for _, b := range cfg.Blocked() {
		mark := ""
		if policy.IsBuiltin(b) {
			mark = "   # built-in, always on"
		}
		fmt.Printf("  - %q%s\n", b, mark)
	}
	if !cfg.HasRepo {
		fmt.Println("# no section for this repo; run `swim init` to add one")
	}
	return nil
}

// errNoLaneCount answers `swim config lanes N`: there's no lane count.
var errNoLaneCount = errors.New("swim no longer has a lane count: it shows the lanes in use (a script, a current log or a running process). Start one with `swim new N`; `swim archive N` drops one")

// setConfigKey writes one operator setting: under defaults:, or this
// repo's section with --repo. lanes always goes to the repo's section
// (lane numbers are per repo) and keeps the pending-round guard.
func setConfigKey(key, value string, toRepo bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, path := config.RepoRoot(cwd), config.Path()
	if err := config.SetKey(path, root, key, value, toRepo); err != nil {
		return err
	}
	where := "defaults"
	if toRepo {
		where = "repo " + root
	}
	written, _ := config.Normalize(key, value)
	fmt.Printf("%s: %s  (%s in %s)\n", key, written, where, path)
	if !toRepo {
		if cfg, err := config.Load(root); err == nil {
			if v, _ := cfg.Get(key); v != written {
				fmt.Printf("note: this repo's section sets %s: %s, which wins here (change it with --repo)\n", key, v)
			}
		}
	}
	return nil
}

func cmdNew(args []string) error {
	var force bool
	var job, lang string
	rest, err := flags{bools: map[string]*bool{"force": &force}, strs: map[string]*string{"job": &job, "lang": &lang}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) < 2 {
		return usagef(`usage: swim new N "<round goal>" [--lang sh|py|js|ts]`)
	}
	lang = strings.TrimPrefix(strings.ToLower(lang), ".")
	if lang == "" {
		lang = "sh"
	}
	if !slices.Contains(assets.Langs, lang) {
		return usagef("--lang %q: use one of %s", lang, strings.Join(assets.Langs, ", "))
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	if err := requireVersion(root, false); err != nil {
		return err
	}
	n, err := laneArg(cfg, rest[0])
	if err != nil {
		return err
	}
	goal := strings.TrimSpace(strings.Join(rest[1:], " "))
	if goal == "" {
		return usagef("the round goal must not be empty")
	}
	if err := refuseRecurring(root, n, "new", force); err != nil {
		return err
	}
	if job == "" {
		job = lane.NewJobID()
	} else if !lane.ValidJobID(job) {
		return usagef("job id %q: use 8-64 letters, digits, '.', '_' or '-' (not all digits)", job)
	}
	for m := 1; m <= cfg.Lanes; m++ {
		if info, _ := lane.ReadScript(root, m); m != n && info.Pending() && strings.EqualFold(info.Job, job) {
			return fmt.Errorf("job %s is already in %s; job ids must be unique", job, lane.ScriptName(root, m))
		}
	}
	content, err := assets.ScriptIn(lang, n, goal, cfg.Toolchain, job, os.Getenv("USER"))
	if err != nil {
		return err
	}
	if err := lane.WriteScriptAs(root, n, lang, content, force); err != nil {
		return err
	}
	status.Update(root, n, cfg.Lanes, func(l *status.Lane) {
		l.Pending, l.PendingJob = strings.Join(strings.Fields(goal), " "), job
	})
	history.Log(root, history.Entry{Event: history.New, Lane: n, Job: job, Detail: goal})
	fmt.Printf("wrote %s (swim %d): %s\n", rel(root, lane.Script(root, n)), n, goal)
	fmt.Printf("job: %s\n", job)
	fmt.Printf("edit its steps, then the operator runs: swim run %d   (or pinned: swim run %s)\n", n, job)
	fmt.Printf("check it after editing: swim lint %d\n", n)
	return nil
}

// runFlags are the options swim run and swim all share.
type runFlags struct {
	plain, rerun     bool
	runID            string
	parallel         string
	chime, noChime   bool
	yaml, yamlOutput bool
	interrupt        string
	noTUI            bool
	noPrompt         bool
	yes              bool
	failed           bool // matrix lanes: only the cells that didn't pass
	noFinally        bool // leave out the finally lanes naming the lanes run
	replace          bool // end a paused round on the lane (swim pause) and run the new one
}

func (rf *runFlags) parse(args []string) ([]string, error) {
	// -y is the one short flag: approve every guard question (--yes).
	args = append([]string(nil), args...)
	for i, a := range args {
		if a == "--" {
			break
		}
		if a == "-y" {
			args[i] = "--yes"
		}
	}
	return flags{
		bools: map[string]*bool{"plain": &rf.plain, "rerun": &rf.rerun, "chime": &rf.chime, "no-chime": &rf.noChime,
			"yaml": &rf.yaml, "yaml-output": &rf.yamlOutput, "no-tui": &rf.noTUI, "no-prompt": &rf.noPrompt, "yes": &rf.yes, "no-finally": &rf.noFinally,
			"failed": &rf.failed, "replace": &rf.replace},
		strs: map[string]*string{"run-id": &rf.runID, "parallel": &rf.parallel, "interrupt": &rf.interrupt},
	}.parse(args)
}

func cmdRun(args []string) error {
	if hasDashDash(args) {
		// swim run "<label>" -- cmd: a step, from inside a lane.
		return laneStep("run", args)
	}
	var rf runFlags
	rest, err := rf.parse(args)
	if err != nil {
		return err
	}
	return runLanes(rest, rf)
}

// runLanes runs the named lanes (lane numbers or job ids), or with none,
// every pending lane.
func runLanes(rest []string, rf runFlags) error {
	plain, rerun := rf.plain, rf.rerun
	var parallel *int
	if rf.parallel != "" {
		n, err := strconv.Atoi(rf.parallel)
		if err != nil || n < 0 {
			return usagef("--parallel needs 0 (unlimited) or a positive number, got %q", rf.parallel)
		}
		parallel = &n
	}
	if rf.runID != "" && !lane.ValidRunID(rf.runID) {
		return usagef("--run-id %q: use 8-64 letters, digits, '.', '_' or '-' (not all digits)", rf.runID)
	}
	if rf.chime && rf.noChime {
		return usagef("use --chime or --no-chime, not both")
	}
	if rf.yamlOutput {
		rf.yaml = true
	}
	if rf.yes && rf.noPrompt {
		return usagef("use -y (approve every guard question) or --no-prompt (ask none), not both")
	}
	if rf.interrupt != "" && rf.interrupt != config.InterruptGraceful && rf.interrupt != config.InterruptImmediate {
		return usagef("--interrupt takes graceful or immediate, got %q", rf.interrupt)
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	if rerun && len(rest) > 0 {
		return usagef("--rerun is for running every pending lane; named lanes always run")
	}
	if root, _, err := repo(); err != nil {
		return err
	} else if err := requireVersion(root, true); err != nil {
		return err
	}
	var lanes []int
	pins := map[int]string{}
	cells := map[int][]string{}
	for _, a := range rest {
		// A matrix cell: 3[orders], or <job>/<cell>.
		ref, cell, isCell := lane.ParseCellRef(a)
		if !isCell {
			if j, c, ok := strings.Cut(a, "/"); ok && lane.ValidJobID(j) {
				ref, cell, isCell = j, c, true
			}
		}
		n, err := laneRef(root, cfg, ref, false)
		if err != nil {
			return err
		}
		if isCell {
			info, _ := lane.ReadScript(root, n)
			if !info.IsMatrix() {
				return fmt.Errorf("swim %d has no jobs and is not a matrix round; %s names nothing", n, a)
			}
			// A job names it and every job under it (1.5: 1.5, 1.5.9, ...).
			named := info.CellsUnder(cell)
			if len(named) == 0 {
				what := "cell " + strconv.Quote(cell)
				if info.IsGroup() {
					what = fmt.Sprintf("job %d.%s", n, cell)
				}
				return fmt.Errorf("swim %d has no %s (its %s: %s)", n, what, map[bool]string{true: "jobs", false: "cells"}[info.IsGroup()], strings.Join(launcher.CellNames(info.Cells()), ", "))
			}
			for _, c := range named {
				if !slices.Contains(cells[n], c.Name) {
					cells[n] = append(cells[n], c.Name)
				}
			}
		}
		if !slices.Contains(lanes, n) {
			lanes = append(lanes, n)
		}
		if _, err := strconv.Atoi(ref); err != nil {
			pins[n] = ref // named by job id: must still hold it when it starts
		}
	}
	if err := refreshStatus(root, cfg); err != nil {
		return err
	}
	// A paused lane (swim pause) holds a live round: running anything on it
	// ends that round first, asked at a terminal or with --replace.
	for _, n := range lanes {
		if err := replaceIfPaused(root, n, rf); err != nil {
			return err
		}
	}
	// A lane a scheduled batch holds runs then, not now: twice otherwise.
	for _, n := range lanes {
		if e := scheduledHolding(root, n); e != nil {
			return fmt.Errorf("swim %d is scheduled for %s in %s (#%d); swim schedule rm %s first, or wait for it", n, scheduledAt(e), e.ID, e.Seq, e.ID)
		}
	}
	// Pre-flight: lint's error-level checks the launcher doesn't enforce (lint.go).
	if err := lintPreflight(root, cfg, lanes, rerun); err != nil {
		return err
	}
	// The first lane of a kind swim has no runner for: ask now, before the
	// live view takes the terminal (runners.go).
	if cfg, err = confirmRunners(root, cfg, lanes); err != nil {
		return err
	}
	o := launcher.Options{
		Root: root, Cfg: cfg, Lanes: lanes, Plain: plain, Rerun: rerun,
		Self: self(), Out: os.Stdout, Stdin: os.Stdin, RunID: rf.runID, Parallel: parallel,
		Finished:    chimeWhenDone(cfg, rf),
		Interactive: !rf.noTUI && display.TUIAllowed(os.Getenv),
		Interrupt:   rf.interrupt,
		// The secrets key's passphrase, if it has one, is asked here
		// before the live view starts (a terminal is needed).
		SecretsPrompt: passphrasePrompt,
		HubSecrets:    func() (map[string]secrets.Granted, error) { return hubGrants(root) },
		Pins:          pins,
		Cells:         cells,
		FailedCells:   rf.failed,
		// Guards without their flag may ask the operator (not in CI, where
		// nobody is at the terminal, nor with approvals: flags).
		Prompt: !rf.noPrompt && cfg.ApprovalMode() == config.ApprovalsPrompt && !ci.InCI(os.Getenv),
		// -y approves every guard question without asking (the operator's
		// explicit say-so for this run; FLAG=0 still declines).
		AutoApprove: rf.yes,
		NoFinally:   rf.noFinally,
	}
	if rf.yaml {
		o.YAML, o.YAMLOutput = os.Stdout, rf.yamlOutput
	}
	code, err := launcher.Run(o)
	if err != nil {
		return err
	}
	checkpointAfterRun(root, cfg)
	if code != 0 {
		return exitError{code}
	}
	return nil
}

func joinNums(ns []int) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ", ")
}

// cmdPlan prints what `swim run` / `swim all` would do, running nothing.
func cmdPlan(args []string) (err error) {
	var rerun, asYAML, check, noFinally bool
	var parallel, timeout string
	rest, err := flags{bools: map[string]*bool{"rerun": &rerun, "yaml": &asYAML, "check": &check, "no-finally": &noFinally},
		strs: map[string]*string{"parallel": &parallel, "timeout": &timeout}}.parse(args)
	if err != nil {
		return err
	}
	if !check && (parallel != "" || timeout != "") {
		return usagef("--parallel and --timeout are for swim plan --check")
	}
	if rerun && len(rest) > 0 {
		return usagef("--rerun is for planning every pending lane; named lanes always run")
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	warnVersion(root)
	var lanes []int
	for _, a := range rest {
		n, err := laneRef(root, cfg, a, false)
		if err != nil {
			return err
		}
		lanes = append(lanes, n)
	}
	// The plan shows each lane's last read-only check (stale once its
	// script changes); --check runs them first.
	o := launcher.Options{Root: root, Cfg: cfg, Lanes: lanes, Rerun: rerun, ShowChecks: true, NoFinally: noFinally}
	checkCode := 0
	if check {
		if checkCode, err = runChecks(o, parallel, timeout, asYAML); err != nil {
			return err
		}
		o.ChecksRan = true
	}
	defer func() {
		if err == nil && checkCode != 0 {
			err = exitError{checkCode}
		}
	}()
	if asYAML {
		doc, err := launcher.Plan(o)
		if err != nil {
			return err
		}
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		if err := enc.Encode(doc); err != nil {
			return err
		}
		return enc.Close()
	}
	err = launcher.WritePlan(os.Stdout, o, ui.ColorEnabled(os.Stdout))
	return err
}

// runChecks is swim plan --check's first half: run the snapshot and check
// stages of every lane the plan would run, all at once, read-only. Their
// output goes to stderr (stdout is the plan). It returns 1 if any check
// didn't pass.
func runChecks(o launcher.Options, parallel, timeout string, quiet bool) (int, error) {
	doc, err := launcher.Plan(o)
	if err != nil {
		return 0, err
	}
	var lanes []int
	for _, l := range doc.Lanes {
		if l.Action != "skip" {
			lanes = append(lanes, l.Lane)
		}
	}
	if len(lanes) == 0 {
		return 0, nil
	}
	if err := requireVersion(o.Root, true); err != nil {
		return 0, err
	}
	co := launcher.Options{Root: o.Root, Cfg: o.Cfg, Lanes: lanes, CheckOnly: true, Plain: true, Out: os.Stderr,
		Self: self(), RunID: lane.NewRunID()}
	if quiet {
		if devnull, err := os.Open(os.DevNull); err == nil {
			defer devnull.Close()
			co.Out = devnull
		}
	}
	if parallel != "" {
		n, err := strconv.Atoi(parallel)
		if err != nil || n < 0 {
			return 0, usagef("--parallel needs 0 (unlimited) or a positive number, got %q", parallel)
		}
		co.Parallel = &n
	}
	if timeout != "" {
		d, err := time.ParseDuration(timeout)
		if err != nil || d <= 0 {
			return 0, usagef("--timeout needs a duration like 10m, got %q", timeout)
		}
		co.CheckTimeout = d
	}
	code, err := launcher.Run(co)
	os.Unsetenv(lane.CheckEnv)
	if err != nil {
		return 0, err
	}
	if code != 0 {
		return 1, nil
	}
	return 0, nil
}

// cmdAll runs every lane holding a pending round: `swim run` with no lanes.
func cmdAll(args []string) error {
	var rf runFlags
	rest, err := rf.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return usagef("all takes no lanes (it runs every pending one); to pick lanes: swim run %s", strings.Join(rest, " "))
	}
	return runLanes(nil, rf)
}

func cmdStep(args []string) error {
	var label, timeout, retry, backoff, retryOn string
	var fresh, snap, noRetryTimeout, evid bool
	// Flags only before "--"; everything after is the command.
	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
			break
		}
	}
	var cmdArgs []string
	flagArgs := args
	if sep >= 0 {
		flagArgs, cmdArgs = args[:sep], args[sep+1:]
	}
	rest, err := flags{
		bools: map[string]*bool{"new": &fresh, "snapshot": &snap, "no-retry-timeout": &noRetryTimeout, "evidence": &evid},
		strs:  map[string]*string{"label": &label, "timeout": &timeout, "retry": &retry, "backoff": &backoff, "retry-on": &retryOn},
	}.parse(flagArgs)
	if err != nil {
		return err
	}
	if sep < 0 {
		cmdArgs, rest = rest, nil
	}
	if len(rest) > 0 || len(cmdArgs) == 0 {
		return usagef("usage: swim step [--label L] [--new] [--snapshot] -- cmd [args...]")
	}

	cwd, _ := os.Getwd()
	root := config.RepoRoot(cwd)
	cfg, err := config.Load(root)
	if err != nil {
		// A broken config must not stop the operator's command from running.
		fmt.Fprintln(os.Stderr, "swim step: warning:", err)
		cfg = &config.Config{Root: root, Settings: config.Settings{Lanes: 1}}
	}
	n, _ := strconv.Atoi(os.Getenv("SWIM_LANE"))
	if !cfg.ValidLane(n) {
		n = 0
	}
	// A step limit: --timeout, else the script's Step-Timeout: (exported by
	// lane_init); --timeout 0 turns the default off for this step.
	stepTimeout, stepText := time.Duration(0), ""
	if timeout == "" {
		if t := os.Getenv("SWIM_STEP_TIMEOUT"); t != "" && t != "0" {
			timeout = t
		}
	}
	if timeout != "" && timeout != "0" {
		d, err := time.ParseDuration(timeout)
		if err != nil || d <= 0 {
			return usagef("--timeout %q: use a duration like 30s, 5m or 1h30m (0 for none)", timeout)
		}
		stepTimeout, stepText = d, timeout
	}
	var rt step.Retry
	if retry != "" {
		n, err := strconv.Atoi(retry)
		if err != nil || n < 0 {
			return usagef("--retry %q: use a number of extra attempts (e.g. 3)", retry)
		}
		rt.Max = n
	}
	if backoff != "" {
		d, err := time.ParseDuration(backoff)
		if err != nil || d < 0 {
			return usagef("--backoff %q: use a duration like 5s", backoff)
		}
		rt.Backoff = d
	}
	if retryOn != "" {
		for _, c := range strings.Split(retryOn, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(c))
			if err != nil {
				return usagef("--retry-on %q: use exit codes, e.g. 1,255", retryOn)
			}
			rt.On = append(rt.On, n)
		}
	}
	rt.NoTimeout = noRetryTimeout
	// A paused round (swim pause) holds here, before the step starts.
	holdIfPaused(root, n, os.Getenv("SWIM_RUN"), label)
	// lane_init exports the round's deadline (Timeout:) as epoch seconds;
	// time the round spent paused moves it out.
	deadline := roundDeadline(root, n)
	logPath := os.Getenv("STEP_LOG")
	if logPath == "" {
		logPath = "step.log"
	}
	// A step that writes a cache two writers can't share (terraform init
	// and its plugin cache, # Cache: terraform) holds its write lock.
	defer cacheWriteLock(cmdArgs, label)()
	res, err := step.Run(step.Options{
		Args: cmdArgs, Label: label, New: fresh, Snapshot: snap || evid, Evidence: evid,
		LogPath: logPath, Root: root, Lane: n, Lanes: cfg.Lanes,
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		Color:    ui.ColorEnabled(os.Stderr),
		Deadline: deadline, TimeoutText: os.Getenv("SWIM_TIMEOUT"),
		StepTimeout: stepTimeout, StepTimeoutText: stepText, Retry: rt,
		Blocked: cfg.Blocked(),
		Mask:    cfg.Masker(os.Environ()),
		// A stop request ends the round at its next step, but a handler's
		// steps (rollback, cleanup) still run: SWIM_IN_HANDLER.
		Stopped: func() bool {
			return n > 0 && os.Getenv("SWIM_IN_HANDLER") == "" && lane.StopRequested(root, n, os.Getenv("SWIM_RUN"))
		},
	})
	if err != nil {
		return err
	}
	if evid && res.ExitCode == 0 && res.Saved != "" && n > 0 {
		// The round's evidence: what an approval of the plan covers.
		if data, err := os.ReadFile(res.Saved); err == nil {
			key := evidence.LaneKey(n, "")
			if c := lane.CurrentCell(); c != "" {
				key = evidence.LaneKey(n, lane.CellSlug(c))
			}
			if _, err := evidence.Record(root, key, os.Getenv("SWIM_JOB"), os.Getenv("SWIM_RUN"), scriptSHA(root, n), label, string(data), lane.CheckMode()); err != nil {
				fmt.Fprintln(os.Stderr, "swim step: evidence: "+err.Error())
			}
		}
	}
	if s, ok := res.Signal.(syscall.Signal); ok && (s == syscall.SIGINT || s == syscall.SIGTERM || s == syscall.SIGHUP) {
		// Die by the same signal so the calling shell stops the lane script
		// (bash only aborts on Ctrl-C if its child died of SIGINT).
		reraise(s)
	}
	if res.ExitCode != 0 {
		return exitError{res.ExitCode}
	}
	return nil
}

func cmdLib(args []string) error {
	if len(args) > 0 {
		return usagef("lib takes no arguments")
	}
	fmt.Print(assets.LibFor(self()))
	return nil
}

// cmdArchive archives lanes' logs: swim archive N|JOB ... [--as NAME].
// Every argument that is a lane number or a job id is a lane; each lane's
// log becomes agentN.prev-<NAME>.log, or with no name its round's job id.
// The older form, a name after one lane (swim archive 1 cutover), still
// works: arguments after the lanes that are neither are taken as the name.
func cmdArchive(args []string) error {
	var as string
	var force bool
	rest, err := flags{strs: map[string]*string{"as": &as}, bools: map[string]*bool{"force": &force}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) < 1 {
		return usagef("usage: swim archive N|JOB ... [--as NAME] [--force]")
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	var lanes []int
	seen := map[int]bool{}
	for i, a := range rest {
		n, err := laneRef(root, cfg, a, true)
		if err != nil {
			if _, numErr := strconv.Atoi(a); numErr == nil || i == 0 || as != "" {
				return err // a number is always a lane; so is the first argument
			}
			// The older form: the rest is the archive's name.
			as = strings.Join(rest[i:], " ")
			break
		}
		if !seen[n] {
			seen[n] = true
			lanes = append(lanes, n)
		}
	}
	// Check every lane first: archive all of them or none.
	type plan struct {
		n    int
		what string
		dst  string
	}
	var plans []plan
	var problems []string
	for _, n := range lanes {
		if err := refuseAlways(root, n, "archive", force); err != nil {
			problems = append(problems, err.Error())
			continue
		}
		what := as
		if what == "" {
			// Default to the job id of the round being archived.
			if r, _ := logparse.ParseFile(lane.Log(root, n)); r.Job != "" {
				what = r.Job
			} else if _, err := os.Stat(lane.Log(root, n)); err == nil {
				problems = append(problems, fmt.Sprintf("swim %d's log has no job id; name the archive: swim archive %d --as <name>", n, n))
				continue
			} else if st, _ := status.Load(root); st != nil && st.Get(n) != nil && st.Get(n).Job != "" {
				what = st.Get(n).Job // a lane of jobs (or a matrix): its logs are its jobs'
			}
		}
		dst, err := lane.ArchiveCheck(root, n, or(what, "x"))
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		plans = append(plans, plan{n: n, what: what, dst: dst})
	}
	if len(problems) > 0 {
		msg := "nothing archived:\n  " + strings.Join(problems, "\n  ")
		return errors.New(msg)
	}
	for _, pl := range plans {
		dst, err := lane.Archive(root, pl.n, pl.what)
		if err != nil {
			return err
		}
		status.Update(root, pl.n, cfg.Lanes, func(l *status.Lane) {
			l.ResetRun()
			l.State, l.Round, l.Job = status.Idle, "", ""
			l.LastArchive = filepath.Base(dst)
		})
		archived, _ := logparse.ParseFile(dst)
		history.Log(root, history.Entry{Event: history.Archive, Lane: pl.n, Job: archived.Job, Detail: filepath.Base(dst)})
		fmt.Printf("archived %s -> %s\n", rel(root, lane.Log(root, pl.n)), rel(root, dst))
	}
	return nil
}

func cmdStub(args []string) error {
	var force bool
	args, err := flags{bools: map[string]*bool{"force": &force}}.parse(args)
	if err != nil {
		return err
	}
	if len(args) < 2 {
		return usagef(`usage: swim stub N "<message>" [--force]`)
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	n, err := laneRef(root, cfg, args[0], false)
	if err != nil {
		return err
	}
	if err := refuseAlways(root, n, "stub", force); err != nil {
		return err
	}
	if err := refuseRecurring(root, n, "stub", force); err != nil {
		return err
	}
	msg := strings.Join(args[1:], " ")
	replaced, _ := lane.ReadScript(root, n)
	content, err := assets.Stub(n, msg)
	if err != nil {
		return err
	}
	if err := lane.WriteScript(root, n, content, true); err != nil {
		return err
	}
	status.Update(root, n, cfg.Lanes, func(l *status.Lane) { l.Pending, l.PendingJob = "", "" })
	history.Log(root, history.Entry{Event: history.Stub, Lane: n, Job: replaced.Job, Detail: msg})
	fmt.Printf("stubbed %s (swim %d): %s\n", rel(root, lane.Script(root, n)), n, msg)
	return nil
}

// refuseAlways keeps archive and stub off an Always lane, which is the
// repo's standing setup, not a disposable round, unless forced.
func refuseAlways(root string, n int, verb string, force bool) error {
	if info, _ := lane.ReadScript(root, n); info.Always && !force {
		return fmt.Errorf("swim %d is an Always lane (standing setup, not a round); %s it anyway with --force", n, verb)
	}
	return nil
}

func cmdNote(args []string) error {
	var ref string
	rest, err := flags{strs: map[string]*string{"lane": &ref}}.parse(args)
	if err != nil {
		return err
	}
	text := strings.TrimSpace(strings.Join(rest, " "))
	if text == "" {
		return usagef(`usage: swim note [--lane N|JOB] "<text>"`)
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	text = cfg.Masker(os.Environ()).String(text)
	e := history.Entry{Event: history.Note, Detail: text}
	if ref != "" {
		if e.Lane, err = laneRef(root, cfg, ref, true); err != nil {
			return err
		}
		if info, _ := lane.ReadScript(root, e.Lane); info.Job != "" {
			e.Job = info.Job
		} else if st, _ := status.Load(root); st != nil && st.Get(e.Lane) != nil {
			e.Job = st.Get(e.Lane).Job
		}
	}
	if err := history.Append(root, e); err != nil {
		return err
	}
	fmt.Printf("noted in %s\n", history.FileName)
	return nil
}

func rel(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return p
}

// swim lock [--upgrade]
func cmdLock(args []string) error {
	var upgrade bool
	rest, err := flags{bools: map[string]*bool{"upgrade": &upgrade}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return usagef("usage: swim lock [--upgrade]")
	}
	root, _, err := repo()
	if err != nil {
		return err
	}
	l, ok, err := version.ReadLock(root)
	if err != nil {
		return err
	}
	if !upgrade {
		fmt.Printf("swim:  %s (syntax %s)\n", version.Long(), version.Current)
		if !ok {
			fmt.Printf("lock:  none (%s is written on the next swim run)\n", version.LockName)
			return nil
		}
		fmt.Printf("lock:  syntax %s, written by swim %s at %s\n", l.Syntaxes(), l.Version, l.UpdatedAt)
		if l.Mismatched() {
			return version.ErrMismatch{Lock: l, Path: version.LockPath(root)}
		}
		fmt.Println("ok:    this swim can run this repo's lanes")
		return nil
	}
	switch {
	case ok && l.Newer():
		return version.ErrMismatch{Lock: l, Path: version.LockPath(root)}
	case ok && !l.Mismatched() && l.Breaking == 0:
		fmt.Printf("%s already pins syntax %s\n", version.LockName, version.Current)
		return nil
	}
	for n := 1; n <= 99; n++ {
		if pid, running := lane.Running(root, n); running {
			return fmt.Errorf("swim %d is running (pid %d); let it finish before upgrading the lock", n, pid)
		}
	}
	if err := version.WriteLock(root); err != nil {
		return err
	}
	from := "none"
	if ok {
		from = l.Syntaxes().String()
		if l.Breaking != 0 {
			from = fmt.Sprintf("breaking %d (%s)", l.Breaking, from)
		}
	}
	history.Log(root, history.Entry{Event: history.Lock, Detail: fmt.Sprintf("syntax %s -> %s (swim %s)", from, version.Current, version.String())})
	fmt.Printf("%s: syntax %s -> %s\n", version.LockName, from, version.Current)
	return nil
}
