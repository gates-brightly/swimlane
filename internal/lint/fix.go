package lint

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
)

// A fixed lane: the header lines Fix added to its script, or why it left
// the lane alone.
type Fixed struct {
	Lane  int
	File  string
	Added []string // header lines added, as written
	// Renamed: the lines (1-based) whose run calls it rewrote to step.
	// Remove after the next release (T38).
	Renamed []int
	Skip    string // why it wasn't changed ("" if it was, or had nothing to add)
}

// Fix adds the header lines a lane's script is missing for what its body
// does: # Guards: for a guard it uses without declaring (with the guard's
// own action as the description), # Guards: * for a flag it names at run
// time, # Includes: for a repo file it sources or imports. The header is
// the lane's contract (swim refuses what it doesn't declare); these are
// lines for a person to review, so it never removes or rewrites one. In a
// bash lane it also rewrites run, step's old name, to step. It skips
// running lanes, lanes of jobs (each job declares its own guards)
// and compiled lanes.
func Fix(root string, cfg *config.Config, lanes []int) ([]Fixed, error) {
	if len(lanes) == 0 {
		for n := 1; n <= cfg.Lanes; n++ {
			lanes = append(lanes, n)
		}
	}
	var out []Fixed
	for _, n := range lanes {
		path := lane.Script(root, n)
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return out, err
		}
		fx := Fixed{Lane: n, File: filepath.Base(path)}
		switch {
		case lane.IsBinary(data):
			continue
		default:
			if _, running := lane.Running(root, n); running {
				fx.Skip = "running: fix it once the round ends"
			}
		}
		if fx.Skip != "" {
			out = append(out, fx)
			continue
		}
		src := string(data)
		if s := NewScript(n, src); s.Lib {
			var renamed []int
			src, renamed = renameRun(src)
			for _, i := range renamed {
				fx.Renamed = append(fx.Renamed, i+1)
			}
		}
		s := NewScript(n, src)
		s.Name = fx.File
		add := missingHeader(root, s)
		if len(add) == 0 && len(fx.Renamed) == 0 {
			continue
		}
		lines := strings.Split(src, "\n")
		prefix := "# "
		if commentStyle(lines) == "//" {
			prefix = "// "
		}
		for i := range add {
			add[i] = prefix + add[i]
		}
		if len(add) > 0 {
			lines = slices.Insert(lines, headerEnd(lines), add...)
		}
		st, err := os.Stat(path)
		if err != nil {
			return out, err
		}
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), st.Mode().Perm()); err != nil {
			return out, err
		}
		fx.Added = add
		out = append(out, fx)
	}
	return out, nil
}

// missingHeader lists the header lines (without their comment prefix) the
// script needs for what its body does.
func missingHeader(root string, s *Script) []string {
	var add []string
	listed, used, order := s.guardFlags()
	if s.Lib {
		for _, flag := range order {
			if _, ok := listed[flag]; ok || s.Info.DynamicGuards > 0 {
				continue
			}
			desc := guardAction(s.v.raw[used[flag]], flag)
			if desc == "" {
				desc = "<what it approves, date, reason>"
			}
			add = append(add, fmt.Sprintf("Guards: %s  %s", flag, desc))
		}
		if s.Info.DynamicGuards == 0 && len(checkGuardDynamic(nil, s)) > 0 {
			add = append(add, "Guards: *  flags named at run time (say which, and why)")
		}
	}
	var includes []string
	if root != "" {
		for _, src := range scanSources(root, s.Name, s.Src) {
			if !src.Dynamic && !s.included(src.Path) && !slices.Contains(includes, src.Path) {
				includes = append(includes, src.Path)
			}
		}
		for _, r := range unstagedReads(root, s) {
			if !slices.Contains(includes, r.Rel) {
				includes = append(includes, r.Rel)
			}
		}
	}
	if len(includes) > 0 {
		add = append(add, "Includes: "+strings.Join(includes, " "))
	}
	return add
}

// guardAction is the action a guard call gives for flag on a line (guard
// FLAG "<action>"), "" if it isn't written out.
func guardAction(line, flag string) string {
	for _, c := range lane.Commands(line) {
		if len(c) > 2 && c[0].Text == "guard" && c[1].Text == flag && !c[2].Expands {
			return c[2].Text
		}
	}
	return ""
}

// commentStyle is how the script's header comments start: "#", or "//"
// (lane.N.js, lane.N.ts, lane.N.go).
func commentStyle(lines []string) string {
	for i, l := range lines {
		if i > 20 {
			break
		}
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "//") && strings.Contains(t, ":") {
			return "//"
		}
	}
	return "#"
}

// headerEnd is the line index just after the script's header block: the
// shebang, the syntax line and the `# Key: value` lines at its top.
func headerEnd(lines []string) int {
	end := 0
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if i == 0 && strings.HasPrefix(t, "#!") {
			end = 1
			continue
		}
		t = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(t, "//"), "#"))
		k, _, ok := strings.Cut(t, ":")
		if !strings.HasPrefix(strings.TrimSpace(l), "#") && !strings.HasPrefix(strings.TrimSpace(l), "//") || !ok || k == "" || strings.ContainsAny(k, " \t") && k != "swim" {
			break
		}
		end = i + 1
	}
	return end
}

// runCalls finds the calls of run, the old name of the library's step
// function, in a bash lane: the column of each run that starts a
// statement (at the start of a line, or after ; & | ( { ) ! `, then, do,
// else, if, elif, while or until, or first in a "$(...)") and has an
// argument after it. Never one in quoted text, a comment or a heredoc body
// (the bare view blanks those), on a line a \ continues, or in a lane that defines a run of its
// own. Remove after the next release (T38), with the run alias.
func runCalls(v *view) map[int][]int {
	for i, line := range v.bare {
		if v.isCode(i) && runDefRE.MatchString(line) {
			return nil
		}
	}
	out := map[int][]int{}
	cont := false
	for i, line := range v.exp {
		wasCont := cont
		cont = v.isCode(i) && continued(v.code[i])
		if !v.isCode(i) || v.inQuote[i] {
			continue
		}
		for j := 0; j+3 < len(line); j++ {
			if line[j:j+3] != "run" || line[j+3] != ' ' && line[j+3] != '\t' || strings.TrimSpace(line[j+3:]) == "" {
				continue
			}
			if j > 0 && !strings.ContainsRune(" \t;&|({)!`", rune(line[j-1])) {
				continue // part of a longer word (npm-run, xrun)
			}
			before := strings.TrimRight(line[:j], " \t")
			switch {
			case v.bare[i][j:j+3] == "run":
				// Code: bare is exp with double-quoted text blanked.
				if !statementStart(strings.TrimRight(v.bare[i][:j], " \t"), wasCont) {
					continue
				}
			case strings.HasSuffix(before, "$(") || strings.HasSuffix(before, "`"):
				// "$(run ...)": quoted, but bash runs it.
			default:
				continue
			}
			out[i] = append(out[i], j)
		}
	}
	return out
}

// runDefRE: a lane's own function named run (a regular expression, kept:
// lane.Commands splits a word at its parentheses).
var runDefRE = regexp.MustCompile(`(?:^|[\s;&|{])(?:function\s+run\b|run\s*\(\s*\))`)

// statementStart reports whether a command word after before (the line's
// text up to it, trailing blanks trimmed) starts a statement.
func statementStart(before string, continued bool) bool {
	if before == "" {
		return !continued
	}
	if strings.ContainsRune(";&|({)!`", rune(before[len(before)-1])) {
		return true
	}
	k := strings.LastIndexAny(before, " \t;&|(){}!`")
	switch before[k+1:] {
	case "then", "do", "else", "if", "elif", "while", "until":
		return k < 0 || statementStart(strings.TrimRight(before[:k+1], " \t"), continued)
	}
	return false
}

// renameRun rewrites src's run calls (runCalls) to step: what swim lint
// --fix does for a bash lane. It returns the new text and the 0-based
// lines it changed. Remove after the next release (T38).
func renameRun(src string) (string, []int) {
	calls := runCalls(scan(src))
	if len(calls) == 0 {
		return src, nil
	}
	lines := strings.Split(src, "\n")
	var changed []int
	for i, cols := range calls {
		l := lines[i]
		for k := len(cols) - 1; k >= 0; k-- {
			l = l[:cols[k]] + "step" + l[cols[k]+3:]
		}
		lines[i] = l
		changed = append(changed, i)
	}
	slices.Sort(changed)
	return strings.Join(lines, "\n"), changed
}
