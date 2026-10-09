package cli

import (
	"fmt"
	"os"
	"slices"

	"github.com/gates-brightly/swimlane/internal/assets"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/doctor"
	"github.com/gates-brightly/swimlane/internal/finding"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/launcher"
	"github.com/gates-brightly/swimlane/internal/lint"
	"github.com/gates-brightly/swimlane/internal/status"
)

// swim lint [N|JOB ...] [--strict] [--yaml]
func cmdLint(args []string) error {
	var strict, asYAML bool
	rest, err := flags{bools: map[string]*bool{"strict": &strict, "yaml": &asYAML}}.parse(args)
	if err != nil {
		return err
	}
	// Read-only: no log or syntax migration.
	root, cfg, err := repoNoMigrate()
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
	fs, err := lint.Run(root, cfg, lanes)
	if err != nil {
		return err
	}
	if asYAML {
		if err := finding.WriteYAML(os.Stdout, lint.Schema, fs); err != nil {
			return err
		}
	} else {
		finding.WriteText(os.Stdout, fs)
	}
	if code := finding.ExitCode(fs, strict); code != 0 {
		return exitError{code}
	}
	return nil
}

// swim doctor [--fix] [--strict] [--yaml]
func cmdDoctor(args []string) error {
	var fix, strict, asYAML bool
	rest, err := flags{bools: map[string]*bool{"fix": &fix, "strict": &strict, "yaml": &asYAML}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return usagef("doctor takes no arguments")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root := config.RepoRoot(cwd)
	// A broken config is a finding, not a failure.
	cfg, cfgErr := config.Load(root)
	o := doctor.Options{
		Root: root, Cwd: cwd, Cfg: cfg, CfgErr: cfgErr, Self: self(),
		GitignoreBegin: gitignoreBegin, GitignoreEnd: gitignoreEnd, GitignoreBody: assets.Gitignore,
		FixGitignore: mergeGitignore,
	}
	fs := doctor.Run(o)
	if fix {
		done, err := doctor.Fix(o, fs)
		for _, d := range done {
			fmt.Fprintln(os.Stderr, "fixed: "+d)
		}
		if err != nil {
			return err
		}
		if len(done) == 0 {
			fmt.Fprintln(os.Stderr, "fixed: nothing (--fix only rewrites swim's .gitignore block and removes stale pid files)")
		}
		fs = doctor.Run(o)
	}
	finding.Sort(fs)
	if asYAML {
		if err := finding.WriteYAML(os.Stdout, doctor.Schema, fs); err != nil {
			return err
		}
	} else {
		finding.WriteText(os.Stdout, fs)
		if !fix {
			for _, f := range fs {
				if doctor.Fixable(f.Code) {
					fmt.Println("run `swim doctor --fix` to apply the safe fixes (swim's .gitignore block, stale pid files)")
					break
				}
			}
		}
	}
	if code := finding.ExitCode(fs, strict); code != 0 {
		return exitError{code}
	}
	return nil
}

// lintPreflight is swim run's pre-flight: it refuses to start (exit 2) when
// a lane about to run fails a lint check swim run doesn't enforce elsewhere
// (set -e, a missing or wrong lane_init). Blocked commands, After: and
// cycles are left to the launcher, which already refuses them.
func lintPreflight(root string, cfg *config.Config, named []int, rerun bool) error {
	var lanes []int
	st, _ := status.Load(root)
	for n := 1; n <= cfg.Lanes; n++ {
		if len(named) > 0 && !slices.Contains(named, n) {
			continue
		}
		info, err := lane.ReadScript(root, n)
		if err != nil || !info.Pending() {
			continue
		}
		if len(named) == 0 && !rerun && st != nil && launcher.AlreadyPassed(info, st.Get(n)) {
			continue
		}
		lanes = append(lanes, n)
	}
	if len(lanes) == 0 {
		return nil
	}
	fs, err := lint.Errors(root, cfg, lanes)
	if err != nil || len(fs) == 0 {
		return err
	}
	finding.WriteText(os.Stderr, fs)
	fmt.Fprintln(os.Stderr, "swim run: refusing to start: fix the errors above (swim lint shows every check), or add `# swim:lint-ignore <code> <reason>`")
	return exitError{2}
}
