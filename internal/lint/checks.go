package lint

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/gates-brightly/swimlane/internal/chars"
	"github.com/gates-brightly/swimlane/internal/envfile"
	"github.com/gates-brightly/swimlane/internal/finding"
	"github.com/gates-brightly/swimlane/internal/help"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/policy"
	"github.com/gates-brightly/swimlane/internal/redact"
	"github.com/gates-brightly/swimlane/internal/syntax"
)

// f builds a finding at 0-based line i (-1 for the whole file).
func f(level, code string, i int, fix, format string, a ...any) finding.Finding {
	return finding.Finding{Level: level, Code: code, Line: i + 1, Message: fmt.Sprintf(format, a...), Fix: fix}
}

// Lint's regular expressions, kept: each matches a pattern inside text
// (rm -r, terraform apply, a bash 4 form, a redirect, an SDK call in
// another language). The commands themselves are read with lane.Commands
// (shell.go: commands, ranCommands); lint's findings are advice.

// headerLine returns the 0-based line of the `# Key:` header, or -1.
func (s *Script) headerLine(key string) int {
	isKey := func(line string) bool {
		line = strings.TrimPrefix(line, "#")
		if line != "" && (line[0] == ' ' || line[0] == '\t') {
			line = line[1:]
		}
		return strings.HasPrefix(line, key+":")
	}
	for i, raw := range s.v.raw { // raw: header lines are comments
		if i >= 200 {
			break
		}
		if isKey(strings.TrimSpace(raw)) {
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

// checkWorkspaceLive warns about a lane that runs in the working tree: a
// file it reads that its manifest doesn't pin can change after approval.
func checkWorkspaceLive(c *Context, s *Script) []finding.Finding {
	if s.Info.Workspace != lane.WorkspaceLive {
		return nil
	}
	line := -1
	for i, raw := range s.v.raw {
		if strings.Contains(raw, "Workspace:") {
			line = i
			break
		}
	}
	return []finding.Finding{f(finding.Warn, "workspace-live", line, "pin the files it reads (# Includes:) and drop the line, so it runs in its staged copy",
		"runs in the working tree (# Workspace: live): files it reads but doesn't pin can change after it's approved")}
}

// treeTools check or build their working directory when given no paths
// (or ./...): in a staged copy with nothing pinned they read nothing, and
// some pass on it (gofmt -l). Each maps to the subcommands
// that do (nil: any).
var treeTools = map[string][]string{
	"go":            {"vet", "test", "build", "list", "fmt"},
	"gofmt":         nil,
	"goimports":     nil,
	"golangci-lint": {"run"},
	"staticcheck":   nil,
	"cargo":         {"build", "check", "test", "clippy", "fmt"},
	"pytest":        nil,
	"ruff":          {"check", "format"},
	"black":         nil,
	"mypy":          nil,
	"eslint":        nil,
	"prettier":      nil,
	"terraform":     {"plan", "validate", "fmt"},
	"tofu":          {"plan", "validate", "fmt"},
	"tflint":        nil,
}

// treeCommand reports whether words run a tool over the working directory:
// a tree tool above, or any command given ./... to read. (swim lint reads
// the repo: SWIM_ROOT names it.)
func treeCommand(words []lane.Word) bool {
	if len(words) == 0 {
		return false
	}
	for _, w := range words[1:] {
		if !w.Expands && w.Text == "./..." {
			return true
		}
	}
	name := words[0].Text[strings.LastIndexByte(words[0].Text, '/')+1:]
	subs, ok := treeTools[name]
	if !ok {
		return false
	}
	if subs == nil {
		return true
	}
	for _, w := range words[1:] {
		if strings.HasPrefix(w.Text, "-") {
			continue
		}
		for _, sub := range subs {
			if w.Text == sub {
				return true
			}
		}
		return false
	}
	return false
}

// checkWorkspaceEmpty warns about a staged lane that runs a tool over its
// whole directory but pins nothing for it: the stage holds only the lane's
// script, so the tool checks an empty copy (gofmt -l passes on one, go vet
// fails with "matched no packages").
func checkWorkspaceEmpty(c *Context, s *Script) []finding.Finding {
	if s.Info.Workspace == lane.WorkspaceLive || len(s.Info.Includes) > 0 {
		return nil
	}
	switch strings.ToLower(filepath.Ext(s.Name)) {
	case ".sh", ".bash", "":
	default:
		return nil
	}
	for _, lc := range s.v.commands() {
		words := stepCommand(lc.Words)
		if !treeCommand(words) {
			continue
		}
		texts := make([]string, len(words))
		for k, w := range words {
			texts[k] = w.Text
		}
		return []finding.Finding{f(finding.Warn, "workspace-empty", lc.Line, "add `# Workspace: live` to run it in the working tree, or pin what it reads with # Includes: (filepath globs, one directory each: `go.mod go.sum *.go cmd/*/*.go`)",
			"%q runs in the lane's staged copy, which holds only %s (no # Includes:): it reads none of the repo, so it checks nothing", strings.Join(texts, " "), s.scriptName())}
	}
	return nil
}

// A repoRead is a repo file a staged lane's step names as a literal
// argument (cat deploy.yaml, bash scripts/x.sh) without # Includes:
// listing it: the staged copy doesn't hold it.
type repoRead struct {
	Rel  string
	Line int
}

// unstagedReads are the repo files a staged bash lane's steps name that its
// # Includes: doesn't cover, in order, once each.
func unstagedReads(root string, s *Script) []repoRead {
	if root == "" || s.v == nil || s.Info.Workspace == lane.WorkspaceLive {
		return nil
	}
	switch strings.ToLower(filepath.Ext(s.Name)) {
	case ".sh", ".bash", "":
	default:
		return nil
	}
	var out []repoRead
	seen := map[string]bool{}
	for _, lc := range s.v.commands() {
		words := stepCommand(lc.Words)
		if len(words) > 0 && (words[0].Text == "source" || words[0].Text == ".") {
			continue // the source check's
		}
		for _, w := range words {
			p := w.Text
			if w.Expands || p == "" || strings.HasPrefix(p, "-") || filepath.IsAbs(p) || strings.ContainsAny(p, "*?[=:") {
				continue
			}
			rel := filepath.ToSlash(filepath.Clean(p))
			if rel == "." || strings.HasPrefix(rel, "../") || rel == ".." || strings.HasPrefix(rel, ".swim/") || seen[rel] || rel == s.Name {
				continue
			}
			if st, err := os.Stat(filepath.Join(root, rel)); err != nil || !st.Mode().IsRegular() {
				continue
			}
			if s.included(rel) {
				continue
			}
			seen[rel] = true
			out = append(out, repoRead{rel, lc.Line})
		}
	}
	return out
}

// checkWorkspaceFile warns about a staged lane that names a repo file its
// # Includes: doesn't list: it isn't in the staged copy, so the step fails
// with "No such file or directory" under swim run.
func checkWorkspaceFile(c *Context, s *Script) []finding.Finding {
	var out []finding.Finding
	for _, r := range unstagedReads(c.Root, s) {
		out = append(out, f(finding.Warn, "workspace-file", r.Line, "add `# Includes: "+r.Rel+"` (swim lint --fix adds it), or `# Workspace: live`",
			"%s is in the repo but not in # Includes:, so the lane's staged copy doesn't have it: the step reading it fails under swim run", r.Rel))
	}
	return out
}

// scriptName is the script's file name, or lane.N.sh when it has none yet.
func (s *Script) scriptName() string {
	if s.Name != "" {
		return s.Name
	}
	return fmt.Sprintf("lane.%d.sh", s.N)
}

// checkTask checks the lane's name (# Task:): swim run NAME must find this
// lane and nothing else.
func checkTask(c *Context, s *Script) []finding.Finding {
	t := s.Info.Task
	if t == "" {
		return nil
	}
	line := -1
	for i, raw := range s.v.raw {
		if strings.Contains(raw, "Task:") {
			line = i
			break
		}
	}
	switch {
	case !lane.ValidTaskName(t):
		return []finding.Finding{f(finding.Error, "task", line, "use a-z, 0-9 and -, starting with a letter, at most 40 (and not only hex digits and -, which reads as a job id)",
			"# Task: %q isn't a task name: swim run can't find the lane by it", t)}
	case isCommand(t):
		return []finding.Finding{f(finding.Error, "task", line, "pick another name",
			"# Task: %s is a swim command: swim %s runs the command, not this lane", t, t)}
	}
	for n, o := range c.Scripts {
		if n != s.N && o.Info.Task == t {
			return []finding.Finding{f(finding.Error, "task", line, "give each lane its own name",
				"# Task: %s is swim %d's name too: swim run %s can't tell them apart", t, n, t)}
		}
	}
	return nil
}

// isCommand reports whether name is a swim command.
func isCommand(name string) bool {
	_, ok := help.Commands[name]
	return ok || name == "help" || name == "guide"
}

var placeholderRE = regexp.MustCompile(`\{\{[^}]*\}\}|<round goal>|replace with a read-only describe/list command`)

func checkPlaceholder(c *Context, s *Script) []finding.Finding {
	var out []finding.Finding
	for i, raw := range s.v.raw { // raw: a placeholder is wrong wherever it is
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
	fix := "name a configured lane number, a task (# Task:) or a job id a lane holds"
	return []finding.Finding{f(finding.Error, "after", s.headerLine("After"), fix, "%s; swim run refuses this lane", msg)}
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

// stages lists the script's literal `stage NAME` calls (0-based line, name).
func (s *Script) stages() (lines []int, names []string) {
	for _, c := range s.v.commands() {
		w := c.Words
		if c.Sub || w[0].Text != "stage" || w[0].Quoted {
			continue
		}
		name := ""
		if len(w) > 1 {
			if !literal(w[1]) {
				continue
			}
			name = w[1].Text
		}
		lines, names = append(lines, c.Line), append(names, name)
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
				out = append(out, f(finding.Warn, "bash4", i, "rewrite it for bash 3.2 (see `swim guide`, WRITING A LANE SCRIPT)",
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
	for i, raw := range s.v.raw { // raw for the header lines; uses are read from bare
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

// checkGuardUnlisted: a lane's guards are what its header declares, so a
// guard it uses without declaring is refused when it asks (a dry run). With
// "# Guards: *" any flag may be asked for, and an unlisted one only lacks
// its description.
func checkGuardUnlisted(c *Context, s *Script) []finding.Finding {
	listed, used, order := s.guardFlags()
	var out []finding.Finding
	for _, flag := range order {
		if _, ok := listed[flag]; ok {
			continue
		}
		fix := fmt.Sprintf("add `# Guards: %s  <what it approves, date, reason>` to the header (swim lint --fix adds it)", flag)
		if s.Info.DynamicGuards > 0 {
			out = append(out, f(finding.Warn, "guard-unlisted", used[flag], fix,
				"guard flag %s is used but not listed in `# Guards:`; the operator reads the header to know what it approves", flag))
			continue
		}
		out = append(out, f(finding.Error, "guard-unlisted", used[flag], fix,
			"guard flag %s is used but not declared in `# Guards:`: swim refuses it when it asks (a dry run)", flag))
	}
	return out
}

// checkGuardDynamic: a guard whose flag is named at run time (guard
// "$FLAG") may ask for any flag only if the header says so: # Guards: *.
func checkGuardDynamic(c *Context, s *Script) []finding.Finding {
	if s.Info.DynamicGuards > 0 {
		return nil
	}
	var out []finding.Finding
	for _, lc := range s.v.commands() {
		i, cmd := lc.Line, lc.Words
		if !lc.Sub && len(cmd) > 1 && cmd[0].Text == "guard" && cmd[1].Expands {
			out = append(out, f(finding.Error, "guard-dynamic", i, "add `# Guards: *  <which flags, and why they're named at run time>` (swim lint --fix adds it), or name the flag",
				"guard %s names its flag at run time, but the header doesn't say `# Guards: *`: swim refuses it when it asks", cmd[1].Text))
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
	blockRE = regexp.MustCompile(`[A-Za-z0-9_]+|&&|!`)
)

// talk are the commands that only say something: a destructive word in
// their arguments is prose.
var talk = map[string]bool{"echo": true, "printf": true, "stop": true, "drift": true, "confirm": true, "guard": true, ":": true, "msg": true, "note": true}

// stepCommand is the command a step runs: run/gate/snapshot
// [options] "<label>" cmd... gives cmd...; any other command is itself.
func stepCommand(words []lane.Word) []lane.Word {
	if len(words) == 0 {
		return nil
	}
	switch words[0].Text {
	case "run", "gate", "snapshot":
	default:
		return words
	}
	k := 1
	for k < len(words) && strings.HasPrefix(words[k].Text, "--") {
		if stepValueOpts[words[k].Text] {
			k++
		}
		k++
	}
	if k < len(words) {
		k++ // the label
	}
	if k < len(words) && words[k].Text == "--" {
		k++
	}
	return words[k:]
}

// A ran is a command the script runs, as text: the step's command for
// run/gate/snapshot (its label and options dropped), with any
// bash -c body as one of its words. Commands that only talk (echo, stop,
// swim note, ...) aren't listed.
type ran struct {
	Line int
	Text string
}

// ranCommands lists the commands the bash script runs (ran).
func (s *Script) ranCommands() []ran {
	var out []ran
	for _, lc := range s.v.commands() {
		if lc.Sub {
			continue // in its bash -c word's text already
		}
		words := stepCommand(lc.Words)
		if len(words) == 0 || talk[words[0].Text] || len(words) > 1 && isSwim(words[0]) && words[1].Text == "note" {
			continue
		}
		texts := make([]string, len(words))
		for k, w := range words {
			texts[k] = w.Text
		}
		out = append(out, ran{lc.Line, strings.Join(texts, " ")})
	}
	return out
}

// stepValueOpts are a step's options that take a value.
var stepValueOpts = map[string]bool{"--timeout": true, "--retry": true, "--backoff": true, "--retry-on": true, "--key": true, "--target": true}

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
	flagged := map[int]bool{}
	for _, r := range s.ranCommands() {
		i := r.Line
		if guarded[i] || flagged[i] {
			continue
		}
		if m := destructiveRE.FindStringSubmatch(r.Text); m != nil {
			flagged[i] = true
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

// secretUseRE is an SDK's call to the secret store swim no longer has:
// secret.get("NAME") / Secret.Get("NAME"), or ["swim", "secret", ...].
var secretUseRE = regexp.MustCompile(`\b(?:secret\.get|Secret\.Get)\(|['"]swim['"]\s*,\s*['"]secret['"]`)

// secretUses lists the lines where the script uses the secret store swim
// no longer has: in bash, swim secret commands (in a $(...) too, never in
// quoted text or a comment); in another language, the SDK's calls as
// written.
func (s *Script) secretUses() []int {
	var out []int
	if s.bash() {
		for _, c := range s.v.commands() {
			if w := c.Words; len(w) > 1 && isSwim(w[0]) && w[1].Text == "secret" {
				out = append(out, c.Line)
			}
		}
		return out
	}
	for i, line := range s.v.code {
		if s.v.isCode(i) && secretUseRE.MatchString(line) {
			out = append(out, i)
		}
	}
	return out
}

func checkHeader(c *Context, s *Script) []finding.Finding {
	var out []finding.Finding
	for _, p := range s.Info.Problems {
		if isCacheProblem(p) {
			continue // checkCache
		}
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
	// code: a path in quotes is still the path a command reads or writes.
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
	secretOutputRE = regexp.MustCompile(`(?i)(^|/|[._-])(\.?env|secrets?|credentials?|passwords?|tokens?|id_rsa|id_ed25519|.*\.pem|.*\.key)($|[._/-])`)
	inputUseRE     = regexp.MustCompile(`(?:^|[\s(;&|$])input\s+([0-9]+)\s`)
)

// checkFailSyntax: a round with Fail: must declare script syntax 3, or an
// older swim would run it soft.
func checkFailSyntax(c *Context, s *Script) []finding.Finding {
	if s.Info.FailText == "" || s.Info.Syntax >= 3 {
		return nil
	}
	return []finding.Finding{f(finding.Error, "fail", -1, "change the syntax line to `# swim: syntax 3`",
		"uses Fail: but declares syntax %d: an older swim would run the round without it", s.Info.Syntax)}
}

// checkOutputs: output files aren't masked, so a name that suggests secrets is
// worth a second look.
func checkOutputs(c *Context, s *Script) []finding.Finding {
	var out []finding.Finding
	for _, name := range s.Info.Outputs {
		if secretOutputRE.MatchString(name) {
			out = append(out, f(finding.Warn, "outputs", s.headerLine("Outputs"), "never put secret values in outputs: output files aren't masked",
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
	report := func(i int, n string) {
		if !deps[n] {
			out = append(out, f(finding.Error, "input-dep", i, "add "+n+" to the round's After:",
				"input %s reads swim %s's outputs, but swim %s isn't in After: (or config deps)", n, n, n))
		}
	}
	if s.bash() {
		// input N NAME, or swim input N NAME: a command, never quoted text.
		for _, lc := range s.v.commands() {
			w := lc.Words
			if len(w) > 0 && isSwim(w[0]) {
				w = w[1:]
			}
			if len(w) > 2 && w[0].Text == "input" && !w[0].Quoted && chars.Word(w[1].Text, chars.Digit, chars.Digit) {
				report(lc.Line, w[1].Text)
			}
		}
		return out
	}
	for i, line := range s.v.raw {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for _, m := range inputUseRE.FindAllStringSubmatch(line+" ", -1) {
			report(i, m[1])
		}
	}
	return out
}

// changeLine is the line of the script's first `stage change` (or
// verify) command, or the line count if it has none.
func (s *Script) changeLine() int {
	for _, c := range s.v.commands() {
		w := c.Words
		if len(w) > 0 && isSwim(w[0]) {
			w = w[1:]
		}
		if !c.Sub && len(w) > 1 && w[0].Text == "stage" && !w[0].Quoted && (w[1].Text == "change" || w[1].Text == "verify") {
			return c.Line
		}
	}
	return len(s.v.code)
}

var (
	applyRE = regexp.MustCompile(`(?i)\b(terraform\s+(?:apply|destroy|import)|kubectl\s+(?:apply|delete|scale|rollout)|helm\s+(?:install|upgrade|uninstall)|aws\s+\S+\s+(?:delete|put|update|create)-\S+|sst\s+(?:deploy|remove))\b`)
)

// checkCheckUnsafe: everything before the first `stage change` belongs to
// the snapshot and check stages, which should only read, so a change there
// (terraform apply, a delete) runs before the round says it changes
// anything.
func checkCheckUnsafe(c *Context, s *Script) []finding.Finding {
	if !s.Info.Staged {
		return nil // no stages: nothing claims to be read-only
	}
	var out []finding.Finding
	change := s.changeLine()
	flagged := map[int]bool{}
	for _, r := range s.ranCommands() {
		i, cmd := r.Line, r.Text
		if i >= change {
			break
		}
		if flagged[i] {
			continue
		}
		m := applyRE.FindString(cmd)
		if m == "" {
			if d := destructiveRE.FindStringSubmatch(cmd); d != nil {
				m = d[1]
				if m == "" {
					m = "rm -r"
				}
			}
		}
		if m != "" {
			flagged[i] = true
			out = append(out, f(finding.Warn, "check-unsafe", i, "move it after `stage change` (snapshot and check stages must be read-only)",
				"%q runs before `stage change`, in a stage that should only read", strings.ToLower(m)))
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
		cands := importCandidates(ext, line)
		for _, cand := range cands {
			if st, err := os.Stat(filepath.Join(c.Root, cand)); err == nil && !st.IsDir() {
				if !covered(cand) {
					out = append(out, f(finding.Error, "includes", i, "add `# Includes: "+cand+"` (globs work; swim lint --fix adds it)",
						"imports local %s, which isn't in # Includes:: it isn't staged, so the import fails under swim run", cand))
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

// checkSource: a bash lane that sources a local file must list it in #
// Includes:, which is what swim pins and stages: under swim run, sourcing a
// repo file the header doesn't list stops the round. A path built at run
// time can't be pinned at all: what it runs is outside what was approved.
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
	for _, src := range scanSources(c.Root, s.Name, s.Src) {
		switch {
		case src.Dynamic:
			out = append(out, f(finding.Error, "source", src.Line-1, "source a literal repo path, listed in # Includes:",
				"sources %s, a path known only at run time: swim can't pin or stage it, so no approval covers what it runs", src.Arg))
		case !s.included(src.Path):
			out = append(out, f(finding.Error, "source", src.Line-1, "add `# Includes: "+src.Path+"` (globs work; swim lint --fix adds it)",
				"sources local %s, which isn't in # Includes:: under swim run that stops the round", src.Path))
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

var handlerDefRE = regexp.MustCompile(`^\s*(?:function\s+)?(on_success|on_failure|on_exit)\s*(?:\(\s*\))?\s*\{?\s*$`)

// sdkModuleRE is an SDK's module call, r.module("notify", ...) or
// r.Module(...), which swim no longer answers.
var sdkModuleRE = regexp.MustCompile(`\.[Mm]odule(?:Env|_env|With)?\(\s*["'\x60]`)

// checkRemoved: a header key, outcome handler, module call, secret
// store call or evidence step of a feature swim no longer has. swim
// would ignore the header and the handler, so a cleanup or rollback the
// script counts on would never run; a module call or evidence step would
// fail mid-round. Refuse the lane instead.
func checkRemoved(c *Context, s *Script) []finding.Finding {
	var out []finding.Finding
	for _, k := range s.Info.Removed {
		if k == "Modules" {
			out = append(out, f(finding.Error, "removed", s.headerLine(k), "run the tool as a step (run \"<label>\" -- cmd ...), with its files in # Includes:, and drop the line",
				"# Modules: is gone: swim no longer has modules (mod, mod_env)"))
			continue
		}
		out = append(out, f(finding.Error, "removed", s.headerLine(k), "put the work in its own lane (After: for order) and drop the line",
			"# %s: is a lane kind swim no longer has; swim would ignore it", k))
	}
	if s.Lib {
		// mod / mod_env commands: never a word in quoted text or a comment.
		for _, cmd := range s.v.commands() {
			if cmd.Sub || len(cmd.Words) == 0 || cmd.Words[0].Quoted || cmd.Words[0].Text != "mod" && cmd.Words[0].Text != "mod_env" {
				continue
			}
			out = append(out, f(finding.Error, "removed", cmd.Line, "run the tool as a step instead: run \"<label>\" -- cmd ...",
				"%s calls a module, and swim no longer has modules: the step would fail", cmd.Words[0].Text))
		}
	} else {
		for i, line := range s.v.code {
			if s.v.isCode(i) && sdkModuleRE.MatchString(line) {
				out = append(out, f(finding.Error, "removed", i, "run the tool as a step instead (the SDK's run)",
					"a module call, and swim no longer has modules: the step would fail"))
			}
		}
	}
	for _, i := range s.Info.EvidenceLines {
		out = append(out, f(finding.Error, "removed", i, "use snapshot (saves read-only output, without an approval binding)",
			"evidence steps are gone: swim no longer binds approvals to a round's evidence"))
	}
	for _, i := range s.secretUses() {
		out = append(out, f(finding.Error, "removed", i, "give the lane the credential in its environment instead (the operator's own, or env.swim)",
			"swim secret is gone: swim no longer has a secret store, so getting a secret would fail"))
	}
	for i, line := range s.v.bare {
		if s.v.doc[i] {
			continue
		}
		if m := handlerDefRE.FindStringSubmatch(line); m != nil {
			out = append(out, f(finding.Error, "removed", i, "put the rollback in its own lane (After: for order), or a guarded step of this one",
				"%s is an outcome handler, which swim no longer calls: it would never run", m[1]))
		}
	}
	return out
}
