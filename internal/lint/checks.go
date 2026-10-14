package lint

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/envfile"
	"github.com/gates-brightly/swimlane/internal/finding"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/policy"
	"github.com/gates-brightly/swimlane/internal/redact"
	"github.com/gates-brightly/swimlane/internal/secrets"
	"github.com/gates-brightly/swimlane/internal/syntax"
)

// f builds a finding at 0-based line i (-1 for the whole file).
func f(level, code string, i int, fix, format string, a ...any) finding.Finding {
	return finding.Finding{Level: level, Code: code, Line: i + 1, Message: fmt.Sprintf(format, a...), Fix: fix}
}

// headerLine returns the 0-based line of the `# Key:` header, or -1.
func (s *Script) headerLine(key string) int {
	re := regexp.MustCompile(`^#?[ \t]?` + regexp.QuoteMeta(key) + `:`)
	for i, raw := range s.v.raw {
		if i >= 200 {
			break
		}
		if re.MatchString(strings.TrimSpace(raw)) {
			return i
		}
	}
	return -1
}

// cmdRE matches a word at command position-ish: the start of the line or
// after a separator or keyword boundary.
func cmdRE(words string) *regexp.Regexp {
	return regexp.MustCompile(`(?:^|[\s;&|(!{])(` + words + `)(?:$|[\s;&|)])`)
}

func checkRound(c *Context, s *Script) []finding.Finding {
	if s.Info.Round != "" {
		return nil
	}
	return []finding.Finding{f(finding.Error, "round", -1, fmt.Sprintf("add `# Round: <goal>` to the header (or round: to lane.%d.yml)", s.N),
		"no `# Round:` line; swim treats the lane as having nothing pending and won't run it")}
}

var placeholderRE = regexp.MustCompile(`\{\{[^}]*\}\}|<round goal>|replace with a read-only describe/list command`)

func checkPlaceholder(c *Context, s *Script) []finding.Finding {
	var out []finding.Finding
	for i, raw := range s.v.raw {
		if m := placeholderRE.FindString(raw); m != "" {
			out = append(out, f(finding.Error, "placeholder", i, "replace it with the round's real content",
				"template placeholder left in: %s", m))
		}
	}
	return out
}

func checkAfter(c *Context, s *Script) []finding.Finding {
	c.resolve()
	msg, ok := c.depErrs[s.N]
	if !ok {
		return nil
	}
	fix := "name a configured lane number or a job id a lane holds"
	if s.Info.Always {
		fix = "an Always lane waits only on other Always lanes: drop the dependency, or mark that lane `# Always: yes` too"
	}
	return []finding.Finding{f(finding.Error, "after", s.headerLine("After"), fix, "%s; swim run refuses this lane", msg)}
}

// checkRerunGuard warns about a guarded lane that a Rerun-Children lane
// reruns: swim all runs it again after it passed, and asks for its
// approvals again (or, unattended, dry-runs them).
func checkRerunGuard(c *Context, s *Script) []finding.Finding {
	if len(s.Info.Guards) == 0 && s.Info.DynamicGuards == 0 {
		return nil
	}
	var by []int
	for n, o := range c.Scripts {
		if n != s.N && o.Info.Always && o.Info.RerunChildren && c.reaches(s.N, n) {
			by = append(by, n)
		}
	}
	if len(by) == 0 {
		return nil
	}
	sort.Ints(by)
	parts := make([]string, len(by))
	for i, n := range by {
		parts[i] = fmt.Sprintf("swim %d", n)
	}
	return []finding.Finding{f(finding.Warn, "rerun-guard", s.headerLine("After"),
		"keep guarded rounds out from under Rerun-Children lanes, or archive and stub this lane once it has done its work",
		"this guarded round sits downstream of %s (Rerun-Children: yes): swim all reruns it every time, even after it passed", strings.Join(parts, ", "))}
}

// checkCycle reports lanes on a dependency cycle (strongly connected
// components of more than one lane, over After: and config deps).
func checkCycle(c *Context, s *Script) []finding.Finding {
	c.resolve()
	// Lanes reachable from s that also reach s are on a cycle with it.
	var members []int
	for n := range c.deps {
		if n != s.N && c.reaches(s.N, n) && c.reaches(n, s.N) {
			members = append(members, n)
		}
	}
	if len(members) == 0 {
		return nil
	}
	members = append(members, s.N)
	sort.Ints(members)
	parts := make([]string, len(members))
	for i, n := range members {
		parts[i] = fmt.Sprintf("swim %d", n)
	}
	return []finding.Finding{f(finding.Error, "cycle", s.headerLine("After"),
		"remove one of the `# After:` lines (or config deps) in the cycle",
		"dependency cycle: %s wait on each other; none of them can start", strings.Join(parts, ", "))}
}

var (
	setERE     = regexp.MustCompile(`(?:^|[\s;&|(])set\s+(?:-[a-zA-Z]*e[a-zA-Z]*|(?:-[a-zA-Z]+\s+)*-o\s+errexit)(?:$|[\s;&|)])`)
	shebangERE = regexp.MustCompile(`^#!.*\s-[a-zA-Z]*e[a-zA-Z]*(?:\s|$)`)
)

func checkSetE(c *Context, s *Script) []finding.Finding {
	const fix = "remove it: failed checks keep going; use gate to stop the round, or `# Fail: hard` (swim: syntax 3) to stop at the first failed step"
	var out []finding.Finding
	if len(s.v.raw) > 0 && shebangERE.MatchString(s.v.raw[0]) {
		out = append(out, f(finding.Error, "set-e", 0, fix, "the shebang turns on errexit (-e); a failed check would end the round without a summary"))
	}
	for i, line := range s.v.bare {
		if !s.v.doc[i] && setERE.MatchString(line) {
			out = append(out, f(finding.Error, "set-e", i, fix, "uses `set -e`; failed checks must keep going (use gate for stops)"))
		}
	}
	return out
}

var (
	laneInitRE    = cmdRE(`lane_init`)
	laneInitArgRE = regexp.MustCompile(`lane_init(?:\s+["']?([^\s"';&|)]*))?`)
)

func checkLaneInit(c *Context, s *Script) []finding.Finding {
	for i, line := range s.v.bare {
		if s.v.doc[i] || !laneInitRE.MatchString(line) {
			continue
		}
		m := laneInitArgRE.FindStringSubmatch(s.v.code[i])
		arg := ""
		if m != nil {
			arg = m[1]
		}
		if n, err := strconv.Atoi(arg); err == nil && n != s.N {
			return []finding.Finding{f(finding.Error, "lane-init", i, fmt.Sprintf("lane_init %d", s.N),
				"`lane_init %d` in %s; the round would claim swim %d's log and status", n, s.Name, n)}
		}
		if arg == "" {
			return []finding.Finding{f(finding.Error, "lane-init", i, fmt.Sprintf("lane_init %d", s.N),
				"`lane_init` without a lane number; the round stops at once")}
		}
		return nil
	}
	return []finding.Finding{f(finding.Error, "lane-init", -1, fmt.Sprintf("add `lane_init %d` after the line that loads the library", s.N),
		"no `lane_init %d`; the round wouldn't be logged or recorded in status", s.N)}
}

var (
	summaryRE     = cmdRE(`summary`)
	summaryLastRE = regexp.MustCompile(`^\s*summary\s*(?:$|[;&|])`)
	exitRE        = regexp.MustCompile(`^\s*exit\b`)
)

func checkSummary(c *Context, s *Script) []finding.Finding {
	last := s.v.lastCode()
	found := -1
	for i, line := range s.v.bare {
		if !s.v.doc[i] && summaryRE.MatchString(line) {
			found = i
		}
	}
	if found < 0 {
		return []finding.Finding{f(finding.Warn, "summary", -1, "end the script with `summary`",
			"no `summary` at the end; the round's final state is recorded only by the exit trap")}
	}
	if last >= 0 && !summaryLastRE.MatchString(s.v.bare[last]) {
		// `summary` then `exit $?` is fine.
		if exitRE.MatchString(s.v.bare[last]) && found < last {
			prev := last - 1
			for prev >= 0 && !s.v.isCode(prev) {
				prev--
			}
			if prev == found {
				return nil
			}
		}
		return []finding.Finding{f(finding.Warn, "summary", last, "move `summary` to the end",
			"`summary` is not last (line %d); steps after it aren't in the round's summary", found+1)}
	}
	return nil
}

var (
	stageRE     = regexp.MustCompile(`^\s*stage(?:\s+["']?([^\s"';&|)]*))?`)
	stageWordRE = regexp.MustCompile(`^\s*stage(\s|$)`)
)

// stages lists the script's literal `stage NAME` calls (0-based line, name).
func (s *Script) stages() (lines []int, names []string) {
	for i, line := range s.v.code {
		if s.v.doc[i] {
			continue
		}
		m := stageRE.FindStringSubmatch(line)
		if m == nil || strings.HasPrefix(m[1], "$") || !stageWordRE.MatchString(line) {
			continue
		}
		lines, names = append(lines, i), append(names, m[1])
	}
	return
}

func checkStage(c *Context, s *Script) []finding.Finding {
	var out []finding.Finding
	lines, names := s.stages()
	for k, name := range names {
		if syntax.StageIndex(name) < 0 {
			out = append(out, f(finding.Error, "stage", lines[k], "use one of: "+strings.Join(syntax.Stages, ", "),
				"unknown stage %q; the round stops here (stages are %s)", name, strings.Join(syntax.Stages, ", ")))
		}
	}
	return out
}

func checkStageOrder(c *Context, s *Script) []finding.Finding {
	var out []finding.Finding
	lines, names := s.stages()
	last, seen := -1, map[string]bool{}
	for k, name := range names {
		idx := syntax.StageIndex(name)
		if idx < 0 {
			continue
		}
		switch {
		case seen[name]:
			out = append(out, f(finding.Warn, "stage-order", lines[k], "keep one stage line per stage",
				"stage %s appears twice", name))
		case idx < last:
			out = append(out, f(finding.Warn, "stage-order", lines[k], "order: "+strings.Join(syntax.Stages, ", "),
				"stage %s after %s (expected order: %s)", name, syntax.Stages[last], strings.Join(syntax.Stages, ", ")))
		case name == syntax.Change && !seen[syntax.Snapshot] && !seen[syntax.CheckSt]:
			out = append(out, f(finding.Warn, "stage-order", lines[k], "snapshot and check before changing anything",
				"change stage without a snapshot or check stage before it"))
		}
		seen[name] = true
		if idx > last {
			last = idx
		}
	}
	return out
}

var bash4 = []struct {
	re   *regexp.Regexp
	what string
}{
	{regexp.MustCompile(`(?:^|[\s;&|(])(?:declare|local|typeset)\s+-[a-zA-Z]*A`), "`declare -A` (associative arrays)"},
	{cmdRE(`mapfile|readarray`), "`mapfile`/`readarray`"},
	{regexp.MustCompile(`\$\{[^}]*(?:,,|\^\^)\}|\$\{[A-Za-z_][A-Za-z0-9_]*[,^]\}`), "`${x,,}`/`${x^^}` case conversion"},
	{regexp.MustCompile(`\|&`), "`|&`"},
	{regexp.MustCompile(`&>>`), "`&>>`"},
	{cmdRE(`coproc`), "`coproc`"},
	{regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*\[-[0-9]+\]\}`), "negative array indexes"},
	{regexp.MustCompile(`(?:^|[\s;&|(])(?:declare|local)\s+-[a-zA-Z]*n\s`), "`declare -n` (namerefs)"},
}

func checkBash4(c *Context, s *Script) []finding.Finding {
	var out []finding.Finding
	for i, line := range s.v.bare {
		if s.v.doc[i] {
			continue
		}
		for _, b := range bash4 {
			if b.re.MatchString(line) {
				out = append(out, f(finding.Warn, "bash4", i, "rewrite it for bash 3.2 (see `swim --help`, WRITING A LANE SCRIPT)",
					"%s is bash 4; macOS bash 3.2 can't run it", b.what))
				break
			}
		}
	}
	return out
}

var (
	guardHeaderRE = regexp.MustCompile(`^#[ \t]?Guards:\s*(\S+)`)
	guardUseRE    = regexp.MustCompile(`(?:^|[\s;&|(!])guard\s+([A-Za-z_][A-Za-z0-9_]*)`)
)

// guardFlags returns the flags listed in Guards: headers and those used in
// code, each with the 0-based line it first appears on.
func (s *Script) guardFlags() (listed, used map[string]int, order []string) {
	listed, used = map[string]int{}, map[string]int{}
	for i, raw := range s.v.raw {
		if i < 200 {
			if m := guardHeaderRE.FindStringSubmatch(strings.TrimSpace(raw)); m != nil {
				flag := m[1]
				if flag != "-" && !strings.EqualFold(flag, "none") {
					if _, ok := listed[flag]; !ok {
						listed[flag] = i
					}
				}
			}
		}
		if s.v.doc[i] {
			continue
		}
		for _, m := range guardUseRE.FindAllStringSubmatch(s.v.bare[i], -1) {
			if _, ok := used[m[1]]; !ok {
				used[m[1]] = i
				order = append(order, m[1])
			}
		}
	}
	return
}

func checkGuardUnlisted(c *Context, s *Script) []finding.Finding {
	listed, used, order := s.guardFlags()
	var out []finding.Finding
	for _, flag := range order {
		if _, ok := listed[flag]; !ok {
			out = append(out, f(finding.Warn, "guard-unlisted", used[flag], fmt.Sprintf("add `# Guards: %s  <what it approves, date, reason>` to the header", flag),
				"guard flag %s is used but not listed in `# Guards:`; the operator reads the header to know what it approves", flag))
		}
	}
	return out
}

func checkGuardUnused(c *Context, s *Script) []finding.Finding {
	listed, used, _ := s.guardFlags()
	var flags []string
	for flag := range listed {
		if _, ok := used[flag]; !ok {
			flags = append(flags, flag)
		}
	}
	sort.Slice(flags, func(a, b int) bool { return listed[flags[a]] < listed[flags[b]] })
	var out []finding.Finding
	for _, flag := range flags {
		out = append(out, f(finding.Warn, "guard-unused", listed[flag], "remove the line, or guard the action with it",
			"`# Guards:` lists %s, but no `guard %s` uses it", flag, flag))
	}
	return out
}

var (
	destructiveRE = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_])(delete|destroy|drop)(?:$|[^A-Za-z0-9_])|(?:^|[\s;&|('"])rm\s+(?:-[a-zA-Z]*[rR][a-zA-Z]*|--recursive)\b`)
	// Lines that only say or record something.
	talkRE  = regexp.MustCompile(`^\s*(?:echo|printf|stop|drift|confirm|guard|:|swim\s+note|if\s+!?\s*guard)\b`)
	labelRE = regexp.MustCompile(`^(\s*(?:run|gate|snapshot)\s+(?:--\S+(?:\s+[^-"'\s]\S*)?\s+)*)("(?:[^"\\]|\\.)*"|'[^']*')`)
	blockRE = regexp.MustCompile(`[A-Za-z0-9_]+|&&|!`)
)

// guardedLines marks lines inside `if guard FLAG ...; then ... fi` (the
// then branch only) or after `guard FLAG "..." &&` on the same line.
func (s *Script) guardedLines() []bool {
	type block struct{ guarded, cond bool }
	var stack []block
	out := make([]bool, len(s.v.bare))
	for i, line := range s.v.bare {
		if s.v.doc[i] {
			continue
		}
		inside := false
		for _, b := range stack {
			if b.guarded && !b.cond {
				inside = true
			}
		}
		neg, sawGuard := false, false
		for _, tok := range blockRE.FindAllString(line, -1) {
			switch tok {
			case "if":
				stack = append(stack, block{cond: true})
			case "elif":
				if len(stack) > 0 {
					stack[len(stack)-1] = block{cond: true}
				}
			case "else":
				if len(stack) > 0 {
					stack[len(stack)-1] = block{}
				}
			case "then":
				if len(stack) > 0 {
					stack[len(stack)-1].cond = false
					if stack[len(stack)-1].guarded {
						inside = true
					}
				}
			case "fi":
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
			case "guard":
				if len(stack) > 0 && stack[len(stack)-1].cond && !neg {
					stack[len(stack)-1].guarded = true
				} else if !neg {
					sawGuard = true
				}
			case "&&":
				if sawGuard {
					inside = true
				}
			}
			neg = tok == "!"
		}
		out[i] = inside
	}
	return out
}

func checkDestructive(c *Context, s *Script) []finding.Finding {
	guarded := s.guardedLines()
	var out []finding.Finding
	for i, line := range s.v.code {
		if !s.v.isCode(i) || guarded[i] || talkRE.MatchString(line) || strings.HasPrefix(strings.TrimSpace(line), "#!") {
			continue
		}
		cmd := line
		if m := labelRE.FindStringSubmatch(line); m != nil {
			// run "<label>" cmd...: judge the command, not the label.
			cmd = line[len(m[0]):]
			if talkRE.MatchString(cmd) {
				continue
			}
		}
		if m := destructiveRE.FindStringSubmatch(cmd); m != nil {
			what := m[1]
			if what == "" {
				what = "rm -r"
			}
			out = append(out, f(finding.Warn, "destructive", i, "wrap it: if guard FLAG \"<action - date, reason>\"; then ... fi",
				"%q looks destructive but isn't inside a guard block", strings.ToLower(what)))
		}
	}
	return out
}

func checkBlocked(c *Context, s *Script) []finding.Finding {
	var out []finding.Finding
	for _, h := range policy.ScanScript(s.Src, c.Cfg.Blocked()) {
		out = append(out, f(finding.Error, "blocked", h.Line-1, "remove it: an operator pushes, commits or pulls, never a lane",
			"runs %q, a blocked command; swim run refuses this lane", h.Pattern))
	}
	return out
}

var (
	echoRE   = cmdRE(`echo|printf`)
	varRefRE = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)`)
)

// secret reports whether a variable name is a secret under the config.
func (c *Context) secret(name string) bool {
	for _, n := range c.Cfg.SecretEnvIgnore {
		if n == name {
			return false
		}
	}
	for _, n := range c.Cfg.SecretEnv {
		if n == name {
			return true
		}
	}
	return (c.Cfg.SecretEnvAuto == nil || *c.Cfg.SecretEnvAuto) && redact.AutoName(name)
}

func checkSecretEcho(c *Context, s *Script) []finding.Finding {
	var out []finding.Finding
	for i, line := range s.v.bare {
		if s.v.doc[i] || !echoRE.MatchString(line) {
			continue
		}
		for _, m := range varRefRE.FindAllStringSubmatch(s.v.code[i], -1) {
			if c.secret(m[1]) {
				out = append(out, f(finding.Warn, "secret-echo", i, "print something derived (its length, whether it's set) or drop the line",
					"echoes $%s, a secret; swim masks it in what it writes, but lanes must never print secrets", m[1]))
				break
			}
		}
	}
	return out
}

// secretUseRE is a secret named literally: swim secret get NAME, an SDK's
// secret.get("NAME") / Secret.Get("NAME"), or ["swim", "secret", "get", "NAME"].
var secretUseRE = regexp.MustCompile(`\bsecret\s+get\s+["']?([A-Za-z][A-Za-z0-9_]*)|\b(?:secret\.get|Secret\.Get)\(\s*["'\x60]([A-Za-z][A-Za-z0-9_]*)|['"]secret['"]\s*,\s*['"]get['"]\s*,\s*['"]([A-Za-z][A-Za-z0-9_]*)`)

// checkSecretRefs: a secret a lane names must be in the store (or come
// from SWIM_SECRET_<NAME>); names are readable, so no key is needed.
func checkSecretRefs(c *Context, s *Script) []finding.Finding {
	var out []finding.Finding
	store, err := secrets.Load(secrets.Path(config.Path()))
	if err != nil {
		return []finding.Finding{f(finding.Warn, "secret-ref", -1, "fix or remove the secrets store", "can't read the secrets store: %v", err)}
	}
	repo := secrets.RepoKey(c.Root)
	seen := map[string]bool{}
	for i, line := range s.v.raw {
		for _, m := range secretUseRE.FindAllStringSubmatch(line, -1) {
			name := m[1] + m[2] + m[3]
			if seen[name] || store.Has(repo, name) || os.Getenv(secrets.EnvPrefix+secrets.EnvName(name)) != "" {
				continue
			}
			seen[name] = true
			out = append(out, f(finding.Warn, "secret-ref", i, "swim secret set "+name+" (or --all)",
				"secret %s isn't stored for this repo; getting it will fail", name))
		}
	}
	return out
}

func checkHeader(c *Context, s *Script) []finding.Finding {
	var out []finding.Finding
	for _, p := range s.Info.Problems {
		key := strings.TrimSuffix(strings.Fields(p)[0], ":")
		out = append(out, f(finding.Warn, "header", s.headerLine(key), "fix the value, or leave it empty for the default", "%s", p))
	}
	return out
}

var (
	redirRE  = regexp.MustCompile(`(?:^|[^0-9&<>])&?>>?\s*("[^"]+"|'[^']+'|[^\s;&|)<>'"]+)`)
	outRE    = regexp.MustCompile(`(?:^|\s)(?:-o|--output)(?:=|\s+)("[^"]+"|'[^']+'|[^\s;&|)'"]+)`)
	cpRE     = regexp.MustCompile(`(?:^|[\s;&|('"])(?:cp|mv|tee|install)\s+([^;&|)]*)`)
	pathChar = regexp.MustCompile(`[A-Za-z0-9_./$-]`)
)

// usablePath cleans a written path and reports whether it's specific enough
// to look for in other lanes.
func usablePath(p string) (string, bool) {
	p = strings.Trim(p, `"'`)
	if len(p) < 4 || strings.HasPrefix(p, "-") || strings.HasPrefix(p, "/dev/") || strings.Contains(p, "$(") ||
		strings.ContainsAny(p, "*?`") || strings.Contains(p, "STEP_LOG") || !strings.ContainsAny(p, "/.") {
		return "", false
	}
	return p, true
}

// writes lists the paths a script writes, with the 0-based line of the first write.
func (s *Script) writes() map[string]int {
	out := map[string]int{}
	add := func(p string, i int) {
		if p, ok := usablePath(p); ok {
			if _, seen := out[p]; !seen {
				out[p] = i
			}
		}
	}
	for i, line := range s.v.code {
		if !s.v.isCode(i) {
			continue
		}
		for _, m := range redirRE.FindAllStringSubmatch(line, -1) {
			add(m[1], i)
		}
		for _, m := range outRE.FindAllStringSubmatch(line, -1) {
			add(m[1], i)
		}
		for _, m := range cpRE.FindAllStringSubmatch(line, -1) {
			args := strings.Fields(m[1])
			if strings.Contains(m[0], "tee") {
				for _, a := range args {
					add(a, i)
				}
			} else if len(args) >= 2 {
				add(args[len(args)-1], i)
			}
		}
	}
	return out
}

// mentions returns the first 0-based code line of s that contains path as a
// whole path, or -1.
func (s *Script) mentions(path string) int {
	for i, line := range s.v.code {
		if !s.v.isCode(i) {
			continue
		}
		for from := 0; ; {
			k := strings.Index(line[from:], path)
			if k < 0 {
				break
			}
			k += from
			end := k + len(path)
			before := k == 0 || !pathChar.MatchString(line[k-1:k])
			after := end >= len(line) || !pathChar.MatchString(line[end:end+1])
			if before && after {
				return i
			}
			from = k + 1
		}
	}
	return -1
}

// checkCrossLane is a heuristic: a path written in lane A (`> path`, `-o
// path`, `cp ... path`, `tee path`) that appears in lane B, when neither lane
// waits on the other, may be read before it's written. It reports what it
// saw so a person can judge.
func checkCrossLane(c *Context, s *Script) []finding.Finding {
	mine := s.writes()
	var others []int
	for n := range c.Scripts {
		if n != s.N {
			others = append(others, n)
		}
	}
	sort.Ints(others)
	var out []finding.Finding
	reported := map[string]bool{}
	for _, a := range others {
		o := c.Scripts[a]
		if !o.Info.Pending() {
			continue
		}
		ws := o.writes()
		paths := make([]string, 0, len(ws))
		for p := range ws {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			if _, own := mine[p]; own || reported[p] {
				continue
			}
			i := s.mentions(p)
			if i < 0 || c.reaches(s.N, a) || c.reaches(a, s.N) {
				continue
			}
			reported[p] = true
			out = append(out, f(finding.Info, "cross-lane", i, fmt.Sprintf("if it needs swim %d's output, add `# After: %d`", a, a),
				"uses %s, which %s:%d writes, but doesn't wait for swim %d", p, o.Name, ws[p]+1, a))
		}
	}
	return out
}

// checkEnv resolves the lane's env files: a line that doesn't parse, or a
// key that is one of the lane's guard flags (or matches a lookup.yml guard
// glob), is an error. swim run refuses both before any lane starts.
func checkEnv(c *Context, s *Script) []finding.Finding {
	if c.Root == "" {
		return nil
	}
	r, err := envfile.Resolve(c.Root, c.Cfg, s.N)
	if err != nil {
		return []finding.Finding{f(finding.Error, "env", -1, "fix the line: KEY=VALUE, '...' for literal text, no export or $", "%v", err)}
	}
	lk, _ := lookup.Load(c.Root)
	var out []finding.Finding
	if s.N == firstLane(c) {
		// Once per repo, on its first lane script: a key set twice in a file.
		for _, d := range envfile.Duplicates(c.Root) {
			out = append(out, f(finding.Warn, "env", -1, "remove one of them", "%s", d))
		}
	}
	for _, g := range envfile.GuardConflicts(r, s.Info, lk) {
		out = append(out, f(finding.Error, "env", -1, "remove it from the env file; approve with the shell, the prompt or swim approve", "%s", g.Error()))
	}
	return out
}

var (
	// nearHandlerRE are function names that look like an outcome handler
	// but aren't one: swim would never call them.
	nearHandlerRE = regexp.MustCompile(`^\s*(?:function\s+)?(on_fail|on_failed|onfailure|onfail|on_error|onerror|on_succes|on_succeed|onsuccess|onexit|on_finally|finally)\s*\(\s*\)`)
	// helperHandlerRE are generic names that are a typo only when the round
	// defines no handler at all (otherwise they're helpers a handler calls).
	helperHandlerRE = regexp.MustCompile(`^\s*(?:function\s+)?(always|cleanup|rollback)\s*\(\s*\)`)
	reachedRE       = regexp.MustCompile(`(?:^|[\s;&|(!])reached\s`)
)

// checkHandlers: a round with outcome handlers or Fail: must declare script
// syntax 3, or an older swim would run it without them; and a near-miss
// handler name means the rollback silently never runs.
func checkHandlers(c *Context, s *Script) []finding.Finding {
	var out []finding.Finding
	if (len(s.Info.Handlers) > 0 || s.Info.FailText != "") && s.Info.Syntax < syntax.Script {
		what := "Fail:"
		if len(s.Info.Handlers) > 0 {
			what = "outcome handlers (" + s.Info.Handlers[0].Name + ")"
		}
		out = append(out, f(finding.Error, "handler", -1, "change the syntax line to `# swim: syntax 3`",
			"uses %s but declares syntax %d: an older swim would run the round without them", what, s.Info.Syntax))
	}
	if !s.Lib {
		return out
	}
	for i, raw := range s.v.raw {
		if m := nearHandlerRE.FindStringSubmatch(raw); m != nil {
			out = append(out, f(finding.Error, "handler", i, "rename it on_success, on_failure or on_exit",
				"function %s is not an outcome handler: swim calls only on_success, on_failure and on_exit", m[1]))
		} else if m := helperHandlerRE.FindStringSubmatch(raw); m != nil && len(s.Info.Handlers) == 0 {
			out = append(out, f(finding.Error, "handler", i, "call it from on_failure or on_exit (or rename it)",
				"function %s is never called when the round ends: swim calls only on_success, on_failure and on_exit", m[1]))
		}
	}
	for _, h := range s.Info.Handlers {
		if h.Cmd != "" {
			out = append(out, f(finding.Warn, "handler", h.Line-1, "define a bash function "+h.Name+"() instead",
				"a bash lane's handlers are functions; the %s: header key is for lanes in other languages", map[string]string{lane.OnSuccess: "On-Success", lane.OnFailure: "On-Failure", lane.OnExit: "On-Exit"}[h.Name]))
		}
	}
	return out
}

// checkHandlerReached: an on_failure that can approve something (a guard)
// should first check that the round got far enough to have changed it.
func checkHandlerReached(c *Context, s *Script) []finding.Finding {
	h, ok := s.Info.Handler(lane.OnFailure)
	if !ok || h.Cmd != "" || len(h.Guards) == 0 {
		return nil
	}
	start := h.Line - 1
	for i := start; i < len(s.v.raw); i++ {
		if i > start && strings.TrimRight(s.v.raw[i], " \t") == "}" {
			break
		}
		if reachedRE.MatchString(s.v.raw[i]) {
			return nil
		}
	}
	return []finding.Finding{f(finding.Warn, "handler-reached", start, "start it with: reached change || return 0",
		"on_failure uses guard %s but never checks `reached`: it would roll back even when the round failed before changing anything", h.Guards[0])}
}

var (
	secretOutputRE = regexp.MustCompile(`(?i)(^|/|[._-])(\.?env|secrets?|credentials?|passwords?|tokens?|id_rsa|id_ed25519|.*\.pem|.*\.key)($|[._/-])`)
	inputUseRE     = regexp.MustCompile(`(?:^|[\s(;&|$])input\s+([0-9]+)\s`)
)

// checkOutputs: outputs aren't masked, so a name that suggests secrets is
// worth a second look.
func checkOutputs(c *Context, s *Script) []finding.Finding {
	var out []finding.Finding
	for _, name := range s.Info.Outputs {
		if secretOutputRE.MatchString(name) {
			out = append(out, f(finding.Warn, "outputs", s.headerLine("Outputs"), "never put secret values in outputs: they aren't masked",
				"output %s looks like it could hold secrets", name))
		}
	}
	return out
}

// checkInputDeps: input N reads lane N's outputs only if N is a dependency.
func checkInputDeps(c *Context, s *Script) []finding.Finding {
	deps := map[string]bool{}
	for _, a := range s.Info.After {
		deps[a] = true
	}
	if c.Cfg != nil {
		for _, d := range c.Cfg.DepsOf(s.N) {
			deps[strconv.Itoa(d)] = true
		}
	}
	var out []finding.Finding
	for i, line := range s.v.raw {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for _, m := range inputUseRE.FindAllStringSubmatch(line+" ", -1) {
			if !deps[m[1]] {
				out = append(out, f(finding.Error, "input-dep", i, "add "+m[1]+" to the round's After:",
					"input %s reads swim %s's outputs, but swim %s isn't in After: (or config deps)", m[1], m[1], m[1]))
			}
		}
	}
	return out
}

var (
	stageChangeRE = regexp.MustCompile(`(?:^|[\s;&|(])stage\s+(?:change|verify)\b`)
	applyRE       = regexp.MustCompile(`(?i)\b(terraform\s+(?:apply|destroy|import)|kubectl\s+(?:apply|delete|scale|rollout)|helm\s+(?:install|upgrade|uninstall)|aws\s+\S+\s+(?:delete|put|update|create)-\S+|sst\s+(?:deploy|remove))\b`)
)

// checkCheckUnsafe: swim plan --check runs everything before the first
// `stage change` for real, so a change there (terraform apply, a delete)
// would happen during a preview.
func checkCheckUnsafe(c *Context, s *Script) []finding.Finding {
	if !s.Info.Staged {
		return nil // never checked: swim plan --check refuses a round with no stages
	}
	var out []finding.Finding
	for i, line := range s.v.code {
		if !s.v.isCode(i) {
			continue
		}
		if stageChangeRE.MatchString(line) {
			break
		}
		cmd := line
		if m := labelRE.FindStringSubmatch(line); m != nil {
			cmd = line[len(m[0]):]
		}
		m := applyRE.FindString(cmd)
		if m == "" {
			if d := destructiveRE.FindStringSubmatch(cmd); d != nil && !talkRE.MatchString(cmd) {
				m = d[1]
				if m == "" {
					m = "rm -r"
				}
			}
		}
		if m != "" {
			out = append(out, f(finding.Warn, "check-unsafe", i, "move it after `stage change` (snapshot and check stages must be read-only)",
				"%q runs before `stage change`, so swim plan --check would run it for real", strings.ToLower(m)))
		}
	}
	return out
}

var (
	pySDKRE    = regexp.MustCompile(`(?m)^\s*(?:import\s+swim\b|from\s+swim\s+import)`)
	pyBypassRE = regexp.MustCompile(`\b(subprocess\.(?:run|call|check_call|check_output|Popen)|os\.system|os\.popen)\s*\(`)
	tsSDKRE    = regexp.MustCompile(`["']@gates-brightly/swim["']|/sdk/ts/swim\.ts["']`)
	tsBypassRE = regexp.MustCompile(`\b(execSync|execFileSync|spawnSync|exec|execFile|spawn)\s*\(`)
)

// checkIncludes: a lane in Python or TypeScript that imports a local module
// should list it in # Includes:, or approvals of the script don't cover
// what it actually runs.
func checkIncludes(c *Context, s *Script) []finding.Finding {
	if c.Root == "" {
		return nil
	}
	ext := strings.ToLower(filepath.Ext(s.Name))
	covered := s.included
	switch ext {
	case ".py", ".ts", ".mts", ".js", ".mjs":
	default:
		return nil
	}
	var out []finding.Finding
	for i, line := range s.v.raw {
		cands := lane.ImportCandidates(ext, line)
		for _, cand := range cands {
			if st, err := os.Stat(filepath.Join(c.Root, cand)); err == nil && !st.IsDir() {
				if !covered(cand) {
					out = append(out, f(finding.Warn, "includes", i, "add `# Includes: "+cand+"` (globs work) so the script says what it runs",
						"imports local %s, which isn't in # Includes: (swim pins and stages it anyway; approvers reading the script won't see it)", cand))
				}
				break
			}
		}
	}
	return out
}

// included reports whether rel matches one of the script's # Includes:
// globs (dir/** and dir/ cover everything under dir).
func (s *Script) included(rel string) bool {
	for _, g := range s.Info.Includes {
		if ok, _ := filepath.Match(g, rel); ok {
			return true
		}
		if strings.HasSuffix(g, "/**") || strings.HasSuffix(g, "/") {
			if strings.HasPrefix(rel, strings.TrimSuffix(strings.TrimSuffix(g, "**"), "/")+"/") {
				return true
			}
		}
	}
	return false
}

// checkSource: a bash lane that sources a local file should list it in #
// Includes:. swim pins and stages a literal path anyway (the execution
// manifest), but a path built at run time can't be pinned at all: what it
// runs is outside what was approved.
func checkSource(c *Context, s *Script) []finding.Finding {
	if c.Root == "" {
		return nil
	}
	switch strings.ToLower(filepath.Ext(s.Name)) {
	case ".sh", ".bash", "":
	default:
		return nil
	}
	var out []finding.Finding
	for _, src := range lane.ScanSources(c.Root, s.Name, s.Src) {
		switch {
		case src.Dynamic:
			out = append(out, f(finding.Error, "source", src.Line-1, "source a literal repo path, listed in # Includes:",
				"sources %s, a path known only at run time: swim can't pin or stage it, so no approval covers what it runs", src.Arg))
		case !s.included(src.Path):
			out = append(out, f(finding.Error, "source", src.Line-1, "add `# Includes: "+src.Path+"` (globs work) so the script says what it runs",
				"sources local %s, which isn't in # Includes:", src.Path))
		}
	}
	return out
}

// checkObserve: Observe: probes run when a guard is approved and again just
// before it returns, so they must only read. A probe that looks like it
// changes something, or is a blocked command, is flagged.
func checkObserve(c *Context, s *Script) []finding.Finding {
	var out []finding.Finding
	line := s.headerLine("Observe")
	for _, p := range s.Info.Observe {
		if p.Cmd == "" {
			continue
		}
		i := line
		if p.Line > 0 {
			i = p.Line - 1
		}
		if hits := policy.ScanScript(p.Cmd, c.Cfg.Blocked()); len(hits) > 0 {
			out = append(out, f(finding.Error, "observe", i, "a probe only reads: remove it",
				"Observe: %s runs %q, a blocked command", p.Name, hits[0].Pattern))
			continue
		}
		if m := applyRE.FindString(p.Cmd); m != "" || destructiveRE.MatchString(p.Cmd) || observeWriteRE.MatchString(p.Cmd) {
			out = append(out, f(finding.Warn, "observe", i, "a probe runs at approval and again before the guarded step: make it read-only",
				"Observe: %s looks like it changes something (%s)", p.Name, p.Cmd))
		}
	}
	return out
}

// observeWriteRE is a probe that writes: a redirect into a file, or a
// command that sends or posts something.
var observeWriteRE = regexp.MustCompile(`(?:^|[^0-9&<>])>>?\s*[^&\s]|(?:^|[\s;&|(])(?:curl\s.*-X\s*(?:POST|PUT|DELETE|PATCH)|mail|sendmail|tee|touch|mkdir|mv|cp)(?:$|[\s;&|)])`)

// checkSDKBypass: in a lane using an SDK, commands run with subprocess or
// child_process skip swim's logging, timeouts and blocked-command checks.
func checkSDKBypass(c *Context, s *Script) []finding.Finding {
	var bypass *regexp.Regexp
	switch {
	case pySDKRE.MatchString(s.Src):
		bypass = pyBypassRE
	case tsSDKRE.MatchString(s.Src):
		bypass = tsBypassRE
	default:
		return nil
	}
	var out []finding.Finding
	for i, line := range s.v.raw {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "#") || strings.HasPrefix(t, "//") {
			continue
		}
		if m := bypass.FindStringSubmatch(line); m != nil {
			out = append(out, f(finding.Info, "sdk-bypass", i, "use r.run(...) (or r.step for in-process work) so swim logs it",
				"%s runs a command swim doesn't see: no log block, timeout or blocked-command check", m[1]))
		}
	}
	return out
}

// firstLane is the lowest lane number with a script being linted.
func firstLane(c *Context) int {
	first := 0
	for n := range c.Scripts {
		if first == 0 || n < first {
			first = n
		}
	}
	return first
}
