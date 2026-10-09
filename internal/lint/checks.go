package lint

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/gates-brightly/swimlane/internal/finding"
	"github.com/gates-brightly/swimlane/internal/policy"
	"github.com/gates-brightly/swimlane/internal/redact"
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
	return []finding.Finding{f(finding.Error, "round", -1, "add `# Round: <goal>` to the header",
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
	return []finding.Finding{f(finding.Error, "after", s.headerLine("After"),
		"name a configured lane number or a job id a lane holds", "%s; swim run refuses this lane", msg)}
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
	const fix = "remove it: failed checks keep going; use gate to stop the round"
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
