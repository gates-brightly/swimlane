package cli

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/ci"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/launcher"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/version"
)

// cmdCi runs rounds in a CI job: swim all, non-interactive, with output for
// the CI log viewer (grouped, annotated, summarised) and optionally only the
// rounds a push or merge request changed.
func cmdCi(args []string) error {
	// --changed takes an optional value, which the flag parser can't express.
	changed, base := false, ""
	var rest0 []string
	for _, a := range args {
		switch {
		case a == "--changed":
			changed = true
		case strings.HasPrefix(a, "--changed="):
			changed, base = true, strings.TrimPrefix(a, "--changed=")
		default:
			rest0 = append(rest0, a)
		}
	}
	var junit, providerName, heartbeat, runID string
	var requireWork, rerun bool
	rest, err := flags{
		bools: map[string]*bool{"require-work": &requireWork, "rerun": &rerun},
		strs:  map[string]*string{"junit": &junit, "provider": &providerName, "heartbeat": &heartbeat, "run-id": &runID},
	}.parse(rest0)
	if err != nil {
		return err
	}
	if changed && len(rest) > 0 {
		return usagef("use --changed or name lanes, not both")
	}
	beat := 60 * time.Second
	if heartbeat != "" {
		if beat, err = time.ParseDuration(heartbeat); err != nil || beat < 0 {
			return usagef("--heartbeat takes a duration like 60s (0 turns it off), got %q", heartbeat)
		}
	}
	if runID != "" && !lane.ValidRunID(runID) {
		return usagef("--run-id %q: use 8-64 letters, digits, '.', '_' or '-' (not all digits)", runID)
	}
	p, err := ci.Detect(os.Getenv, providerName)
	if err != nil {
		return usagef("%v", err)
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	if err := requireVersion(root, true); err != nil {
		return err
	}
	out := os.Stdout

	// Lane scripts past the configured lanes: on a fresh runner with no
	// lanes setting anywhere, count them in; otherwise fail with the fix
	// (swim ci never prompts).
	lanesNote := ""
	if beyond := lane.ScriptsBeyond(root, cfg.Lanes); len(beyond) > 0 {
		top := beyond[len(beyond)-1]
		if !cfg.RepoLanes && top <= 99 {
			lanesNote = fmt.Sprintf(" (lanes: %d from the highest lane script; no lanes setting)", top)
			cfg.Lanes = top
			os.Setenv("SWIM_LANES", strconv.Itoa(top)) // for the lanes' own swim hooks
		} else {
			fmt.Fprintf(os.Stderr, "swim ci: lane.%d.sh is beyond the %d configured lanes and would not run.\n", top, cfg.Lanes)
			fmt.Fprintf(os.Stderr, "  fix: set `lanes: %d` in swim.yml (committed), or `swim config --lanes %d`\n", top, top)
			return exitError{2}
		}
	}

	// Which lanes, and why.
	why := "every pending round that hasn't passed"
	var named []int
	pins := map[int]string{}
	for _, a := range rest {
		n, err := laneRef(root, cfg, a, false)
		if err != nil {
			return err
		}
		named = append(named, n)
	}
	if len(rest) > 0 {
		why = "named: " + strings.Join(rest, " ")
	}
	if changed {
		b, err := ci.FindBase(os.Getenv, p, base)
		if err != nil {
			return usagef("%v", err)
		}
		why = "--changed: " + b.Why
		if !b.All {
			lanes, files, err := ci.ChangedLanes(root, b)
			if err != nil {
				return err
			}
			named = withUnpassedDeps(root, cfg, pendingOnly(root, lanes))
			if len(files) == 0 {
				why += "; no lane script changed"
			} else {
				why += "; changed: " + strings.Join(files, ", ")
			}
			if len(named) == 0 {
				return nothingToDo(p, why, requireWork)
			}
		}
	}
	if err := refreshStatus(root, cfg); err != nil {
		return err
	}
	o := launcher.Options{Root: root, Cfg: cfg, Lanes: named, Self: self(), Out: out, Plain: true,
		RunID: runID, Heartbeat: beat, Rerun: rerun && len(named) == 0}
	if rerun && !changed && len(rest) == 0 {
		why = "every pending round (--rerun: including ones that passed)"
	}
	sel, _, err := launcher.Select(o)
	if err != nil {
		return err
	}
	if len(sel) == 0 {
		return nothingToDo(p, why, requireWork)
	}
	// Run pinned: each selected lane must still hold the job it was
	// selected with when it starts.
	var guardsSet []string
	seenGuard := map[string]bool{}
	for _, n := range sel {
		info, _ := lane.ReadScript(root, n)
		if info.Job != "" {
			pins[n] = info.Job
		}
		for _, g := range info.Guards {
			if os.Getenv(g.Flag) == "1" && !seenGuard[g.Flag] {
				seenGuard[g.Flag] = true
				guardsSet = append(guardsSet, g.Flag)
			}
		}
	}
	sort.Strings(guardsSet)
	o.Lanes, o.Pins = sel, pins
	if o.RunID == "" {
		o.RunID = lane.NewRunID()
	}
	commit := gitRevParse(root, "HEAD")
	if commit != "" {
		os.Setenv("SWIM_COMMIT", commit) // tags the rounds, status and .swim.log
	}

	// Header.
	fmt.Fprintf(out, "swim ci · swim %s · provider %s\n", version.String(), p.Name())
	fmt.Fprintf(out, "commit %s  %s\n", or(commit, "-"), ciRef(root))
	fmt.Fprintf(out, "run %s · lanes %s%s\n", o.RunID, joinNums(sel), lanesNote)
	fmt.Fprintf(out, "selected: %s\n", why)
	if len(guardsSet) > 0 {
		fmt.Fprintf(out, "guard flags set (names only): %s\n", strings.Join(guardsSet, " "))
	} else {
		fmt.Fprintln(out, "guard flags set: none (destructive steps run as dry runs)")
	}
	fmt.Fprintln(out)

	code, err := launcher.Run(o)
	if err != nil {
		return err
	}

	// Reports, from the logs.
	reps := ci.Collect(root, sel, o.RunID)
	fmt.Fprintln(out)
	ci.Sections(out, p, reps)
	ci.Annotate(out, p, reps)
	if path := os.Getenv("GITHUB_STEP_SUMMARY"); path != "" && p.Name() == "github" {
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			ci.Markdown(f, reps, o.RunID, commit, code)
			f.Close()
		} else {
			fmt.Fprintf(os.Stderr, "swim ci: job summary: %v\n", err)
		}
	}
	if junit != "" {
		f, err := os.Create(junit)
		if err != nil {
			return err
		}
		if err := ci.JUnit(f, reps); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	artifacts := ".swim/logs/ .swim/snapshots/ .swim.log"
	if junit != "" {
		artifacts += " " + junit
	}
	fmt.Fprintf(out, "artifacts to upload: %s\n", artifacts)
	if code != 0 {
		return exitError{code}
	}
	return nil
}

// nothingToDo reports a run with no pending rounds: a notice, exit 0, or 1
// with --require-work.
func nothingToDo(p ci.Provider, why string, requireWork bool) error {
	p.Annotate(os.Stdout, ci.Notice, "swim ci: nothing to run", why)
	if requireWork {
		return exitError{1}
	}
	return nil
}

// pendingOnly keeps lanes whose script holds a pending round.
func pendingOnly(root string, lanes []int) []int {
	var out []int
	for _, n := range lanes {
		if info, _ := lane.ReadScript(root, n); info.Pending() {
			out = append(out, n)
		}
	}
	return out
}

// withUnpassedDeps adds, transitively, the lanes the given ones wait on
// whose pending round hasn't passed (they have to run first).
func withUnpassedDeps(root string, cfg *config.Config, lanes []int) []int {
	st, _ := status.Load(root)
	in := map[int]bool{}
	for _, n := range lanes {
		in[n] = true
	}
	for changed := true; changed; {
		changed = false
		var cur []int
		for n := range in {
			cur = append(cur, n)
		}
		sort.Ints(cur)
		deps, err := launcher.ResolveDeps(root, cfg, cur)
		if err != nil {
			break
		}
		for _, n := range cur {
			for _, d := range deps[n] {
				if in[d.Lane] {
					continue
				}
				info, _ := lane.ReadScript(root, d.Lane)
				var last *status.Lane
				if st != nil {
					last = st.Get(d.Lane)
				}
				if info.Pending() && !launcher.AlreadyPassed(info, last) {
					in[d.Lane], changed = true, true
				}
			}
		}
	}
	var out []int
	for n := range in {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// gitRevParse runs git rev-parse ARG (swim only reads git).
func gitRevParse(root string, arg ...string) string {
	c := exec.Command("git", "rev-parse", arg[0])
	if len(arg) == 2 {
		c = exec.Command("git", "rev-parse", arg[0], arg[1])
	}
	c.Dir = root
	b, err := c.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ciRef is the branch or merge request being built.
func ciRef(root string) string {
	for _, k := range []string{"GITHUB_HEAD_REF", "GITHUB_REF_NAME", "CI_MERGE_REQUEST_SOURCE_BRANCH_NAME", "CI_COMMIT_REF_NAME"} {
		if v := os.Getenv(k); v != "" {
			return "ref " + v
		}
	}
	if b := gitRevParse(root, "--abbrev-ref", "HEAD"); b != "" {
		return "branch " + b
	}
	return ""
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
