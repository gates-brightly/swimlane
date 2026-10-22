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
	"regexp"
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
	"github.com/gates-brightly/swimlane/internal/hub"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/manifest"
	"github.com/gates-brightly/swimlane/internal/outputs"
	"github.com/gates-brightly/swimlane/internal/proc"
	"github.com/gates-brightly/swimlane/internal/redact"
	"github.com/gates-brightly/swimlane/internal/secrets"
	"github.com/gates-brightly/swimlane/internal/sigfile"
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
	// Headless is what swim knows of this repo's headless worker (swim
	// watch --headless); nil skips the headless checks.
	Headless *Headless
	// Scheduled lists the scheduled batches waiting ("#3 swim 1 at
	// 13:30:00(CDT) ..."), and WorkerUp whether a swim watch runs here
	// (swim schedule; nil when not known).
	Scheduled []string
	WorkerUp  func() bool
	// Recurring are recurring schedules that need a look: paused, or
	// expiring within three days.
	Recurring []string
	// Reclaim is what swim prune --older 30d would free (nil: not known).
	Reclaim func() (items int, bytes int64)
}

// Headless is a repo's headless worker as the CLI finds it.
type Headless struct {
	Running bool        // a process holds .swim/headless.lock
	PID     int         // the lock holder's pid
	Stale   int         // .swim/headless.pid names this process, which is gone or holds no lock (0: none)
	Units   [][2]string // installed service files: kind (systemd, launchd), path
	// HubCheck reaches the hub the repo is enrolled with (nil: not
	// enrolled), with a short timeout.
	HubCheck func() error
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
	{"gitignore", true, "swim's .gitignore block is missing or out of date", checkGitignore},
	{"tracked", false, "lane scripts or swim state tracked by git", checkTracked},
	{"stale-pid", true, "a .swim/laneN.pid whose process is gone", checkStalePID},
	{"toolchain", false, "the configured toolchain fails to load", checkToolchain},
	{"git", false, "git missing, or not a git repo", checkGit},
	{"header-env-secret", false, "header_env lists a secret", checkHeaderEnvSecret},
	{"lock", false, ".swim.lock missing, invalid or for another breaking version", checkLock},
	{"env-file", false, "an env file that doesn't parse, or sets a lane's guard flag", checkEnvFiles},
	{"lane-rc", true, "a deprecated .lane.N.rc (env files replace it)", checkLaneRC},
	{"outputs-old", true, "round outputs in .swim/out/ older than outputs_keep that no lane uses", checkOldOutputs},
	{"hub-http", false, "enrolled with a hub over plain http on another machine", checkHubHTTP},
	{"anchors-none", false, "the repo has a chain of custody but no chain_anchors to checkpoint it to", checkAnchorsNone},
	{"checkpoint-unsent", false, "a chain anchor hasn't had the latest checkpoint", checkCheckpointUnsent},
	{"trust-unpinned", false, "trust.yml isn't pinned here, or changed since the pin with no signed change", checkTrustPin},
	{"stages-old", true, "staged lane copies in .swim/runs/<run>/ older than outputs_keep, of no lane's latest round", checkOldStages},
	{"guarded-secrets", false, "lists swim.yml's guarded secrets; warns when the secrets key has no passphrase (a lane could decrypt them itself)", checkGuardedSecrets},
	{"ambient-credential", false, "a credential swim.yml lists in ambient_credentials is configured on this machine (~/.aws)", checkAmbientCredentials},
	{"standing-approval", false, "a standing approval from before they expired and bound lookup.yml's rules (it still counts)", checkStandingApprovals},
	{"headless-service", false, "a headless worker installed as a service or running (swim watch --headless)", checkHeadlessService},
	{"headless-stale", false, "a .swim/headless.pid left by a headless worker that's gone (killed?)", checkHeadlessStale},
	{"hub-unreachable", false, "a headless worker's hub can't be reached: guards waiting for an answer through it decline", checkHubReachable},
	{"headless-hub-answers", false, "swim.yml's headless.approvals is hub: a hub user's button click approves guards no require: rule names", checkHeadlessHubAnswers},
	{"headless-yes", false, "a headless service runs with -y: it approves every guard in every batch", checkHeadlessYes},
	{"remote-running", false, "a lane has run on another machine sharing the repo for over a day: this one can't tell whether that machine is still there", checkRemoteRunning},
	{"pause-long", false, "a lane has been paused (swim pause) longer than pause_warn, holding its locks and slot; or the repo-wide pause is left on", checkPauseLong},
	{"schedule-no-worker", false, "batches are scheduled, but no swim watch runs to start them", checkScheduleNoWorker},
	{"schedule-recurring", false, "a recurring schedule is paused, or expires within three days", checkScheduleRecurring},
	{"prune-reclaim", false, "swim prune --older 30d would free over 100 MB", checkPruneReclaim},
	{"cache-dir", false, "the cache directory (cache_dir) is a link, isn't yours, or others can write it: lanes with # Cache: won't start", checkCacheDir},
	{"module-go", false, "a lane uses a Go module from the repo, and there's no Go toolchain here to build it", checkModuleGo},
	{"knowledge-retired", false, "knowledge someone accepted (knowledge/steps.yml) that the runs no longer show", checkKnowledgeRetired},
	{"module-fetched", false, "a fetched module .swim.lock pins is missing from .swim/modules/ or changed there (swim modules get), or no lane names it", checkModuleFetched},
	{"module-version", false, "a lane uses a built-in module .swim.lock pins at another version than this swim's, or that no swim modules lock has pinned", checkModuleVersion},
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
		case "lane-rc":
			did, err := migrateRC(&o, f.File)
			if err != nil {
				return done, err
			}
			if did != "" {
				done = append(done, did)
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
			for _, c := range l.Cells {
				if c.Run == run {
					return true
				}
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
	return []finding.Finding{fd(finding.Info, "stages-old", ".swim/runs/", "swim doctor --fix (removes them; the run records stay)",
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

// rcLane is the lane of a .lane.N.rc file name ("" if it isn't one).
func rcLane(name string) string {
	if n, ok := chars.Between(name, ".lane.", ".rc"); ok && chars.Digits(n) {
		return n
	}
	return ""
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

func checkLaneRC(o *Options) []finding.Finding {
	files, _ := filepath.Glob(filepath.Join(o.Root, ".lane.*.rc"))
	var out []finding.Finding
	for _, f := range files {
		ln := rcLane(filepath.Base(f))
		if ln == "" {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		fix := "swim doctor --fix (moves it into .env.swim [lane " + ln + "])"
		if _, problems := envfile.MigrateRC(data); len(problems) > 0 {
			fix = "move it to .env.swim by hand: " + problems[0]
		}
		out = append(out, fd(finding.Warn, "lane-rc", filepath.Base(f), fix,
			"%s is deprecated: env files replace it, and a guard flag it sets no longer approves", filepath.Base(f)))
	}
	return out
}

// migrateRC moves a plain .lane.N.rc into a [lane N] section of .env.swim,
// keeping a copy under .swim/migrations/. One that sets a guard flag, or
// isn't plain assignments, is left alone.
func migrateRC(o *Options, name string) (string, error) {
	ln := rcLane(name)
	if ln == "" {
		return "", nil
	}
	n, _ := strconv.Atoi(ln)
	if _, running := lane.Running(o.Root, n); running {
		return "", nil
	}
	path := filepath.Join(o.Root, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil
	}
	entries, problems := envfile.MigrateRC(data)
	if len(problems) > 0 {
		return "", nil
	}
	info, _ := lane.ReadScript(o.Root, n)
	lk, _ := lookup.Load(o.Root)
	if c := envfile.GuardConflicts(&envfile.Resolved{Vars: toVars(entries, name)}, info, lk); len(c) > 0 {
		return "", fmt.Errorf("%s sets guard flag %s: env files can't approve guards, so it isn't moved; remove that line and approve another way, then run swim doctor --fix again", name, c[0].Key)
	}
	bak := filepath.Join(o.Root, ".swim", "migrations", time.Now().UTC().Format("20060102T150405Z"), name)
	if err := os.MkdirAll(filepath.Dir(bak), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(bak, data, 0o600); err != nil {
		return "", err
	}
	if err := envfile.AppendLane(o.Root, n, entries, "moved from "+name); err != nil {
		return "", err
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return fmt.Sprintf("moved %s into .env.swim [lane %d] (original in %s)", name, n, o.rel(bak)), nil
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

// laneFile reports whether p is a lane script: lane.N, or lane.N.EXT.
func laneFile(p string) bool {
	rest, ok := strings.CutPrefix(p, "lane.")
	if !ok {
		return false
	}
	n, ext, hasExt := strings.Cut(rest, ".")
	return chars.Digits(n) && (!hasExt || ext != "" && chars.All(ext, chars.Alnum))
}

// tracked reports whether p is a lane script, a .lane*.rc or swim state.
func tracked(p string) bool {
	return laneFile(p) || strings.HasPrefix(p, ".lane") && strings.HasSuffix(p, ".rc") || strings.HasPrefix(p, ".swim/")
}

func checkTracked(o *Options) []finding.Finding {
	cmd := exec.Command("git", "ls-files", "--", "lane.[0-9]*", ".lane*.rc", ".swim")
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

// checkHubHTTP warns about an enrollment (.swim/hub.yml) that talks to a
// hub on another machine over plain http: the tokens, log tails and
// grants cross the network unencrypted. Enrollments made before swim
// refused that still work; this is where they surface.
func checkHubHTTP(o *Options) []finding.Finding {
	path := filepath.Join(o.Root, ".swim", "hub.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var h struct{ URL string }
	if yaml.Unmarshal(data, &h) != nil || !hub.PlainRemote(h.URL) {
		return nil
	}
	return []finding.Finding{fd(finding.Warn, "hub-http", o.rel(path),
		"serve the hub with --tls-cert/--tls-key (or behind a TLS proxy), then swim hub enroll https://... again",
		"enrolled with %s over plain http: its tokens, log tails and secrets cross the network unencrypted", h.URL)}
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
	return []finding.Finding{fd(finding.Info, "anchors-none", "", "add chain_anchors: [file:/path/outside/the/repo] (or hub) to swim.yml or config.yml",
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

// checkHeadlessService says what of a headless worker is set up here: the
// service files installed, and whether one runs.
func checkHeadlessService(o *Options) []finding.Finding {
	h := o.Headless
	if h == nil {
		return nil
	}
	var out []finding.Finding
	for _, u := range h.Units {
		out = append(out, fd(finding.Info, "headless-service", u[1], "swim watch --headless --uninstall removes it",
			"a %s service runs this repo's headless worker", u[0]))
	}
	if h.Running {
		out = append(out, fd(finding.Info, "headless-service", ".swim/headless.lock", "swim watch --headless --status | --stop",
			"a headless worker runs here, pid %d", h.PID))
	}
	return out
}

// checkGuardedSecrets lists swim.yml's guarded secrets, and warns when the
// secrets key has no passphrase: a lane runs as the operator, so it could
// read the key and decrypt secrets.yml itself, guards or not.
func checkGuardedSecrets(o *Options) []finding.Finding {
	g := o.Cfg.GuardedSecrets()
	if len(g) == 0 {
		return nil
	}
	var names []string
	for n, p := range g {
		names = append(names, fmt.Sprintf("%s (%s)", n, strings.Join(p.Guard, " or ")))
	}
	sort.Strings(names)
	out := []finding.Finding{fd(finding.Info, "guarded-secrets", "swim.yml", "",
		"guarded secrets, released to a lane only once its guard is approved: %s", strings.Join(names, ", "))}
	f, err := secrets.Load(secrets.Path(config.Path()))
	if err != nil || f.Empty(secrets.RepoKey(o.Root)) {
		return out
	}
	if k, err := secrets.LoadKey(o.Cfg.SecretsKeyPath()); err == nil && secrets.Unprotected(k) {
		out = append(out, fd(finding.Warn, "guarded-secrets", k.Path, "ssh-keygen -p -f "+k.Path+" (swim run asks for the passphrase once per run)",
			"the secrets key has no passphrase: a lane could decrypt secrets.yml itself, guarded secrets included"))
	}
	return out
}

// checkHeadlessStale: a headless worker that was killed leaves its pidfile
// (the lock goes with the process).
func checkHeadlessStale(o *Options) []finding.Finding {
	if o.Headless == nil || o.Headless.Stale == 0 {
		return nil
	}
	return []finding.Finding{fd(finding.Warn, "headless-stale", ".swim/headless.pid", "swim watch --headless --stop removes it; start the worker again if it should run",
		"the headless worker that wrote it (pid %d) is gone: was it killed? Nothing answers this repo's queue from the hub", o.Headless.Stale)}
}

// checkHubReachable: a headless worker with no hub has no one to answer
// its guards (they decline) or send it commands.
func checkHubReachable(o *Options) []finding.Finding {
	h := o.Headless
	if h == nil || h.HubCheck == nil || !h.Running && len(h.Units) == 0 {
		return nil
	}
	if err := h.HubCheck(); err != nil {
		return []finding.Finding{fd(finding.Warn, "hub-unreachable", ".swim/hub.yml", "check the hub runs and the network; swim hub status",
			"the headless worker's hub is unreachable (%v): guards waiting for an answer through it decline, and it takes no commands", err)}
	}
	return nil
}

// checkHeadlessHubAnswers: under headless.approvals: hub the decision on a
// guard no require: rule names rests on the hub's login, not a signature.
func checkHeadlessHubAnswers(o *Options) []finding.Finding {
	if o.Cfg.HeadlessApprovals() != config.HeadlessHub || !o.Cfg.HeadlessAllows(config.HubAnswer) {
		return nil
	}
	return []finding.Finding{fd(finding.Warn, "headless-hub-answers", o.rel(o.Cfg.RepoFile), "headless.approvals: signed, or a require: rule in lookup.yml for the guards that matter",
		"headless.approvals: hub: a hub user's button click approves any guard that no lookup.yml require: rule names (recorded as hub-button, unsigned)")}
}

// checkHeadlessYes: -y written into a service runs forever.
func checkHeadlessYes(o *Options) []finding.Finding {
	if o.Headless == nil {
		return nil
	}
	var out []finding.Finding
	for _, u := range o.Headless.Units {
		data, err := os.ReadFile(u[1])
		if err != nil || !unitHasYes(string(data)) {
			continue
		}
		out = append(out, fd(finding.Warn, "headless-yes", u[1], "swim watch --headless --install "+u[0]+" again without -y",
			"the %s service runs swim watch --headless -y: it approves every guard in every batch, with nobody watching", u[0]))
	}
	return out
}

// unitHasYes reports whether a unit file's command line passes -y.
func unitHasYes(text string) bool {
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "ExecStart="):
			for _, f := range strings.Fields(strings.TrimPrefix(l, "ExecStart=")) {
				if f = strings.Trim(f, `"'`); f == "-y" || f == "--yes" {
					return true
				}
			}
		case l == "<string>-y</string>" || l == "<string>--yes</string>":
			return true
		}
	}
	return false
}

// checkAmbientCredentials: a credential swim.yml names in
// ambient_credentials (an AWS profile with the access a guarded secret
// grants) that this machine has: every lane inherits it, guard or not.
func checkAmbientCredentials(o *Options) []finding.Finding {
	if len(o.Cfg.AmbientCredentials) == 0 {
		return nil
	}
	home, _ := os.UserHomeDir()
	files := []string{filepath.Join(home, ".aws", "credentials"), filepath.Join(home, ".aws", "config")}
	for _, k := range []string{"AWS_SHARED_CREDENTIALS_FILE", "AWS_CONFIG_FILE"} {
		if p := os.Getenv(k); p != "" {
			files = append(files, p)
		}
	}
	var out []finding.Finding
	for _, name := range o.Cfg.AmbientCredentials {
		for _, file := range files {
			data, err := os.ReadFile(file)
			if err != nil {
				continue
			}
			re := regexp.MustCompile(`(?m)^\s*\[(?:profile\s+)?` + regexp.QuoteMeta(name) + `\s*\]`)
			if re.Match(data) {
				out = append(out, fd(finding.Warn, "ambient-credential", file, "keep only read-only credentials on this machine; put write access in a guarded secret (swim.yml secrets:)",
					"profile %s (swim.yml ambient_credentials:) is configured here: every lane can use it without a guard", name))
				break
			}
		}
	}
	return out
}

// checkScheduleNoWorker: scheduled batches start only while a worker runs.
func checkScheduleNoWorker(o *Options) []finding.Finding {
	if len(o.Scheduled) == 0 || o.WorkerUp == nil || o.WorkerUp() {
		return nil
	}
	return []finding.Finding{fd(finding.Warn, "schedule-no-worker", ".swim/queue/", "swim watch (or swim watch --headless --detach)",
		"%d scheduled batches, and no swim watch runs here to start them: %s", len(o.Scheduled), strings.Join(o.Scheduled, "; "))}
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

// checkScheduleRecurring: a paused recurring schedule makes no
// occurrences; one about to expire needs scheduling again.
func checkScheduleRecurring(o *Options) []finding.Finding {
	var out []finding.Finding
	for _, w := range o.Recurring {
		out = append(out, fd(finding.Warn, "schedule-recurring", ".swim/queue/recurring/", "swim schedule list; swim schedule resume ID, or schedule it again", "%s", w))
	}
	return out
}

// reclaimThreshold is when checkPruneReclaim speaks up.
const reclaimThreshold = 100 << 20

// checkPruneReclaim: leftovers past 30 days (moved-aside scripts, consumed
// queue entries, old run stages) take room. swim doctor never prunes.
func checkPruneReclaim(o *Options) []finding.Finding {
	if o.Reclaim == nil {
		return nil
	}
	n, b := o.Reclaim()
	if b < reclaimThreshold {
		return nil
	}
	return []finding.Finding{fd(finding.Info, "prune-reclaim", ".swim/", "swim prune --older 30d (it asks; nothing goes without it)",
		"%d leftovers older than 30 days take %d MB: scripts swim clean moved aside, consumed queue entries, old run stages", n, b>>20)}
}
