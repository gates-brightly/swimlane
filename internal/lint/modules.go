package lint

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gates-brightly/swimlane/helpers"
	"github.com/gates-brightly/swimlane/internal/finding"
)

// A bash lane's code includes the modules it sources. swim's own, as it
// ships them, are tested with swim; any other (a repo's own module, a
// copy edited since swim helpers add wrote it) is linted line by line as
// the lane is, on its own file, for what can be read without the lane:
// destructive commands, blocked commands, bash 3.2, BSD tools, how steps
// are written, secrets. A function whose comment declares "# Changes:
// WHAT" may change things: lint leaves its body alone, and holds the
// lane's calls of it to the rules for a destructive command instead.

// moduleChecks are the checks run on a module's lines (by function, not
// from Checks, which holds checkModules).
var moduleChecks = []func(*Context, *Script) []finding.Finding{
	checkDestructive, checkBlocked, checkBash4, checkGNUOnly, checkStepCall, checkStepSubshell, checkStepPipe, checkSecretEcho,
}

// modulesCheck is checkModules, set at init: it applies a module's
// disable comments, which reads Checks, so Checks can't name it directly.
var modulesCheck func(*Context, *Script) []finding.Finding

func init() { modulesCheck = checkModules }

// module is a bash file a lane sources.
type module struct {
	rel     string            // repo-relative
	src     string            // as on disk
	shipped bool              // swim's own module, as this swim ships it
	changes map[string]string // function -> what it changes (# Changes:)
}

// modules are the bash files s sources that exist in the repo, read once
// per lint run.
func (c *Context) modules(s *Script) []*module {
	if c.Root == "" || !s.Lib {
		return nil
	}
	var out []*module
	for _, src := range scanSources(c.Root, s.Name, s.Src) {
		if src.Dynamic || src.From != "source" {
			continue
		}
		m, ok := c.moduleCache[src.Path]
		if !ok {
			m = readModule(c.Root, src.Path)
			if c.moduleCache == nil {
				c.moduleCache = map[string]*module{}
			}
			c.moduleCache[src.Path] = m
		}
		if m != nil {
			out = append(out, m)
		}
	}
	return out
}

func readModule(root, rel string) *module {
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return nil
	}
	m := &module{rel: filepath.ToSlash(rel), src: string(data), changes: map[string]string{}}
	m.shipped = helpers.Check(m.src).State == helpers.Current
	for _, fn := range helpers.FuncsIn(m.src) {
		if fn.Changes == "" {
			continue
		}
		for _, n := range fn.Names {
			m.changes[n] = fn.Changes
		}
	}
	return m
}

// changingCalls: a lane's calls of module functions that declare they
// change something, outside a guard block. Read as checkDestructive reads
// a destructive command.
func changingCalls(c *Context, s *Script) []finding.Finding {
	declared := map[string]string{} // function -> "what (file)"
	for _, m := range c.modules(s) {
		for fn, what := range m.changes {
			declared[fn] = what + " (" + m.rel + ")"
		}
	}
	if len(declared) == 0 {
		return nil
	}
	guarded := s.guardedLines()
	var out []finding.Finding
	for _, r := range s.ranCommands() {
		if guarded[r.Line] {
			continue
		}
		fn, _, _ := strings.Cut(strings.TrimSpace(r.Text), " ")
		if what, ok := declared[fn]; ok {
			out = append(out, f(finding.Warn, "destructive", r.Line, "wrap it: if guard FLAG \"<action - date, reason>\"; then "+fn+" ...; fi",
				"%s changes things: %s; it isn't inside a guard block", fn, what))
		}
	}
	return out
}

// checkModules lints the modules a lane sources that aren't swim's own as
// shipped, each once per run, on its own file, with its own
// swim:lint-ignore comments.
func checkModules(c *Context, s *Script) []finding.Finding {
	var out []finding.Finding
	for _, m := range c.modules(s) {
		if m.shipped || c.modulesSeen[m.rel] {
			continue
		}
		if c.modulesSeen == nil {
			c.modulesSeen = map[string]bool{}
		}
		c.modulesSeen[m.rel] = true
		ms := &Script{Name: m.rel, Src: m.src, Lib: true, v: scan(m.src)}
		skip := declaredBodies(m)
		var fs []finding.Finding
		for _, run := range moduleChecks {
			fs = append(fs, run(c, ms)...)
		}
		var mine []finding.Finding
		for _, fd := range fs {
			if fd.Code == "destructive" && skip[fd.Line-1] {
				continue
			}
			if fd.Code == "destructive" {
				fd.Fix = "declare it in the function's comment (# Changes: WHAT) so lint holds the lane's calls of it to a guard, or " + fd.Fix
			}
			fd.File = m.rel
			fd.Message = fmt.Sprintf("%s (in a module lane %d sources)", fd.Message, s.N)
			mine = append(mine, fd)
		}
		out = append(out, applyIgnores(ms, mine)...)
	}
	return out
}

// declaredBodies are the 0-based lines of the functions in m that
// declare # Changes: (from "name() {" to the "}" that closes it at the
// start of a line).
func declaredBodies(m *module) map[int]bool {
	out := map[int]bool{}
	lines := strings.Split(m.src, "\n")
	for i := 0; i < len(lines); i++ {
		name, ok := strings.CutSuffix(lines[i], "() {")
		if !ok || m.changes[name] == "" {
			continue
		}
		for ; i < len(lines) && lines[i] != "}"; i++ {
			out[i] = true
		}
	}
	return out
}
