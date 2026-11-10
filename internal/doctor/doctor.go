// Package doctor checks what running swim on this machine needs: the swim
// on PATH, the config, git and bash and the tools lanes call, the
// toolchain, where notifications go, the repo's .gitignore block, and
// state swim left behind. Checks only read; Fix applies the safe local
// repairs (the .gitignore block, and leftover state).
package doctor

import (
	"context"
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

	"github.com/gates-brightly/swimlane/internal/chars"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/envfile"
	"github.com/gates-brightly/swimlane/internal/finding"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/manifest"
	"github.com/gates-brightly/swimlane/internal/proc"
	"github.com/gates-brightly/swimlane/internal/status"
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
	// ToolchainTimeout limits the toolchain check (default 10s).
	ToolchainTimeout time.Duration
	// NotifyTimeout limits the notify_url request (default 5s).
	NotifyTimeout time.Duration
	// BashVersion is bash's "major.minor" (default: asks the bash on PATH;
	// "" there means none).
	BashVersion string
	// GNU reports whether this machine's tool (sed, grep, ...) is the GNU
	// one (default: asks it with --version; timeout: whether it's on PATH).
	GNU func(tool string) bool
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
		o.ToolchainTimeout = 10 * time.Second
	}
	if o.NotifyTimeout == 0 {
		o.NotifyTimeout = 5 * time.Second
	}
	if o.BashVersion == "" {
		o.BashVersion = bashVersion()
	}
	if o.GNU == nil {
		o.GNU = gnuTool()
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
	{"swim-bin", false, "$SWIM_BIN, or the swim lanes find on PATH, isn't this swim", checkSwimBin},
	{"config-parse", false, "the config file doesn't load", checkConfigParse},
	{"config-key", false, "config.yml sets a key swim doesn't read (a typo, or a removed setting): it's ignored", checkConfigKeys},
	{"config-repo", false, "a config.yml repos: key or an env.swim [repo] section that's a path (repos are keyed by name now)", checkConfigRepo},
	{"git", false, "git missing, or not a git repo", checkGit},
	{"bash", false, "bash missing, or older than 4 while a lane uses a bash 4 feature (named by lane and line)", checkBash},
	{"gnu-only", false, "a lane uses a GNU-only tool form (sed -i, grep -P, date -d, ...) this machine's tools don't run (macOS/BSD)", checkGNUOnly},
	{"toolchain", false, "the configured toolchain fails to load", checkToolchain},
	{"notify", false, "notify_url doesn't answer, or curl or notify_command's program isn't there", checkNotifyTarget},
	{"notify-failing", false, "the last notification sent to notify_url (or notify_command) failed", checkNotifyFailing},
	{"gitignore", true, "swim's .gitignore block is missing or out of date", checkGitignore},
	{"tracked", false, "lane scripts or swim state tracked by git", checkTracked},
	{"env-file", false, "an env file that doesn't parse, or sets a lane's guard flag", checkEnvFiles},
	{"stale-pid", true, "a .swim/laneN.pid whose process is gone", checkStalePID},
	{"claim-expired", true, "a lane claim (.swim/claims/) past its lease", checkExpiredClaims},
	{"watch-gone", true, "a swim watch presence file (.swim/workers/) whose process is gone, or whose heartbeat stopped", checkWatchGone},
	{"leftover", true, "a file under .swim/ that swim no longer reads (hub.yml, headless.*, groups/, observations/, out/, checkpoints/, earmarks/, ...), or a .swim.lock an older swim wrote", checkLeftover},
	{"stub", true, "a lane.N.sh stub an older swim archive left (--fix deletes an untracked one)", checkStubs},
	{"stages-old", true, "staged lane copies in .swim/runs/<run>/ older than 14 days, of no lane's latest round", checkOldStages},
	{"remote-running", false, "a lane has run on another machine sharing the repo for over a day: this one can't tell whether that machine is still there", checkRemoteRunning},
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
// files, expired claims, dead watches' presence files, old pinned copies,
// files under .swim/ swim no longer reads, an untracked .swim.lock, and
// untracked archive stubs) and returns what it did. It checks each again before removing it, and never
// touches config, lane scripts other than stubs, git, lane logs, the chain, status.yml, the queue, .swim/scripts
// or snapshots.
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
		case "claim-expired", "watch-gone", "leftover", "stub":
			ok, err := o.removeLeftover(f)
			if err != nil {
				return done, err
			}
			if ok {
				done = append(done, "removed "+f.File)
			}
		case "stages-old":
			if removed := manifest.PruneStages(o.Root, stageKeep, o.keepRun()); len(removed) > 0 {
				done = append(done, fmt.Sprintf("removed the pinned copies of %d old runs from .swim/runs/", len(removed)))
			}
		}
	}
	return done, nil
}

// keepRun keeps the run of each lane's latest round (swim verify checks its
// pinned copy).
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

// stageKeep is how long a run's staged lane copies are kept once no
// lane's latest round is theirs.
const stageKeep = 14 * 24 * time.Hour

func checkOldStages(o *Options) []finding.Finding {
	old := manifest.OldStages(o.Root, stageKeep, o.keepRun())
	if len(old) == 0 {
		return nil
	}
	return []finding.Finding{fd(finding.Info, "stages-old", ".swim/runs/", "swim doctor --fix (removes them)",
		"%d runs' staged lane copies are older than 14 days and no lane's latest round", len(old))}
}

func checkEnvFiles(o *Options) []finding.Finding {
	var out []finding.Finding
	reported := map[string]bool{}
	for n := 1; n <= o.Cfg.Lanes; n++ {
		info, err := lane.ReadScript(o.Root, n)
		if err != nil || !info.Exists {
			info = lane.Info{}
		}
		r, err := envfile.Resolve(o.Root, n)
		if err != nil {
			if !reported[err.Error()] {
				reported[err.Error()] = true
				out = append(out, fd(finding.Error, "env-file", "", "fix the line: KEY=VALUE, '...' for literal text, no export or $", "%v", err))
			}
			continue
		}
		for _, c := range envfile.GuardConflicts(r, info) {
			out = append(out, fd(finding.Error, "env-file", c.File, "remove the line; approve with the shell or the prompt",
				"%s (lane %d)", c.Error(), n))
		}
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

// checkConfigKeys: config.yml is read loosely, so a key swim doesn't
// read (a typo, a removed setting) is silently ignored.
func checkConfigKeys(o *Options) []finding.Finding {
	if o.CfgErr != nil {
		return nil // config-parse says why it doesn't load
	}
	path := o.Cfg.Path
	if path == "" {
		path = config.Path()
	}
	keys, err := config.UnknownKeys(path)
	if err != nil {
		return nil // config-parse says why it doesn't load
	}
	var out []finding.Finding
	for _, k := range keys {
		where := "at the top level"
		if k.Section != "" {
			where = "under " + k.Section
		}
		f := fd(finding.Warn, "config-key", path, "delete it, or correct its spelling (swim guide config lists the settings)",
			"unknown key %s %s: swim ignores it", k.Key, where)
		f.Line = k.Line
		out = append(out, f)
	}
	return out
}

// checkConfigRepo points out config.yml repos: keys and env.swim [repo]
// sections that are paths, as swim keyed repos once: they match nothing
// now.
func checkConfigRepo(o *Options) []finding.Finding {
	if o.CfgErr != nil {
		return nil
	}
	path := o.Cfg.Path
	if path == "" {
		path = config.Path()
	}
	keys, err := config.PathKeys(path)
	if err != nil {
		return nil
	}
	var out []finding.Finding
	for _, k := range keys {
		out = append(out, fd(finding.Warn, "config-repo", path, "rename the key to the repo's name (swim config shows it)",
			"repos: key %s is a path; key by repo name (e.g. %s)", k, config.RepoName(k)))
	}
	for _, k := range envfile.PathSections() {
		out = append(out, fd(finding.Warn, "config-repo", envfile.UserPath(), "rename the section to the repo's name (swim config shows it)",
			"[repo %s] is a path; key by repo name (e.g. [repo %s])", k, config.RepoName(k)))
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
	// A repo whose committed CI runs swim ci runs committed rounds:
	// tracked lane scripts are intended there; swim state isn't.
	ciRepo := runsSwimCI(o.Root)
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

// runsSwimCI reports whether the repo at root has a GitHub Actions
// workflow or a .gitlab-ci.yml that runs swim ci.
func runsSwimCI(root string) bool {
	files, _ := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.y*ml"))
	for _, f := range append(files, filepath.Join(root, ".gitlab-ci.yml")) {
		if data, err := os.ReadFile(f); err == nil && strings.Contains(string(data), "swim ci") {
			return true
		}
	}
	return false
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
			"lane %d's pid file names process %s, which is gone (swim status shows the lane as \"running?\")", n, pid))
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
			"lane %d has been %s on %s for %s (pid %d there); this machine can't tell whether it still is", l.Lane, l.State, l.Host, time.Since(t).Round(time.Hour), l.PID))
	}
	return out
}
