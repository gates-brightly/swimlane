// Package doctor checks swim's environment: the binary and PATH, the
// config, the repo's .gitignore block and git state, stale pid files, the
// toolchain and the version lock. Checks only read; Fix applies the two
// safe local repairs (the .gitignore block and stale pid files).
package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/chars"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/envfile"
	"github.com/gates-brightly/swimlane/internal/finding"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/manifest"
	"github.com/gates-brightly/swimlane/internal/outputs"
	"github.com/gates-brightly/swimlane/internal/proc"
	"github.com/gates-brightly/swimlane/internal/redact"
	"github.com/gates-brightly/swimlane/internal/sigfile"
	"github.com/gates-brightly/swimlane/internal/sign"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/trust"
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
		o.Cfg = &config.Config{Root: o.Root, Settings: config.Settings{Lanes: 1}}
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
	{"lanes-setting", false, "a `lanes:` setting (swim finds the lanes in use; it's ignored)", checkLanesSetting},
	{"removed-setting", false, "a setting swim no longer has (chime, schedule_tz, observe, modules, secrets, ambient_credentials, secrets_key, headless, hub; it's ignored)", checkRemovedSettings},
	{"gitignore", true, "swim's .gitignore block is missing or out of date", checkGitignore},
	{"tracked", false, "lane scripts or swim state tracked by git", checkTracked},
	{"stale-pid", true, "a .swim/laneN.pid whose process is gone", checkStalePID},
	{"toolchain", false, "the configured toolchain fails to load", checkToolchain},
	{"git", false, "git missing, or not a git repo", checkGit},
	{"header-env-secret", false, "header_env lists a secret", checkHeaderEnvSecret},
	{"lock", false, ".swim.lock missing, invalid or for another breaking version", checkLock},
	{"env-file", false, "an env file that doesn't parse, or sets a lane's guard flag", checkEnvFiles},
	{"outputs-old", true, "round outputs in .swim/out/ older than outputs_keep that no lane uses", checkOldOutputs},
	{"anchors-none", false, "the repo has a chain of custody but no chain_anchors to checkpoint it to", checkAnchorsNone},
	{"checkpoint-unsent", false, "a chain anchor hasn't had the latest checkpoint", checkCheckpointUnsent},
	{"trust-unpinned", false, "trust.yml isn't pinned here, or changed since the pin with no signed change", checkTrustPin},
	{"stages-old", true, "staged lane copies in .swim/runs/<run>/ older than outputs_keep, of no lane's latest round", checkOldStages},
	{"signing-key", false, "where the runner key lives: warns when it's a file with no passphrase (a lane could read it and sign as the runner)", checkSigningKey},
	{"standing-approval", false, "a standing approval from before they expired and bound lookup.yml's rules (it still counts)", checkStandingApprovals},
	{"remote-running", false, "a lane has run on another machine sharing the repo for over a day: this one can't tell whether that machine is still there", checkRemoteRunning},
	{"pause-long", false, "a lane has been paused (swim pause) longer than pause_warn, holding its locks and slot; or the repo-wide pause is left on", checkPauseLong},
	{"cache-dir", false, "the cache directory (cache_dir) is a link, isn't yours, or others can write it: lanes with # Cache: won't start", checkCacheDir},
	{"cache-unused", false, "tool caches unused for 30 days, or the repo's caches over cache_max", checkCacheUnused},
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
// files, plain .lane.N.rc files moved into .env.swim) and returns what it
// did. It never touches config, lane scripts or git.
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
		case "outputs-old":
			if removed := outputs.Prune(o.Root, o.Cfg.OutputsKeepFor(), o.keepRound()); len(removed) > 0 {
				done = append(done, fmt.Sprintf("removed %d old round output dirs from .swim/out/", len(removed)))
			}
		case "stages-old":
			if removed := manifest.PruneStages(o.Root, o.Cfg.OutputsKeepFor(), o.keepRun()); len(removed) > 0 {
				done = append(done, fmt.Sprintf("removed the staged copies of %d old runs from .swim/runs/", len(removed)))
			}
		}
	}
	return done, nil
}

// keepRound keeps each lane's latest round's outputs (what input reads).
func (o *Options) keepRound() func(job, run string) bool {
	st, _ := status.Load(o.Root)
	return func(job, run string) bool {
		if st == nil {
			return false
		}
		for _, l := range st.Lanes {
			if l.Job == job && l.Run == run {
				return true
			}
		}
		return false
	}
}

// keepRun keeps the run of each lane's latest round (swim verify checks its
// staged copy).
func (o *Options) keepRun() func(run string) bool {
	st, _ := status.Load(o.Root)
	return func(run string) bool {
		if st == nil {
			return false
		}
		for _, l := range st.Lanes {
			if l.Run == run {
				return true
			}
		}
		return false
	}
}

func checkOldStages(o *Options) []finding.Finding {
	old := manifest.OldStages(o.Root, o.Cfg.OutputsKeepFor(), o.keepRun())
	if len(old) == 0 {
		return nil
	}
	return []finding.Finding{fd(finding.Info, "stages-old", ".swim/runs/", "swim doctor --fix (removes them)",
		"%d runs' staged lane copies are older than outputs_keep (%s) and no lane's latest round", len(old), o.Cfg.OutputsKeepFor())}
}

func checkOldOutputs(o *Options) []finding.Finding {
	old := outputs.Stale(o.Root, o.Cfg.OutputsKeepFor(), o.keepRound())
	if len(old) == 0 {
		return nil
	}
	return []finding.Finding{fd(finding.Info, "outputs-old", ".swim/out/", "swim doctor --fix (removes them)",
		"%d round output dirs are older than outputs_keep (%s) and no lane's latest round", len(old), o.Cfg.OutputsKeepFor())}
}

func checkEnvFiles(o *Options) []finding.Finding {
	var out []finding.Finding
	lk, _ := lookup.Load(o.Root)
	reported := map[string]bool{}
	for n := 1; n <= o.Cfg.Lanes; n++ {
		info, err := lane.ReadScript(o.Root, n)
		if err != nil || !info.Exists {
			info = lane.Info{}
		}
		r, err := envfile.Resolve(o.Root, o.Cfg, n)
		if err != nil {
			if !reported[err.Error()] {
				reported[err.Error()] = true
				out = append(out, fd(finding.Error, "env-file", "", "fix the line: KEY=VALUE, '...' for literal text, no export or $", "%v", err))
			}
			continue
		}
		for _, c := range envfile.GuardConflicts(r, info, lk) {
			out = append(out, fd(finding.Error, "env-file", c.File, "remove the line; approve with the shell, the prompt or swim approve",
				"%s (swim %d)", c.Error(), n))
		}
	}
	return out
}

func toVars(entries []envfile.Entry, file string) []envfile.Var {
	out := make([]envfile.Var, len(entries))
	for i, e := range entries {
		out[i] = envfile.Var{Key: e.Key, Value: e.Value, File: file, Line: e.Line}
	}
	return out
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
			out = append(out, fd(finding.Warn, "binary-platform", o.rel(p), "reinstall swim (just install, or go install ...)", "can't read it as a binary: %v", err))
			continue
		}
		ok, emulated := Runs(plats, o.Host)
		switch {
		case !ok:
			out = append(out, fd(finding.Error, "binary-platform", o.rel(p),
				fmt.Sprintf("rebuild it for %s (e.g. just build, or GOOS=%s GOARCH=%s go build ./cmd/swim)", o.Host, strings.Split(o.Host, "/")[0], strings.Split(o.Host, "/")[1]),
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
	return []finding.Finding{fd(finding.Warn, "path-missing", "", "just link or just install, and put its directory on PATH",
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
	return []finding.Finding{fd(finding.Warn, "path-multiple", "", "remove the ones you don't use, or reorder PATH (just which shows where each came from)",
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

func checkLanesSetting(o *Options) []finding.Finding {
	if !o.Cfg.RepoLanes {
		return nil
	}
	file := o.Cfg.Path
	if o.Cfg.RepoFile != "" {
		file = o.Cfg.RepoFile
	}
	return []finding.Finding{fd(finding.Warn, "lanes-setting", file, "remove `lanes:` (from swim.yml, the repo's config section or defaults)",
		"`lanes:` is no longer used: swim shows the lanes in use (a script, a current log or a running process), and `swim archive N` drops one")}
}

// checkRemovedSettings points out keys swim.yml or config.yml's defaults
// still set though swim no longer has them: read and ignored.
func checkRemovedSettings(o *Options) []finding.Finding {
	var out []finding.Finding
	for _, src := range []struct {
		file string
		keys []string
	}{{o.Cfg.RepoFile, o.Cfg.Repo.RemovedKeys()}, {o.Cfg.Path, o.Cfg.Settings.RemovedKeys()}} {
		if src.file == "" || len(src.keys) == 0 {
			continue
		}
		out = append(out, fd(finding.Warn, "removed-setting", src.file, "delete "+strings.Join(src.keys, ", "),
			"%s: swim no longer has these settings; they're ignored", strings.Join(src.keys, ", ")))
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
			"no swim block; lane scripts and .swim/ would show up in git status")}
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

// laneFile reports whether p is a lane script: lane.N, or lane.N.EXT.
func laneFile(p string) bool {
	rest, ok := strings.CutPrefix(p, "lane.")
	if !ok {
		return false
	}
	n, ext, hasExt := strings.Cut(rest, ".")
	return chars.Digits(n) && (!hasExt || ext != "" && chars.All(ext, chars.Alnum))
}

// tracked reports whether p is a lane script or swim state.
func tracked(p string) bool {
	return laneFile(p) || strings.HasPrefix(p, ".lane") && strings.HasSuffix(p, ".rc") || strings.HasPrefix(p, ".swim/")
}

func checkTracked(o *Options) []finding.Finding {
	cmd := exec.Command("git", "ls-files", "--", "lane.[0-9]*", ".swim")
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
		if tracked(l) && !(ciRepo && laneFile(l)) {
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

func pidLane(name string) (int, bool) {
	ln, ok := chars.Between(name, "lane", ".pid")
	if !ok || !chars.Digits(ln) {
		return 0, false
	}
	n, err := strconv.Atoi(ln)
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
		pid := strings.TrimSpace(string(data))
		if f := strings.Fields(pid); len(f) > 0 {
			pid = f[0] // pid and start time
		}
		out = append(out, fd(finding.Warn, "stale-pid", o.rel(lane.PIDFile(o.Root, n)), "swim doctor --fix (removes it), then swim status --rebuild",
			"swim %d's pid file names process %s, which is gone (swim status shows the lane as \"running?\")", n, pid))
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
			"no %s yet; the first swim run writes it, pinning syntax %s", version.LockName, version.Current)}
	case l.Mismatched():
		first, _, _ := strings.Cut(version.ErrMismatch{Lock: l, Path: version.LockPath(o.Root)}.Error(), "\n")
		return []finding.Finding{fd(finding.Warn, "lock", version.LockName, "see swim lock --help", "%s", strings.TrimSuffix(first, ","))}
	case l.Breaking != 0:
		return []finding.Finding{fd(finding.Info, "lock", version.LockName, "swim lock --upgrade (rewrites it; nothing else changes)",
			"%s is in the old form (breaking: %d); it means syntax %s", version.LockName, l.Breaking, l.Syntaxes())}
	}
	return nil
}

// checkStandingApprovals warns of the standing approvals still counting
// under the old terms: never expiring (until lookup.yml sets a
// standing.max), or not bound to lookup.yml's rules.
func checkStandingApprovals(o *Options) []finding.Finding {
	lk, err := lookup.Load(o.Root)
	if err != nil {
		return nil
	}
	files, _ := sigfile.All(o.Root)
	var out []finding.Finding
	for _, f := range files {
		if f.Kind != sigfile.Standing {
			continue
		}
		st := f.State(lk, time.Now(), sigfile.Current{})
		for _, w := range st.Warnings {
			out = append(out, fd(finding.Warn, "standing-approval", o.rel(f.Path),
				"swim revoke --auto "+f.For+", then swim approve --auto "+f.For+" again (or set standing.max in lookup.yml)",
				"standing approval for %s's jobs: %s", f.For, w))
		}
	}
	return out
}

// checkTrustPin: swim verify trusts trust.yml's keys as pinned in the
// operator's config. Not pinned yet, the next verify pins whatever trust.yml
// says then (trust on first use); changed with no signed change leading to
// it, verify reports trusted: no.
func checkTrustPin(o *Options) []finding.Finding {
	tf, err := trust.Load(o.Root)
	if err != nil {
		return []finding.Finding{fd(finding.Error, "trust-unpinned", trust.FileName, "fix the file", "%v", err)}
	}
	if tf == nil {
		return nil
	}
	pin, err := trust.LoadPin(o.Root)
	if err != nil {
		return []finding.Finding{fd(finding.Warn, "trust-unpinned", trust.PinsPath(), "fix or remove the file", "%v", err)}
	}
	switch res := trust.Check(o.Root, tf, pin); res.Status {
	case trust.TOFU:
		return []finding.Finding{fd(finding.Warn, "trust-unpinned", trust.FileName, "check its keys, then swim keys pin",
			"trust.yml isn't pinned on this machine: the next swim verify trusts whatever it lists then")}
	case trust.Unsigned, trust.Malformed:
		return []finding.Finding{fd(finding.Error, "trust-unpinned", trust.FileName, "find out who changed it; swim keys pin accepts it as it is",
			"%s: swim verify reports trusted: no", res.Why)}
	}
	return nil
}

// checkAnchorsNone: without an anchor, entries removed from the end of the
// chain, or a chain replaced wholesale, can't be told from the real one.
func checkAnchorsNone(o *Options) []finding.Finding {
	if len(o.Cfg.ChainAnchors) > 0 {
		return nil
	}
	if st, err := os.Stat(filepath.Join(o.Root, ".swim", "chain.jsonl")); err != nil || st.Size() == 0 {
		return nil
	}
	return []finding.Finding{fd(finding.Info, "anchors-none", "", "add chain_anchors: [file:/path/outside/the/repo] to swim.yml or config.yml",
		"no chain_anchors: swim verify can't catch entries removed from the end of the chain")}
}

// checkCheckpointUnsent: the newest checkpoint (.swim/checkpoints.jsonl)
// didn't reach an anchor (.swim/checkpoints.sent.yml). The next one is
// sent anyway; until then that anchor proves less.
func checkCheckpointUnsent(o *Options) []finding.Finding {
	data, err := os.ReadFile(filepath.Join(o.Root, ".swim", "checkpoints.jsonl"))
	if err != nil || len(o.Cfg.ChainAnchors) == 0 {
		return nil
	}
	ls := lines(string(data))
	if len(ls) == 0 {
		return nil
	}
	var last struct {
		Seq int    `json:"seq"`
		At  string `json:"at"`
	}
	if json.Unmarshal([]byte(ls[len(ls)-1]), &last) != nil {
		return nil
	}
	sent := map[string]int{}
	if data, err := os.ReadFile(filepath.Join(o.Root, ".swim", "checkpoints.sent.yml")); err == nil {
		yaml.Unmarshal(data, &sent)
	}
	var out []finding.Finding
	for _, a := range o.Cfg.ChainAnchors {
		if sent[a] != last.Seq {
			out = append(out, fd(finding.Warn, "checkpoint-unsent", "", "check the anchor is reachable, then swim chain checkpoint",
				"chain anchor %s hasn't had the latest checkpoint (seq %d, %s); %s", a, last.Seq, last.At, lastSent(sent[a])))
		}
	}
	return out
}

// lastSent says what an anchor last took: a send that failed clears it.
func lastSent(seq int) string {
	if seq == 0 {
		return "the last send to it failed, or none was made"
	}
	return fmt.Sprintf("its last is seq %d", seq)
}

// checkSigningKey says where the runner key lives. swim run signs a run's
// entries with a run key held in its memory, which the runner key vouches
// for once; a lane runs as the operator, so what stands between it and a
// signature swim verify trusts is the runner key itself.
func checkSigningKey(o *Options) []finding.Finding {
	if o.Cfg.SigningMode() == config.SigningOff {
		return nil
	}
	k, err := sign.LoadKey(o.Cfg.SigningKey)
	if err != nil {
		return nil
	}
	switch sign.Storage(k) {
	case sign.InFile:
		return []finding.Finding{fd(finding.Warn, "signing-key", k.Path, "ssh-add -c "+k.Path+", delete the file and set signing_key to "+strings.TrimSuffix(k.Path, ".pub")+".pub; or a security key (ssh-keygen -t ed25519-sk)",
			"the runner key is a file with no passphrase: a lane runs as you, so it can read it and sign chain entries swim verify trusts")}
	case sign.InLockedFile, sign.InAgent:
		return []finding.Finding{fd(finding.Info, "signing-key", k.Path, "ssh-add -c (swim run then asks once per run), or a security key",
			"the runner key signs through ssh-agent: a lane can ask the agent too, unless it confirms each use")}
	}
	return []finding.Finding{fd(finding.Info, "signing-key", k.Path, "",
		"the runner key is a security key: each signature needs it (swim run signs once per run)")}
}

// remoteRunningFor is how long a lane may run on another machine before
// checkRemoteRunning asks whether that machine is still there.
const remoteRunningFor = 24 * time.Hour

// checkRemoteRunning: a lane claimed by another machine sharing the repo
// is never reaped here (this machine can't see its process), so if that
// machine is gone for good the lane stays running until someone releases
// it. Over a day of it is worth a look.
func checkRemoteRunning(o *Options) []finding.Finding {
	st, _ := status.Load(o.Root)
	if st == nil {
		return nil
	}
	var out []finding.Finding
	for _, l := range st.Lanes {
		if proc.Local(l.Host) || l.PID <= 0 || l.State != status.Running && l.State != status.Waiting {
			continue
		}
		if l.StartedAt == nil {
			continue
		}
		t, err := time.Parse(time.RFC3339, *l.StartedAt)
		if err != nil || time.Since(t) < remoteRunningFor {
			continue
		}
		out = append(out, fd(finding.Warn, "remote-running", o.rel(lane.PIDFile(o.Root, l.Lane)),
			fmt.Sprintf("if %s is gone for good: rm .swim/lane%d.pid, then swim status --rebuild", l.Host, l.Lane),
			"swim %d has been %s on %s for %s (pid %d there); this machine can't tell whether it still is", l.Lane, l.State, l.Host, time.Since(t).Round(time.Hour), l.PID))
	}
	return out
}

// checkPauseLong: a pause never ends on its own, and a paused lane keeps
// its locks and its slot, so one left paused past pause_warn (default 4h)
// is worth a look; so is a repo-wide pause that long, which keeps every
// worker from claiming batches. It only warns.
func checkPauseLong(o *Options) []finding.Finding {
	limit := 4 * time.Hour
	if o.Cfg != nil {
		limit = o.Cfg.PauseWarnFor()
	}
	var out []finding.Finding
	if st, _ := status.Load(o.Root); st != nil {
		for _, l := range st.Lanes {
			if l.PausedAt == "" || !l.Alive() {
				continue
			}
			t, err := time.Parse(time.RFC3339, l.PausedAt)
			if err != nil || time.Since(t) < limit {
				continue
			}
			what := "holding"
			if len(l.Locks) > 0 {
				what += " locks " + strings.Join(l.Locks, ", ") + " and"
			}
			out = append(out, fd(finding.Warn, "pause-long", ".swim/status.yml", fmt.Sprintf("swim unpause %d, or swim interrupt %d to end it", l.Lane, l.Lane),
				"swim %d has been paused for %s by %s (after %q), %s its slot", l.Lane, time.Since(t).Round(time.Minute), l.PausedBy, l.PausedAfter, what))
		}
	}
	if r, on := lane.RepoPause(o.Root); on {
		if t, err := time.Parse(time.RFC3339, r.At); err == nil && time.Since(t) >= limit {
			out = append(out, fd(finding.Warn, "pause-long", ".swim/pause", "swim unpause --all",
				"the repo has been paused for %s (swim pause --all by %s): no worker claims a batch", time.Since(t).Round(time.Minute), r.By))
		}
	}
	return out
}
