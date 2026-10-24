package lint

import (
	"fmt"
	"os"
	"path/filepath"
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
	Skip  string   // why it wasn't changed ("" if it was, or had nothing to add)
}

// Fix adds the header lines a lane's script is missing for what its body
// does: # Guards: for a guard it uses without declaring (with the guard's
// own action as the description), # Guards: * for a flag it names at run
// time, # Includes: for a repo file it sources or imports. The header is
// the lane's contract (swim refuses what it doesn't declare); these are
// lines for a person to review, so it never removes or rewrites one. It
// skips running lanes, lanes of jobs (each job declares its own guards)
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
		s := NewScript(n, string(data))
		s.Name = fx.File
		add := missingHeader(root, s)
		if len(add) == 0 {
			continue
		}
		lines := strings.Split(string(data), "\n")
		prefix := "# "
		if commentStyle(lines) == "//" {
			prefix = "// "
		}
		for i := range add {
			add[i] = prefix + add[i]
		}
		at := headerEnd(lines)
		lines = slices.Insert(lines, at, add...)
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
