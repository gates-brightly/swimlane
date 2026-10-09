package cli

import (
	"bufio"
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
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/launcher"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/policy"
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
		fmt.Println("updated .gitignore (lane.[0-9]*.sh, .lane*.rc, .swim/, .swim.log)")
	} else {
		fmt.Println(".gitignore already has the swim block")
	}

	if err := refreshStatus(root, cfg); err != nil {
		return err
	}
	history.Log(root, history.Entry{Event: history.Init, Detail: fmt.Sprintf("lanes=%d", cfg.Lanes)})
	fmt.Printf("lanes: swim 1..%d   status: %s   project log: %s\n", cfg.Lanes, rel(root, status.Path(root)), history.FileName)
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
	return status.UpdateFile(root, cfg.Lanes, func(f *status.File) error {
		for i := range f.Lanes {
			l := &f.Lanes[i]
			info, err := lane.ReadScript(root, l.Lane)
			if err != nil {
				return err
			}
			l.Pending, l.PendingJob = "", ""
			if info.Pending() {
				l.Pending, l.PendingJob = info.Round, info.Job
			}
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
		// --lanes N is an alias for `swim config lanes N`.
		if len(rest) > 0 {
			return usagef("use either --lanes N or `swim config lanes N`, not both")
		}
		if _, err := strconv.Atoi(setLanes); err != nil {
			return usagef("--lanes needs a number, got %q", setLanes)
		}
		rest = []string{"lanes", setLanes}
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
	if !slices.Contains(config.Keys, key) {
		return usagef("unknown config key %q; keys: %s", key, strings.Join(config.Keys, ", "))
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
		"effective":    cfg.Settings,
	})
	fmt.Print(string(out))
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

// setConfigKey writes one operator setting: under defaults:, or this
// repo's section with --repo. lanes always goes to the repo's section
// (lane numbers are per repo) and keeps the pending-round guard.
func setConfigKey(key, value string, toRepo bool) error {
	if key == "lanes" {
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("lanes must be a number 1..99, got %q", value)
		}
		root, cfg, err := repo()
		if err != nil {
			return err
		}
		return applyLanes(root, cfg, n, "")
	}
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
	var job string
	rest, err := flags{bools: map[string]*bool{"force": &force}, strs: map[string]*string{"job": &job}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) < 2 {
		return usagef(`usage: swim new N "<round goal>"`)
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
	if job == "" {
		job = lane.NewJobID()
	} else if !lane.ValidJobID(job) {
		return usagef("job id %q: use 8-64 letters, digits, '.', '_' or '-' (not all digits)", job)
	}
	for m := 1; m <= cfg.Lanes; m++ {
		if info, _ := lane.ReadScript(root, m); m != n && info.Pending() && strings.EqualFold(info.Job, job) {
			return fmt.Errorf("job %s is already in lane.%d.sh; job ids must be unique", job, m)
		}
	}
	content, err := assets.Script(n, goal, cfg.Toolchain, job, os.Getenv("USER"))
	if err != nil {
		return err
	}
	if err := lane.WriteScript(root, n, content, force); err != nil {
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
}

func (rf *runFlags) parse(args []string) ([]string, error) {
	return flags{
		bools: map[string]*bool{"plain": &rf.plain, "rerun": &rf.rerun, "chime": &rf.chime, "no-chime": &rf.noChime,
			"yaml": &rf.yaml, "yaml-output": &rf.yamlOutput},
		strs: map[string]*string{"run-id": &rf.runID, "parallel": &rf.parallel},
	}.parse(args)
}

func cmdRun(args []string) error {
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
	if len(rest) == 0 {
		if cfg, err = offerMoreLanes(root, cfg, rerun, !rf.yaml); err != nil {
			return err
		}
	}
	var lanes []int
	for _, a := range rest {
		n, err := laneRef(root, cfg, a, false)
		if err != nil {
			return err
		}
		lanes = append(lanes, n)
	}
	if err := refreshStatus(root, cfg); err != nil {
		return err
	}
	// Pre-flight: lint's error-level checks the launcher doesn't enforce (lint.go).
	if err := lintPreflight(root, cfg, lanes, rerun); err != nil {
		return err
	}
	o := launcher.Options{
		Root: root, Cfg: cfg, Lanes: lanes, Plain: plain, Rerun: rerun,
		Self: self(), Out: os.Stdout, Stdin: os.Stdin, RunID: rf.runID, Parallel: parallel,
		Finished: chimeWhenDone(cfg, rf),
	}
	if rf.yaml {
		o.YAML, o.YAMLOutput = os.Stdout, rf.yamlOutput
	}
	code, err := launcher.Run(o)
	if err != nil {
		return err
	}
	if code != 0 {
		return exitError{code}
	}
	return nil
}

// offerMoreLanes looks for lane scripts numbered above the configured lane
// count (lane.5.sh with lanes: 4) holding jobs that haven't passed. On a
// terminal it asks whether to raise the lane count so they run; otherwise
// it only says how, and never changes config.
func offerMoreLanes(root string, cfg *config.Config, rerun, ask bool) (*config.Config, error) {
	waiting := launcher.Beyond(root, cfg, rerun)
	var lines []string
	for _, n := range waiting {
		info, _ := lane.ReadScript(root, n)
		lines = append(lines, fmt.Sprintf("  lane.%d.sh  %s", n, info.Round))
	}
	if len(waiting) == 0 {
		return cfg, nil
	}
	want := waiting[len(waiting)-1]
	if want > 99 {
		fmt.Fprintf(os.Stderr, "swim: lane scripts above lane.99.sh are never run\n")
		return cfg, nil
	}
	p := ui.Painter{On: ui.ColorEnabled(os.Stdout)}
	head := fmt.Sprintf("%d job(s) waiting beyond swim %d (only %d lanes are configured):", len(waiting), cfg.Lanes, cfg.Lanes)
	if !ask || !ui.IsTTY(os.Stdin) || !ui.IsTTY(os.Stdout) {
		fmt.Fprintln(os.Stderr, "swim: "+head)
		fmt.Fprintln(os.Stderr, strings.Join(lines, "\n"))
		fmt.Fprintf(os.Stderr, "swim: they won't run. To include them: swim config --lanes %d\n", want)
		return cfg, nil
	}
	fmt.Println(p.Paint(ui.Yellow+ui.Bold, head))
	fmt.Println(strings.Join(lines, "\n"))
	fmt.Printf("Increase lanes from %d to %d so they run now? [y/N] ", cfg.Lanes, want)
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		if err := applyLanes(root, cfg, want, "jobs waiting in lane "+joinNums(waiting)); err != nil {
			return nil, err
		}
		return config.Load(root)
	}
	fmt.Printf("keeping %d lanes; %s won't run (swim config --lanes %d to include them later)\n", cfg.Lanes, joinNums(waiting), want)
	return cfg, nil
}

// applyLanes sets this repo's lane count in config and records it.
func applyLanes(root string, cfg *config.Config, n int, why string) error {
	if n == cfg.Lanes {
		fmt.Printf("lanes already %d\n", n)
		return nil
	}
	if n < cfg.Lanes {
		for k := n + 1; k <= cfg.Lanes; k++ {
			if info, _ := lane.ReadScript(root, k); info.Pending() {
				return fmt.Errorf("lane.%d.sh holds a pending round; stub it before reducing lanes to %d", k, n)
			}
		}
	}
	if err := config.SetLanes(cfg.Path, root, n); err != nil {
		return err
	}
	detail := fmt.Sprintf("%d -> %d", cfg.Lanes, n)
	if why != "" {
		detail += "  (" + why + ")"
	}
	history.Log(root, history.Entry{Event: history.Lanes, Detail: detail})
	fmt.Printf("lanes: %d -> %d in %s\n", cfg.Lanes, n, cfg.Path)
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
func cmdPlan(args []string) error {
	var rerun, asYAML bool
	rest, err := flags{bools: map[string]*bool{"rerun": &rerun, "yaml": &asYAML}}.parse(args)
	if err != nil {
		return err
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
	o := launcher.Options{Root: root, Cfg: cfg, Lanes: lanes, Rerun: rerun}
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
	return launcher.WritePlan(os.Stdout, o, ui.ColorEnabled(os.Stdout))
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
	var fresh, snap, noRetryTimeout bool
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
		bools: map[string]*bool{"new": &fresh, "snapshot": &snap, "no-retry-timeout": &noRetryTimeout},
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
		cfg = &config.Config{Root: root, Settings: config.Settings{Lanes: config.DefaultLanes}}
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
	// lane_init exports the round's deadline (Timeout:) as epoch seconds.
	var deadline time.Time
	if d, err := strconv.ParseInt(os.Getenv("SWIM_DEADLINE"), 10, 64); err == nil && d > 0 {
		deadline = time.Unix(d, 0)
	}
	logPath := os.Getenv("STEP_LOG")
	if logPath == "" {
		logPath = "step.log"
	}
	res, err := step.Run(step.Options{
		Args: cmdArgs, Label: label, New: fresh, Snapshot: snap,
		LogPath: logPath, Root: root, Lane: n, Lanes: cfg.Lanes,
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		Color:    ui.ColorEnabled(os.Stderr),
		Deadline: deadline, TimeoutText: os.Getenv("SWIM_TIMEOUT"),
		StepTimeout: stepTimeout, StepTimeoutText: stepText, Retry: rt,
		Blocked: cfg.Blocked(),
		Mask:    cfg.Masker(os.Environ()),
	})
	if err != nil {
		return err
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

func cmdArchive(args []string) error {
	if len(args) < 1 {
		return usagef("usage: swim archive N|JOB [<what>]")
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	n, err := laneRef(root, cfg, args[0], true)
	if err != nil {
		return err
	}
	what := strings.Join(args[1:], " ")
	if what == "" {
		// Default to the job id of the round being archived.
		if r, _ := logparse.ParseFile(lane.Log(root, n)); r.Job != "" {
			what = r.Job
		} else {
			return usagef("swim %d's log has no job id; give a description: swim archive %d <what>", n, n)
		}
	}
	dst, err := lane.Archive(root, n, what)
	if err != nil {
		return err
	}
	status.Update(root, n, cfg.Lanes, func(l *status.Lane) {
		l.ResetRun()
		l.State, l.Round, l.Job = status.Idle, "", ""
		l.LastArchive = filepath.Base(dst)
	})
	archived, _ := logparse.ParseFile(dst)
	history.Log(root, history.Entry{Event: history.Archive, Lane: n, Job: archived.Job, Detail: filepath.Base(dst)})
	fmt.Printf("archived %s -> %s\n", rel(root, lane.Log(root, n)), rel(root, dst))
	return nil
}

func cmdStub(args []string) error {
	if len(args) < 2 {
		return usagef(`usage: swim stub N "<message>"`)
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	n, err := laneRef(root, cfg, args[0], false)
	if err != nil {
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
		fmt.Printf("swim:  %s (breaking version %d)\n", version.Long(), version.Breaking)
		if !ok {
			fmt.Printf("lock:  none (%s is written on the next swim run)\n", version.LockName)
			return nil
		}
		fmt.Printf("lock:  breaking version %d, written by swim %s at %s\n", l.Breaking, l.Version, l.UpdatedAt)
		if l.Breaking != version.Breaking {
			return version.ErrMismatch{Lock: l, Path: version.LockPath(root)}
		}
		fmt.Println("ok:    this swim can run this repo's lanes")
		return nil
	}
	switch {
	case ok && l.Breaking > version.Breaking:
		return version.ErrMismatch{Lock: l, Path: version.LockPath(root)}
	case ok && l.Breaking == version.Breaking:
		fmt.Printf("%s already pins breaking version %d\n", version.LockName, l.Breaking)
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
		from = fmt.Sprint(l.Breaking)
	}
	history.Log(root, history.Entry{Event: history.Lock, Detail: fmt.Sprintf("breaking %s -> %d (swim %s)", from, version.Breaking, version.String())})
	fmt.Printf("%s: breaking version %s -> %d\n", version.LockName, from, version.Breaking)
	return nil
}
