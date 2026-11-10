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
	// RoundID: the line (1-based) whose # Job: it rewrote to # Round-ID:.
	RoundID int
	// Shortened: the lines (1-based) whose guard FLAG "<text>" it rewrote
	// to guard FLAG, the text being the header's.
	Shortened []int
	Skip      string // why it wasn't changed ("" if it was, or had nothing to add)
}

// Fix adds the header lines a lane's script is missing for what its body
// does: # Guards: for a guard it uses without declaring (with the guard's
// own action as the description), # Guards: * for a flag it names at run
// time, # Includes: for a repo file it sources or imports. The header is
// the lane's contract (swim refuses what it doesn't declare); these are
// lines for a person to review, so it never removes or rewrites one. It
// also rewrites # Job:, Round-ID's old name, to # Round-ID:, and in a bash
// lane drops a guard call's text that only repeats its # Guards: line. It skips
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
		src, roundID := renameJob(string(data))
		fx.RoundID = roundID
		s := NewScript(n, src)
		s.Name = fx.File
		add := missingHeader(root, s)
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
		if s.Lib {
			// With the header saying it (just added or not), a guard
			// call's own copy of the text goes.
			var short []int
			lines, short = shortenGuards(NewScript(n, strings.Join(lines, "\n")))
			for _, i := range short {
				fx.Shortened = append(fx.Shortened, i+1)
			}
		}
		if len(add) == 0 && fx.RoundID == 0 && len(fx.Shortened) == 0 {
			continue
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
			add = append(add, fmt.Sprintf("Guards: %s  %s", flag, desc), "  why: <why this change is made, with its date>")
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

// guardCallRE finds a guard call with its text on a line already parsed
// as one: guard FLAG "<text>" (or '<text>'); 1 is the flag, 2 the text.
var guardCallRE = regexp.MustCompile(`\bguard\s+([A-Za-z_][A-Za-z0-9_]*)\s+("(?:[^"\\$` + "`" + `]|\\.)*"|'[^']*')`)

// shortenGuards rewrites each guard FLAG "<text>" whose text is what s's
// header says FLAG does to guard FLAG, and returns the lines and the
// (0-based) ones it changed. It edits only a line that is code (not a
// comment, a heredoc body or a string over several lines, nor one a \
// continues or that continues one) and only a call the parser reads as
// guard with that flag and text. Text that differs is left for a person.
func shortenGuards(s *Script) ([]string, []int) {
	lines := append([]string(nil), s.v.raw...)
	header := map[string]string{}
	for _, g := range s.Info.Guards {
		if !unwritten(g.Desc) {
			header[g.Flag] = strings.Join(strings.Fields(g.Desc), " ")
		}
	}
	// calls: the flags each code line calls guard with the header's text.
	calls := func(line string) map[string]bool {
		out := map[string]bool{}
		for _, c := range lane.Commands(line) {
			if len(c) == 3 && !c[0].Quoted && c[0].Text == "guard" && !c[1].Expands && !c[2].Expands &&
				header[c[1].Text] != "" && header[c[1].Text] == strings.Join(strings.Fields(c[2].Text), " ") {
				out[c[1].Text] = true
			}
		}
		return out
	}
	var changed []int
	for i, l := range lines {
		joined := strings.HasSuffix(strings.TrimRight(l, " \t"), "\\") || i > 0 && strings.HasSuffix(strings.TrimRight(lines[i-1], " \t"), "\\")
		if !s.v.isCode(i) || s.v.inQuote[i] || joined {
			continue
		}
		want := calls(l)
		if len(want) == 0 {
			continue
		}
		out := guardCallRE.ReplaceAllStringFunc(l, func(m string) string {
			if sub := guardCallRE.FindStringSubmatch(m); want[sub[1]] && len(calls(m)) == 1 {
				return "guard " + sub[1]
			}
			return m
		})
		if out != l {
			lines[i] = out
			changed = append(changed, i)
		}
	}
	return lines, changed
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
