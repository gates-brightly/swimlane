package lint

import (
	"regexp"
	"strings"

	"github.com/gates-brightly/swimlane/internal/finding"
	"github.com/gates-brightly/swimlane/internal/module"
)

var (
	// modUseRE is a bash mod or mod_env call, where a command starts (not
	// go mod download); the rest of the line follows.
	modUseRE = regexp.MustCompile(`(?:^\s*|[;&|({!]\s*|\b(?:then|do|else|if|while|until)\s+)(mod|mod_env)\s+(.*)$`)
	// sdkModuleRE is an SDK's r.module("notify", ...) or r.Module("notify", ...).
	sdkModuleRE = regexp.MustCompile(`\.[Mm]odule(Env|_env)?\(\s*["'\x60]([a-z][a-z0-9_-]*)["'\x60]`)
	// sdkModuleEnvRE is the SDKs' env form: env=True, env: true, Env: true.
	sdkModuleEnvRE = regexp.MustCompile(`\b[Ee]nv\s*[=:]\s*(?:True|true)\b`)
)

// modOptsWithValue are mod's options that take a value.
var modOptsWithValue = map[string]bool{"--label": true, "--timeout": true, "--retry": true, "--backoff": true, "--retry-on": true}

// modCall is one mod or mod_env call a script makes.
type modCall struct {
	Line int // 0-based
	Cmd  string
	Env  bool // mod_env (or an SDK call with env)
}

// modCalls lists the module commands the script calls by a literal name.
func (s *Script) modCalls() []modCall {
	var out []modCall
	for i, line := range s.v.code {
		if !s.v.isCode(i) {
			continue
		}
		if m := modUseRE.FindStringSubmatch(line); m != nil && s.Lib {
			words := shellWords(m[2])
			for k := 0; k < len(words); k++ {
				w := words[k]
				if modOptsWithValue[w] {
					k++
					continue
				}
				if strings.HasPrefix(w, "--") {
					continue
				}
				if !strings.ContainsAny(w, "$`") {
					out = append(out, modCall{Line: i, Cmd: w, Env: m[1] == "mod_env"})
				}
				break
			}
		}
		for _, m := range sdkModuleRE.FindAllStringSubmatch(line, -1) {
			out = append(out, modCall{Line: i, Cmd: m[2], Env: m[1] != "" || sdkModuleEnvRE.MatchString(line)})
		}
	}
	return out
}

// shellWords splits a line into words, keeping quoted parts together (and
// dropping the quotes). Enough for mod's options and command name.
func shellWords(s string) []string {
	var out []string
	var cur strings.Builder
	in, has := byte(0), false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case in != 0 && c == in:
			in = 0
		case in != 0:
			cur.WriteByte(c)
		case c == '\'' || c == '"':
			in, has = c, true
		case c == ' ' || c == '\t':
			if cur.Len() > 0 || has {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		case c == ';' || c == '&' || c == '|' || c == ')' || c == '}':
			if cur.Len() > 0 || has {
				out = append(out, cur.String())
			}
			return out
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 || has {
		out = append(out, cur.String())
	}
	return out
}

// checkModules: the modules a script declares (# Modules:) resolve and
// swim.yml allows them, and every module command it calls is one of
// theirs. swim refuses both at run time: the lane doesn't start, the step
// fails.
func checkModules(c *Context, s *Script) []finding.Finding {
	var out []finding.Finding
	at := max(s.headerLine("Modules"), 0)
	mods, err := module.Resolve(c.Root, c.Cfg.Modules, s.Info.Modules)
	if err != nil {
		for _, e := range strings.Split(err.Error(), "\n") {
			out = append(out, f(finding.Error, "module", at, "see swim modules for what's available, and swim.yml modules: for what's allowed", "%s", e))
		}
	}
	var all []*module.Module
	for _, call := range s.modCalls() {
		if module.ForCommand(mods, call.Cmd) != nil {
			continue
		}
		if all == nil {
			all, _ = module.Available(c.Root, c.Cfg.Modules)
		}
		if m := module.ForCommand(all, call.Cmd); m != nil {
			if !s.declares(m) { // declared, but it didn't resolve: reported above
				out = append(out, f(finding.Error, "module", call.Line, "add `# Modules: "+m.Name+"` (undeclared modules are refused at the step)",
					"module %s isn't declared, so %s can't run", m.Name, call.Cmd))
			}
			continue
		}
		out = append(out, f(finding.Error, "module", call.Line, "swim modules lists the modules and their commands", "no module has a command %s", call.Cmd))
	}
	return out
}

// declares reports whether the script's # Modules: names m.
func (s *Script) declares(m *module.Module) bool {
	for _, ref := range s.Info.Modules {
		if r, err := module.ParseRef(ref); err == nil && (r.Name == m.Name || (r.Path != "" && r.Path == m.Dir)) {
			return true
		}
	}
	return false
}

// checkModuleEffects: a module's declared effects against how the script
// uses it. A module that gives credentials needs mod_env; one that isn't
// read-only shouldn't run before stage change (swim plan --check runs it).
func checkModuleEffects(c *Context, s *Script) []finding.Finding {
	mods, _ := module.Resolve(c.Root, c.Cfg.Modules, s.Info.Modules)
	if len(mods) == 0 {
		return nil
	}
	change := len(s.v.code)
	if s.Info.Staged {
		for i, line := range s.v.code {
			if s.v.isCode(i) && stageChangeRE.MatchString(line) {
				change = i
				break
			}
		}
	}
	var out []finding.Finding
	for _, call := range s.modCalls() {
		m := module.ForCommand(mods, call.Cmd)
		if m == nil {
			continue
		}
		if m.Effects.Credentials && !call.Env {
			out = append(out, f(finding.Warn, "module-effects", call.Line, "use mod_env "+call.Cmd+" (the SDKs: env=True)",
				"module %s gives the lane credentials, but mod drops them (the step fails)", m.Name))
		}
		if s.Info.Staged && call.Line < change && !m.Effects.ReadOnly {
			out = append(out, f(finding.Warn, "module-effects", call.Line, "move it after `stage change`, or use a module whose module.yml says read_only: true",
				"module %s isn't read-only, and runs before `stage change`, so swim plan --check would run it for real", m.Name))
		}
	}
	return out
}
