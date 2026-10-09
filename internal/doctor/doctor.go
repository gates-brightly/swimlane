// Package doctor checks swim's environment: the binary and PATH, the
// config, the repo's .gitignore block and git state, stale pid files, the
// toolchain and the version lock. Checks only read; Fix applies the two
// safe local repairs (the .gitignore block and stale pid files).
package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/finding"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/redact"
	"github.com/gates-brightly/swimlane/internal/version"
)

// Schema is the --yaml document's schema.
const Schema = "swim.doctor/v1"

// Options is what doctor inspects. Zero values fall back to the process's
// own (runtime.GOOS/GOARCH, $PATH, $SWIM_BIN).
type Options struct {
	Root   string
	Cwd    string
	Cfg    *config.Config // nil when the config didn't load
	CfgErr error
	Self   string // the running swim binary

	Host    string // GOOS/GOARCH; default runtime's
	PATH    string // default $PATH
	SwimBin string // default $SWIM_BIN
	// The .gitignore block swim init writes: markers and content.
	GitignoreBegin, GitignoreEnd, GitignoreBody string
	// FixGitignore rewrites the block (swim init's merge); used by Fix.
	FixGitignore func(path string) (bool, error)
	// ToolchainTimeout limits the toolchain check (default 30s).
	ToolchainTimeout time.Duration
}

func (o *Options) defaults() {
	if o.Host == "" {
		o.Host = runtime.GOOS + "/" + runtime.GOARCH
	}
	if o.PATH == "" {
		o.PATH = os.Getenv("PATH")
	}
	if o.SwimBin == "" {
		o.SwimBin = os.Getenv("SWIM_BIN")
	}
	if o.ToolchainTimeout == 0 {
		o.ToolchainTimeout = 30 * time.Second
	}
	if o.Cfg == nil {
		o.Cfg = &config.Config{Root: o.Root, Settings: config.Settings{Lanes: config.DefaultLanes}}
	}
}

// Check is one registered doctor check.
type Check struct {
	Code    string
	Fixable bool // swim doctor --fix repairs it
	Doc     string
	Run     func(o *Options) []finding.Finding
}

// Checks is the table of doctor checks, in report order.
var Checks = []Check{
	{"binary-platform", false, "a swim binary built for another OS or CPU (this one, bin/swim, each on PATH)", checkPlatform},
	{"path-missing", false, "no swim on PATH (lane scripts run with bash can't load the library)", checkPathMissing},
	{"path-multiple", false, "more than one swim on PATH", checkPathMultiple},
	{"swim-bin", false, "$SWIM_BIN, or the swim lanes find on PATH, isn't this swim", checkSwimBin},
	{"config-parse", false, "the config file doesn't load", checkConfigParse},
	{"config-repo", false, "the config has no section for this repo", checkConfigRepo},
	{"lanes-beyond", false, "lane scripts numbered above the configured lanes", checkLanesBeyond},
	{"gitignore", true, "swim's .gitignore block is missing or out of date", checkGitignore},
	{"tracked", false, "lane scripts or swim state tracked by git", checkTracked},
	{"stale-pid", true, "a .swim/laneN.pid whose process is gone", checkStalePID},
	{"toolchain", false, "the configured toolchain fails to load", checkToolchain},
	{"git", false, "git missing, or not a git repo", checkGit},
	{"header-env-secret", false, "header_env lists a secret", checkHeaderEnvSecret},
	{"lock", false, ".swim.lock missing, invalid or for another breaking version", checkLock},
}

// Run runs every check.
func Run(o Options) []finding.Finding {
	o.defaults()
	var out []finding.Finding
	for _, c := range Checks {
		out = append(out, c.Run(&o)...)
	}
	return out
}

// Fixable reports whether --fix repairs findings with this code.
func Fixable(code string) bool {
	for _, c := range Checks {
		if c.Code == code {
			return c.Fixable
		}
	}
	return false
}

// Fix applies the safe repairs for fs (the .gitignore block, stale pid
// files) and returns what it did. It never touches config, lane scripts or
// git.
func Fix(o Options, fs []finding.Finding) ([]string, error) {
	o.defaults()
	var done []string
	for _, f := range fs {
		switch f.Code {
		case "gitignore":
			if o.FixGitignore == nil || f.Fix == "" || !strings.Contains(f.Fix, "--fix") {
				continue
			}
			if changed, err := o.FixGitignore(filepath.Join(o.Root, ".gitignore")); err != nil {
				return done, err
			} else if changed {
				done = append(done, "rewrote swim's block in .gitignore")
			}
		case "stale-pid":
			n, ok := pidLane(filepath.Base(f.File))
			if !ok {
				continue
			}
			// Re-check: never remove the marker of a lane that is running now.
			if _, running := lane.Running(o.Root, n); running {
				continue
			}
			if err := os.Remove(lane.PIDFile(o.Root, n)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return done, err
			}
			done = append(done, "removed "+f.File)
		}
	}
	return done, nil
}

func fd(level, code, file, fix, format string, a ...any) finding.Finding {
	return finding.Finding{Level: level, Code: code, File: file, Message: fmt.Sprintf(format, a...), Fix: fix}
}

// rel shortens p relative to root for display.
func (o *Options) rel(p string) string {
	if r, err := filepath.Rel(o.Root, p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return p
}

// binaries lists the swim binaries doctor inspects: this one, bin/swim at
// the repo root, and each swim on PATH (deduplicated, in that order).
func (o *Options) binaries() []string {
	var out []string
	add := func(p string) {
		if p == "" {
			return
		}
		for _, q := range out {
			if q == p {
				return
			}
		}
		out = append(out, p)
	}
	add(o.Self)
	if st, err := os.Stat(filepath.Join(o.Root, "bin", "swim")); err == nil && !st.IsDir() {
		add(filepath.Join(o.Root, "bin", "swim"))
	}
	for _, p := range LookPathAll("swim", o.PATH) {
		add(p)
	}
	return out
}

func checkPlatform(o *Options) []finding.Finding {
	var out []finding.Finding
	for _, p := range o.binaries() {
		plats, err := Platform(p)
		if errors.Is(err, ErrScript) {
			continue
		}
		if err != nil {
			out = append(out, fd(finding.Warn, "binary-platform", o.rel(p), "reinstall swim (make install, or go install ...)", "can't read it as a binary: %v", err))
			continue
		}
		ok, emulated := Runs(plats, o.Host)
		switch {
		case !ok:
			out = append(out, fd(finding.Error, "binary-platform", o.rel(p),
				fmt.Sprintf("rebuild it for %s (e.g. make build, or GOOS=%s GOARCH=%s go build ./cmd/swim)", o.Host, strings.Split(o.Host, "/")[0], strings.Split(o.Host, "/")[1]),
				"built for %s, but this host is %s: running it fails with \"exec format error\"", strings.Join(plats, ", "), o.Host))
		case emulated:
			out = append(out, fd(finding.Info, "binary-platform", o.rel(p), "build a native arm64 swim",
				"built for %s; it runs on %s only through Rosetta", strings.Join(plats, ", "), o.Host))
		}
	}
	return out
}

func checkPathMissing(o *Options) []finding.Finding {
	if len(LookPathAll("swim", o.PATH)) > 0 {
		return nil
	}
	return []finding.Finding{fd(finding.Warn, "path-missing", "", "make link or make install, and put its directory on PATH",
		"no swim on PATH; a lane script run directly with bash can't load the library")}
}

func checkPathMultiple(o *Options) []finding.Finding {
	all := LookPathAll("swim", o.PATH)
	var distinct []string
	for _, p := range all {
		dup := false
		for _, q := range distinct {
			if sameFile(p, q) {
				dup = true
			}
		}
		if !dup {
			distinct = append(distinct, p)
		}
	}
	if len(distinct) < 2 {
		return nil
	}
	parts := []string{distinct[0] + " (runs)"}
	for _, p := range distinct[1:] {
		parts = append(parts, p+" (hidden)")
	}
	return []finding.Finding{fd(finding.Warn, "path-multiple", "", "remove the ones you don't use, or reorder PATH (make which shows where each came from)",
		"%d different swims on PATH, in order: %s", len(distinct), strings.Join(parts, ", "))}
}

func checkSwimBin(o *Options) []finding.Finding {
	if o.Self == "" {
		return nil
	}
	if o.SwimBin != "" {
		bin := o.SwimBin
		if !strings.Contains(bin, "/") {
			if found := LookPathAll(bin, o.PATH); len(found) > 0 {
				bin = found[0]
			}
		}
		if !sameFile(bin, o.Self) {
			return []finding.Finding{fd(finding.Warn, "swim-bin", "", "unset SWIM_BIN, or point it at the swim you mean to run",
				"$SWIM_BIN is %s, but this swim is %s; lanes run with bash would use $SWIM_BIN", o.SwimBin, o.Self)}
		}
		return nil
	}
	found := LookPathAll("swim", o.PATH)
	if len(found) > 0 && !sameFile(found[0], o.Self) {
		return []finding.Finding{fd(finding.Warn, "swim-bin", "", "run the swim on PATH, or put this one first on PATH",
			"the swim on PATH (%s) isn't this swim (%s); lane scripts run directly with bash load the library from it", found[0], o.Self)}
	}
	return nil
}

func checkConfigParse(o *Options) []finding.Finding {
	if o.CfgErr == nil {
		return nil
	}
	return []finding.Finding{fd(finding.Error, "config-parse", config.Path(), "fix the file (swim config --path shows where it is)",
		"config doesn't load: %v", o.CfgErr)}
}

func checkConfigRepo(o *Options) []finding.Finding {
	if o.CfgErr != nil || o.Cfg.HasRepo {
		return nil
	}
	return []finding.Finding{fd(finding.Warn, "config-repo", o.Cfg.Path, "swim init",
		"the config has no section for %s; built-in defaults apply", o.Root)}
}

func checkLanesBeyond(o *Options) []finding.Finding {
	var out []finding.Finding
	for _, n := range lane.ScriptsBeyond(o.Root, o.Cfg.Lanes) {
		out = append(out, fd(finding.Warn, "lanes-beyond", fmt.Sprintf("lane.%d.sh", n), fmt.Sprintf("swim config --lanes %d (or stub/remove the script)", n),
			"only %d lanes are configured, so swim %d never runs", o.Cfg.Lanes, n))
	}
	return out
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func checkGitignore(o *Options) []finding.Finding {
	if o.GitignoreBegin == "" {
		return nil
	}
	const file = ".gitignore"
	const fix = "swim doctor --fix (rewrites only swim's block)"
	data, err := os.ReadFile(filepath.Join(o.Root, file))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return []finding.Finding{fd(finding.Warn, "gitignore", file, "", "can't read it: %v", err)}
	}
	s := string(data)
	i := strings.Index(s, o.GitignoreBegin)
	if i < 0 {
		return []finding.Finding{fd(finding.Warn, "gitignore", file, fix,
			"no swim block; lane scripts, .lane*.rc and .swim/ would show up in git status")}
	}
	j := strings.Index(s[i:], o.GitignoreEnd)
	if j < 0 {
		return []finding.Finding{fd(finding.Warn, "gitignore", file, fmt.Sprintf("add %q after the block by hand", o.GitignoreEnd),
			"swim's block starts with %q but has no %q", o.GitignoreBegin, o.GitignoreEnd)}
	}
	have := lines(s[i+len(o.GitignoreBegin) : i+j])
	want := lines(o.GitignoreBody)
	in := func(list []string, x string) bool {
		for _, y := range list {
			if y == x {
				return true
			}
		}
		return false
	}
	var missing, extra []string
	for _, w := range want {
		// The help invites dropping .swim.log to commit the project log.
		if !in(have, w) && w != ".swim.log" {
			missing = append(missing, w)
		}
	}
	for _, h := range have {
		if !in(want, h) {
			extra = append(extra, h)
		}
	}
	if len(missing) == 0 && len(extra) == 0 {
		return nil
	}
	var why []string
	if len(missing) > 0 {
		why = append(why, "missing "+strings.Join(missing, " "))
	}
	if len(extra) > 0 {
		why = append(why, "has old "+strings.Join(extra, " "))
	}
	return []finding.Finding{fd(finding.Warn, "gitignore", file, fix,
		"swim's block is out of date (%s)", strings.Join(why, "; "))}
}

var (
	trackedRE  = regexp.MustCompile(`^(lane\.[0-9]+\.sh|\.lane.*\.rc|\.swim/.*)$`)
	laneFileRE = regexp.MustCompile(`^lane\.[0-9]+\.sh$`)
)

func checkTracked(o *Options) []finding.Finding {
	cmd := exec.Command("git", "ls-files", "--", "lane.*.sh", ".lane*.rc", ".swim")
	cmd.Dir = o.Root
	out, err := cmd.Output()
	if err != nil {
		return nil // checkGit reports a missing git or repo
	}
	// A repo with a committed swim.yml runs committed rounds in CI
	// (swim ci): tracked lane scripts are intended there; swim state isn't.
	ciRepo := o.Cfg != nil && o.Cfg.RepoFile != ""
	var files []string
	for _, l := range lines(string(out)) {
		if trackedRE.MatchString(l) && !(ciRepo && laneFileRE.MatchString(l)) {
			files = append(files, l)
		}
	}
	if len(files) == 0 {
		return nil
	}
	shown := files
	if len(shown) > 5 {
		shown = append(append([]string(nil), shown[:5]...), fmt.Sprintf("and %d more", len(files)-5))
	}
	return []finding.Finding{fd(finding.Warn, "tracked", "", "untrack them yourself: git rm --cached <files> (swim never writes to git)",
		"git tracks %s; lane scripts and swim state are per-operator and meant to be ignored", strings.Join(shown, ", "))}
}

var pidRE = regexp.MustCompile(`^lane([0-9]+)\.pid$`)

func pidLane(name string) (int, bool) {
	m := pidRE.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

func checkStalePID(o *Options) []finding.Finding {
	entries, err := os.ReadDir(filepath.Join(o.Root, ".swim"))
	if err != nil {
		return nil
	}
	var out []finding.Finding
	var ns []int
	for _, e := range entries {
		if n, ok := pidLane(e.Name()); ok && !e.IsDir() {
			ns = append(ns, n)
		}
	}
	sort.Ints(ns)
	for _, n := range ns {
		if _, running := lane.Running(o.Root, n); running {
			continue
		}
		data, _ := os.ReadFile(lane.PIDFile(o.Root, n))
		out = append(out, fd(finding.Warn, "stale-pid", o.rel(lane.PIDFile(o.Root, n)), "swim doctor --fix (removes it), then swim status --rebuild",
			"swim %d's pid file names process %s, which is gone (swim status shows the lane as \"running?\")", n, strings.TrimSpace(string(data))))
	}
	return out
}

func checkToolchain(o *Options) []finding.Finding {
	tc := strings.TrimSpace(o.Cfg.Toolchain)
	if tc == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), o.ToolchainTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", tc)
	cmd.Dir = o.Root
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	why := err.Error()
	if ctx.Err() != nil {
		why = fmt.Sprintf("timed out after %s", o.ToolchainTimeout)
	}
	if tail := lines(string(out)); len(tail) > 0 {
		why += ": " + tail[len(tail)-1]
	}
	return []finding.Finding{fd(finding.Error, "toolchain", "", "fix `toolchain` in the config (swim config --path), or check the version manager is installed",
		"toolchain %q fails to load (%s); every lane script would stop at it", tc, why)}
}

func checkGit(o *Options) []finding.Finding {
	if _, err := exec.LookPath("git"); err != nil {
		return []finding.Finding{fd(finding.Error, "git", "", "install git", "git is not on PATH; swim finds the repo root with git")}
	}
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = o.Cwd
	if err := cmd.Run(); err != nil {
		return []finding.Finding{fd(finding.Error, "git", "", "run swim inside a git repo (git init)",
			"%s is not in a git repo; swim keys config and state by the repo's root", o.Cwd)}
	}
	return nil
}

func checkHeaderEnvSecret(o *Options) []finding.Finding {
	ignored := map[string]bool{}
	for _, n := range o.Cfg.SecretEnvIgnore {
		ignored[n] = true
	}
	listed := map[string]bool{}
	for _, n := range o.Cfg.SecretEnv {
		listed[n] = true
	}
	auto := o.Cfg.SecretEnvAuto == nil || *o.Cfg.SecretEnvAuto
	var out []finding.Finding
	for _, n := range o.Cfg.HeaderEnv {
		if listed[n] || auto && redact.AutoName(n) && !ignored[n] {
			out = append(out, fd(finding.Warn, "header-env-secret", "", "remove it from header_env (swim config --path)",
				"header_env lists %s, a secret; step headers record it only as %s=***(len N)", n, n))
		}
	}
	return out
}

func checkLock(o *Options) []finding.Finding {
	l, ok, err := version.ReadLock(o.Root)
	switch {
	case err != nil:
		return []finding.Finding{fd(finding.Warn, "lock", version.LockName, "fix or delete it; swim run writes a new one", "%v", err)}
	case !ok:
		return []finding.Finding{fd(finding.Info, "lock", version.LockName, "commit it after the next swim run writes it",
			"no %s yet; the first swim run writes it, pinning breaking version %d", version.LockName, version.Breaking)}
	case l.Breaking != version.Breaking:
		first, _, _ := strings.Cut(version.ErrMismatch{Lock: l, Path: version.LockPath(o.Root)}.Error(), "\n")
		return []finding.Finding{fd(finding.Warn, "lock", version.LockName, "see swim lock --help", "%s", strings.TrimSuffix(first, ","))}
	}
	return nil
}
