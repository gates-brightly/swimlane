// Package lint checks lane scripts before they run: their headers, the
// lane library's rules (no set -e, lane_init N, summary last, stages in
// order), bash 3.2 compatibility, guard flags, blocked commands, secrets and
// the dependency graph. It reads files and never changes them.
//
// Each check is a function registered in the checks table, with a stable
// code that findings carry and disable comments name:
//
//	some_command   # swim:lint-ignore CODE[,CODE] reason   (this line)
//	# swim:lint-ignore CODE reason                          (the next line)
//	# swim:lint-ignore-file CODE reason                     (the whole file)
//
// The reason is required; a disable comment without one is itself an error
// and disables nothing.
package lint

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/finding"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/launcher"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/syntax"
	"github.com/gates-brightly/swimlane/internal/trust"
)

// Schema is the --yaml document's schema.
const Schema = "swim.lint/v1"

// Script is one lane script being linted.
type Script struct {
	N    int
	Name string // lane.N.sh, lane.N.py, ...
	Src  string
	Info lane.Info
	// Lib: the script loads swim's bash library. Only those get the bash
	// checks; any other script (lane.N.py, a binary, plain bash) runs
	// inside swim's wrapper.
	Lib bool
	v   *view
}

// bashOnly are the checks that read a script written against the bash
// library. (A wrapped script's guards pass their action to swim guard
// itself, and its stages are checked as they run.)
var bashOnly = map[string]bool{
	"set-e": true, "lane-init": true, "summary": true, "stage": true, "stage-order": true, "bash4": true,
	"destructive": true, "blocked": true, "secret-echo": true, "cross-lane": true,
	"guard-unlisted": true, "guard-unused": true, "guard-dynamic": true, "check-unsafe": true,
	"decision-unlisted": true, "decision-unused": true, "decision-subshell": true, "decision-choices": true,
	"checkpoint": true,
}

// Context is what checks can see besides the script: the repo, its config
// and every lane script (for the dependency and cross-lane checks).
type Context struct {
	Root    string
	Cfg     *config.Config
	Scripts map[int]*Script // every lane 1..Lanes with a non-stub script

	depsDone bool
	deps     map[int][]int  // resolved dependencies (After: plus config deps)
	depErrs  map[int]string // lane -> why its After: didn't resolve
}

// Check is one registered lint check.
type Check struct {
	Code  string
	Level string // the level its findings carry
	// Preflight checks are refused by `swim run` too (only error-level checks
	// that the launcher doesn't already enforce some other way).
	Preflight bool
	Doc       string
	Run       func(c *Context, s *Script) []finding.Finding
}

// Checks is the table of lint checks, in report order.
var Checks = []Check{
	{"parse", finding.Error, false, "bash can't parse the script (bash -n)", checkParse},
	{"round", finding.Error, false, "no `# Round:` line", checkRound},
	{"workspace-live", finding.Warn, false, "`# Workspace: live`: the lane runs in the working tree and reads files no approval covers", checkWorkspaceLive},
	{"workspace-file", finding.Warn, false, "a staged lane names a repo file its # Includes: doesn't list (cat deploy.yaml): the staged copy doesn't have it", checkWorkspaceFile},
	{"workspace-empty", finding.Warn, false, "a staged lane runs a tool over its whole directory (go vet ./..., gofmt -l .) with no # Includes:, so the tool reads an empty copy", checkWorkspaceEmpty},
	{"task", finding.Error, false, "a `# Task:` name that isn't one, is a swim command, or another lane's too", checkTask},
	{"placeholder", finding.Error, false, "a template placeholder left in", checkPlaceholder},
	{"after", finding.Error, false, "`# After:` names an unknown lane or job, or the lane itself", checkAfter},
	{"cycle", finding.Error, false, "lanes wait on each other in a cycle", checkCycle},
	{"set-e", finding.Error, true, "`set -e` / `set -o errexit`", checkSetE},
	{"lane-init", finding.Error, true, "no `lane_init`, or `lane_init` with the wrong lane number", checkLaneInit},
	{"summary", finding.Warn, false, "`summary` missing or not last", checkSummary},
	{"stage", finding.Error, false, "an unknown stage name", checkStage},
	{"stage-order", finding.Warn, false, "stages out of order or repeated", checkStageOrder},
	{"bash4", finding.Warn, false, "a bash 4 feature macOS bash 3.2 can't run", checkBash4},
	{"guard-unlisted", finding.Error, false, "a guard flag used but not declared in `# Guards:` (refused when it asks; a warning with `# Guards: *`)", checkGuardUnlisted},
	{"guard-dynamic", finding.Error, false, "a guard whose flag is named at run time, without `# Guards: *`", checkGuardDynamic},
	{"guard-unused", finding.Warn, false, "a flag listed in `# Guards:` but never used", checkGuardUnused},
	{"decision-unlisted", finding.Error, false, "a decision used but not declared in `# Decisions:` (refused when it asks)", checkDecisionUnlisted},
	{"decision-subshell", finding.Error, false, "`decide` in $(...), backticks or a pipe, where an unanswered decision can't end the round", checkDecisionSubshell},
	{"decision-choices", finding.Error, false, "a `decide` with fewer than 2 or more than 9 choices", checkDecisionChoices},
	{"decision-unused", finding.Warn, false, "a decision listed in `# Decisions:` but never asked", checkDecisionUnused},
	{"checkpoint", finding.Error, false, "a `--checkpoint` step that can't be one: a snapshot, two with one key; warns before `stage change` and on a label that changes", checkCheckpoint},
	{"destructive", finding.Warn, false, "a destructive-looking command outside a guard block", checkDestructive},
	{"blocked", finding.Error, false, "a blocked command (blocked_commands)", checkBlocked},
	{"secret-echo", finding.Warn, false, "a secret variable echoed directly", checkSecretEcho},
	{"header", finding.Warn, false, "a header value swim can't use (Timeout, Step-Timeout, Locks, Workspace, Task)", checkHeader},
	{"cross-lane", finding.Info, false, "reads a path another lane writes without waiting for it", checkCrossLane},
	{"env", finding.Error, false, "the lane's env files don't parse, or set one of its guard flags", checkEnv},
	{"check-unsafe", finding.Warn, false, "a destructive-looking command before `stage change` (snapshot and check stages should only read)", checkCheckUnsafe},
	{"outputs", finding.Warn, false, "an output whose name suggests secrets (output files aren't masked)", checkOutputs},
	{"input-dep", finding.Error, false, "`input N` of a lane not in After: (or config deps)", checkInputDeps},
	{"cache", finding.Error, false, "a # Cache: with an unknown preset, no lockfile, a lockfile that isn't there, or pip requirements without hashes", checkCache},
	{"cache-bypass", finding.Warn, false, "a command that takes from a declared cache without checking it against the lockfile (npm install, pip install without --require-hashes)", checkCacheBypass},
	{"includes", finding.Error, false, "a Python or TypeScript lane imports a local module its # Includes: doesn't cover (not staged: the import fails)", checkIncludes},
	{"source", finding.Error, false, "a bash lane sources a local file its # Includes: doesn't list, or a path built at run time", checkSource},
	{"observe", finding.Warn, false, "an Observe: probe that looks like it changes something (probes must only read)", checkObserve},
	{"fail", finding.Error, false, "Fail: without `# swim: syntax 3`", checkFailSyntax},
	{"removed", finding.Error, false, "what this swim no longer has: Matrix:, Always:, Finally:, Rerun-Children:, an outcome handler (on_success, on_failure, on_exit), modules (# Modules:, mod, mod_env), the secret store (swim secret, an SDK's secret.get), or evidence steps", checkRemoved},
}

// Codes of findings that come from outside the table.
const (
	codeSyntax = "syntax"
	codeIgnore = "lint-ignore"
	codeTrust  = "trust"
)

// KnownCode reports whether code names a check (for disable comments).
func KnownCode(code string) bool {
	for _, c := range Checks {
		if c.Code == code {
			return true
		}
	}
	return code == codeSyntax || code == codeTrust
}

// Run lints the given lanes (all lanes 1..Lanes with a script when lanes is
// empty). Stubs are skipped.
func Run(root string, cfg *config.Config, lanes []int) ([]finding.Finding, error) {
	return run(root, cfg, lanes, func(Check) bool { return true })
}

// launcherRefuses are the error-level checks the launcher already refuses
// as the run starts (with its own message), so the pre-flight leaves them.
var launcherRefuses = map[string]bool{"after": true, "cycle": true, "blocked": true}

// Errors runs every error-level check `swim run` refuses on, and returns
// their findings: a lane swim lint calls wrong doesn't run.
func Errors(root string, cfg *config.Config, lanes []int) ([]finding.Finding, error) {
	fs, err := run(root, cfg, lanes, func(c Check) bool {
		return c.Preflight || c.Level == finding.Error && !launcherRefuses[c.Code]
	})
	var out []finding.Finding
	for _, f := range fs {
		if f.Level == finding.Error && f.Code != codeIgnore && f.Code != codeSyntax {
			out = append(out, f)
		}
	}
	return out, err
}

func run(root string, cfg *config.Config, lanes []int, want func(Check) bool) ([]finding.Finding, error) {
	c := &Context{Root: root, Cfg: cfg, Scripts: map[int]*Script{}}
	var out []finding.Finding
	tooNew := map[int]int{} // lane -> the newer syntax it declares
	if want(Check{Code: codeTrust}) {
		out = append(out, checkTrust(root)...)
	}
	for n := 1; n <= cfg.Lanes; n++ {
		data, err := os.ReadFile(lane.Script(root, n))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if lane.IsBinary(data) {
			data = nil // a compiled lane: only its sidecar has anything to check
		}
		s := NewScript(n, string(data))
		s.Name = lane.ScriptName(root, n)
		if !strings.HasSuffix(s.Name, ".sh") && s.Info.Syntax < syntax.Current {
			s.Info.Syntax = syntax.Current // syntax 1 was bash only
		}
		// The sidecar's keys (lane.N.yml) count, and a lane with two
		// scripts or a bad sidecar is an error.
		var tooNewErr syntax.ErrTooNew
		if info, err := lane.ReadScript(root, n); err != nil && !errors.As(err, &tooNewErr) {
			out = append(out, finding.Finding{Level: finding.Error, File: s.Name, Code: "header", Message: err.Error()})
			continue
		} else if err == nil && info.Exists {
			s.Info = info
		}
		if s.Info.Syntax > syntax.Script {
			tooNew[n] = s.Info.Syntax
		}
		if s.Info.Stub {
			continue
		}
		c.Scripts[n] = s
	}
	if len(lanes) == 0 {
		for n := 1; n <= cfg.Lanes; n++ {
			if c.Scripts[n] != nil || tooNew[n] > 0 {
				lanes = append(lanes, n)
			}
		}
	}
	for _, n := range lanes {
		name := lane.ScriptName(root, n)
		if tooNew[n] > 0 {
			out = append(out, finding.Finding{Level: finding.Error, File: name, Code: codeSyntax,
				Message: fmt.Sprintf("uses swim syntax %d; this swim reads up to %d", tooNew[n], syntax.Script),
				Fix:     "update swim (swim --version)"})
			continue
		}
		s := c.Scripts[n]
		if s == nil {
			continue
		}
		var fs []finding.Finding
		if s.Info.Syntax < syntax.Current {
			fs = append(fs, finding.Finding{Level: finding.Warn, Code: codeSyntax,
				Message: fmt.Sprintf("uses swim syntax %d; the next swim command migrates it to %d", s.Info.Syntax, syntax.Current)})
		}
		for _, ch := range Checks {
			if want(ch) && (s.Lib || !bashOnly[ch.Code]) {
				fs = append(fs, ch.Run(c, s)...)
			}
		}
		for i := range fs {
			fs[i].File = s.Name
		}
		out = append(out, applyIgnores(s, fs)...)
	}
	finding.Sort(out)
	return out, nil
}

// checkTrust: once trust.yml lists the keys swim verify trusts, every key
// lookup.yml names must be one of them, or an approval by that person
// would fail verify's trusted level.
func checkTrust(root string) []finding.Finding {
	if root == "" {
		return nil
	}
	tf, err := trust.Load(root)
	if err != nil {
		return []finding.Finding{{Level: finding.Error, File: trust.FileName, Code: codeTrust, Message: err.Error()}}
	}
	lk, err := lookup.Load(root)
	if tf == nil || err != nil {
		return nil
	}
	var users []string
	for u := range lk.Users {
		users = append(users, u)
	}
	sort.Strings(users)
	var out []finding.Finding
	for _, u := range users {
		for _, k := range lk.Users[u].Keys {
			if !tf.Has(k) {
				out = append(out, finding.Finding{Level: finding.Error, File: lookup.FileName, Code: codeTrust,
					Message: fmt.Sprintf("%s's key %s isn't in trust.yml: swim verify won't trust what it signs", u, keyLabel(k)),
					Fix:     "swim keys trust '" + k + "' --sign (or remove the key from lookup.yml)"})
			}
		}
	}
	return out
}

// keyLabel names a public key briefly: its comment, else the start of it.
func keyLabel(k string) string {
	f := strings.Fields(k)
	if len(f) >= 3 {
		return strings.Join(f[2:], " ")
	}
	if len(k) > 32 {
		return k[:32] + "..."
	}
	return k
}

// NewScript parses src as lane n's script.
func NewScript(n int, src string) *Script {
	return &Script{N: n, Name: fmt.Sprintf("lane.%d.sh", n), Src: src, Info: lane.ParseScript(src), Lib: loadsLib(src), v: scan(src)}
}

// bash reports whether the script is bash (lane.N.sh, or one that loads
// swim's library), which lint reads command by command.
func (s *Script) bash() bool { return s.Lib || strings.HasSuffix(s.Name, ".sh") }

// libLoadRE: the line that loads swim's library, in any of its spellings
// (a regular expression, kept: it is matched within a line of text).
var libLoadRE = regexp.MustCompile(`(?:swim|SWIM_BIN[^"]*)"?\s+lib\b`)

// loadsLib reports whether src loads swim's bash library (or calls
// lane_init, which only it defines).
func loadsLib(src string) bool { return lane.UsesLib(src) || libLoadRE.MatchString(src) }

// Lint runs every check on one script outside a repo (for tests): deps are
// resolved against the other scripts given, with cfg's lane count.
func Lint(cfg *config.Config, scripts map[int]string, n int) []finding.Finding {
	ss := map[int]*Script{}
	for k, src := range scripts {
		ss[k] = NewScript(k, src)
	}
	return lintScripts(cfg, ss, n)
}

// lintScripts is Lint for scripts already read (a test names one
// lane.N.py).
func lintScripts(cfg *config.Config, scripts map[int]*Script, n int) []finding.Finding {
	c := &Context{Cfg: cfg, Scripts: map[int]*Script{}}
	for k, s := range scripts {
		if !s.Info.Stub {
			c.Scripts[k] = s
		}
	}
	c.deps, c.depErrs = localDeps(cfg, c.Scripts)
	c.depsDone = true
	s := c.Scripts[n]
	if s == nil {
		return nil
	}
	var fs []finding.Finding
	for _, ch := range Checks {
		if s.Lib || !bashOnly[ch.Code] {
			fs = append(fs, ch.Run(c, s)...)
		}
	}
	for i := range fs {
		fs[i].File = s.Name
	}
	fs = applyIgnores(s, fs)
	finding.Sort(fs)
	return fs
}

// localDeps resolves After: refs (lane numbers, or job ids held by the
// given scripts) and config deps without a repo or status file.
func localDeps(cfg *config.Config, scripts map[int]*Script) (map[int][]int, map[int]string) {
	out, errs := map[int][]int{}, map[int]string{}
	for n, s := range scripts {
		seen := map[int]bool{}
		for _, d := range cfg.DepsOf(n) {
			seen[d] = true
		}
		for _, ref := range s.Info.After {
			m, err := strconv.Atoi(ref)
			if err != nil {
				m = 0
				for k, o := range scripts {
					if o.Info.Task == ref && lane.ValidTaskName(ref) || o.Info.Pending() && lane.MatchJob(o.Info.Job, ref) {
						m = k
					}
				}
			}
			switch {
			case err != nil && m == 0:
				errs[n] = fmt.Sprintf("`# After: %s`: no lane holds job %s", ref, ref)
				if lane.ValidTaskName(ref) {
					errs[n] += " or is task " + ref
				}
			case !cfg.ValidLane(m):
				errs[n] = fmt.Sprintf("`# After: %s`: no swim %d (lanes are 1..%d)", ref, m, lane.MaxLanes)
			case scripts[m] == nil && !cfg.IsActive(m):
				errs[n] = fmt.Sprintf("`# After: %s`: no swim %d: it has no lane script, log or past run", ref, m)
			case m == n:
				errs[n] = fmt.Sprintf("`# After: %s`: a lane can't wait for itself", ref)
			default:
				seen[m] = true
			}
		}
		for d := range seen {
			out[n] = append(out[n], d)
		}
		sort.Ints(out[n])
	}
	return out, errs
}

// resolve fills c.deps from launcher.ResolveDeps, one lane at a time so one
// bad reference doesn't hide the others.
func (c *Context) resolve() {
	if c.depsDone {
		return
	}
	c.depsDone = true
	c.deps, c.depErrs = map[int][]int{}, map[int]string{}
	for n, s := range c.Scripts {
		if !s.Info.Pending() {
			continue
		}
		deps, err := launcher.ResolveDeps(c.Root, c.Cfg, []int{n})
		if err != nil {
			if len(s.Info.After) > 0 {
				msg := err.Error()
				if i := strings.Index(msg, "`: "); i >= 0 && strings.HasPrefix(msg, s.Name) {
					msg = msg[len(s.Name)+1:]
				}
				c.depErrs[n] = msg
			}
			// Keep what resolves for the graph checks.
			local, _ := localDeps(c.Cfg, c.Scripts)
			c.deps[n] = local[n]
			continue
		}
		for _, d := range deps[n] {
			c.deps[n] = append(c.deps[n], d.Lane)
		}
	}
}

// reaches reports whether lane a waits on lane b, directly or through others.
func (c *Context) reaches(a, b int) bool {
	c.resolve()
	seen := map[int]bool{}
	var walk func(n int) bool
	walk = func(n int) bool {
		if seen[n] {
			return false
		}
		seen[n] = true
		for _, d := range c.deps[n] {
			if d == b || walk(d) {
				return true
			}
		}
		return false
	}
	return walk(a)
}

// ignore is a parsed disable comment.
type ignore struct {
	line   int // 0-based line it applies to; -1 for the whole file
	codes  []string
	reason string
}

// ignoreRE: a swim:lint-ignore comment (a regular expression, kept: it is
// matched within a comment's text).
var ignoreRE = regexp.MustCompile(`#\s*swim:lint-ignore(-file)?(?:\s+(.*))?$`)

// ignores parses a script's disable comments, returning them and findings
// for malformed ones.
func ignores(s *Script) ([]ignore, []finding.Finding) {
	var out []ignore
	var bad []finding.Finding
	v := s.v
	for i, raw := range v.raw {
		if v.doc[i] || !strings.Contains(raw, "swim:lint-ignore") || strings.Contains(v.code[i], "swim:lint-ignore") {
			continue
		}
		m := ignoreRE.FindStringSubmatch(raw)
		if m == nil {
			continue
		}
		fields := strings.Fields(m[2])
		if len(fields) < 2 {
			bad = append(bad, finding.Finding{Level: finding.Error, Line: i + 1, Code: codeIgnore,
				Message: "disable comment needs a check code and a reason; it disables nothing",
				Fix:     "# swim:lint-ignore <code> <why this is safe here>"})
			continue
		}
		ig := ignore{reason: strings.Join(fields[1:], " "), line: -1}
		for _, code := range strings.Split(fields[0], ",") {
			if code == "" {
				continue
			}
			if !KnownCode(code) {
				bad = append(bad, finding.Finding{Level: finding.Warn, Line: i + 1, Code: codeIgnore,
					Message: fmt.Sprintf("disable comment names unknown check %q", code),
					Fix:     "use a code shown in brackets by swim lint (swim lint --help lists them)"})
				continue
			}
			ig.codes = append(ig.codes, code)
		}
		if m[1] == "" {
			if strings.TrimSpace(v.code[i]) != "" {
				ig.line = i // trailing comment: this line
			} else {
				ig.line = -2 // next line, resolved below
				for k := i + 1; k < len(v.raw); k++ {
					t := strings.TrimSpace(v.raw[k])
					if t == "" || ignoreRE.MatchString(t) && strings.HasPrefix(t, "#") {
						continue
					}
					ig.line = k
					break
				}
			}
		}
		out = append(out, ig)
	}
	return out, bad
}

// applyIgnores drops findings a disable comment covers, and adds findings
// for malformed disable comments.
func applyIgnores(s *Script, fs []finding.Finding) []finding.Finding {
	igs, bad := ignores(s)
	var out []finding.Finding
	for _, f := range fs {
		if !ignored(igs, f) {
			out = append(out, f)
		}
	}
	for i := range bad {
		bad[i].File = s.Name
	}
	return append(out, bad...)
}

func ignored(igs []ignore, f finding.Finding) bool {
	for _, ig := range igs {
		if ig.line != -1 && ig.line != f.Line-1 {
			continue
		}
		for _, c := range ig.codes {
			if c == f.Code {
				return true
			}
		}
	}
	return false
}
