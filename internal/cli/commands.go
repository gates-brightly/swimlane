package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"

	"swim/internal/assets"
	"swim/internal/config"
	"swim/internal/lane"
	"swim/internal/launcher"
	"swim/internal/logparse"
	"swim/internal/status"
	"swim/internal/step"
	"swim/internal/ui"
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
		fmt.Println("updated .gitignore (lane.[0-9]*.sh, agent*.log, .lane*.rc, .swim/)")
	} else {
		fmt.Println(".gitignore already has the swim block")
	}

	if err := refreshStatus(root, cfg); err != nil {
		return err
	}
	fmt.Printf("lanes: swim 1..%d   status: %s\n", cfg.Lanes, rel(root, status.Path(root)))
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
	var pathOnly bool
	rest, err := flags{bools: map[string]*bool{"path": &pathOnly}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return usagef("config takes no arguments")
	}
	if pathOnly {
		fmt.Println(config.Path())
		return nil
	}
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
	if !cfg.HasRepo {
		fmt.Println("# no section for this repo; run `swim init` to add one")
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
	content, err := assets.Script(n, goal, cfg.Toolchain, job)
	if err != nil {
		return err
	}
	if err := lane.WriteScript(root, n, content, force); err != nil {
		return err
	}
	status.Update(root, n, cfg.Lanes, func(l *status.Lane) {
		l.Pending, l.PendingJob = strings.Join(strings.Fields(goal), " "), job
	})
	fmt.Printf("wrote %s (swim %d): %s\n", rel(root, lane.Script(root, n)), n, goal)
	fmt.Printf("job: %s\n", job)
	fmt.Printf("edit its steps, then the operator runs: swim run %d   (or pinned: swim run %s)\n", n, job)
	return nil
}

func cmdRun(args []string) error {
	var plain bool
	rest, err := flags{bools: map[string]*bool{"plain": &plain}}.parse(args)
	if err != nil {
		return err
	}
	root, cfg, err := repo()
	if err != nil {
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
	if err := refreshStatus(root, cfg); err != nil {
		return err
	}
	code, err := launcher.Run(launcher.Options{
		Root: root, Cfg: cfg, Lanes: lanes, Plain: plain,
		Self: self(), Out: os.Stdout, Stdin: os.Stdin,
	})
	if err != nil {
		return err
	}
	if code != 0 {
		return exitError{code}
	}
	return nil
}

func cmdStep(args []string) error {
	var label string
	var fresh, snap bool
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
		bools: map[string]*bool{"new": &fresh, "snapshot": &snap},
		strs:  map[string]*string{"label": &label},
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
	logPath := os.Getenv("STEP_LOG")
	if logPath == "" {
		logPath = "step.log"
	}
	res, err := step.Run(step.Options{
		Args: cmdArgs, Label: label, New: fresh, Snapshot: snap,
		LogPath: logPath, Root: root, Lane: n, Lanes: cfg.Lanes,
		HeaderEnv: cfg.HeaderEnv, Runtime: cfg.Runtime,
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		Color: ui.ColorEnabled(os.Stderr),
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
	content, err := assets.Stub(n, msg)
	if err != nil {
		return err
	}
	if err := lane.WriteScript(root, n, content, true); err != nil {
		return err
	}
	status.Update(root, n, cfg.Lanes, func(l *status.Lane) { l.Pending, l.PendingJob = "", "" })
	fmt.Printf("stubbed %s (swim %d): %s\n", rel(root, lane.Script(root, n)), n, msg)
	return nil
}

func rel(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return p
}
