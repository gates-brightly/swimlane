package lint

import (
	"regexp"
	"strings"

	"github.com/gates-brightly/swimlane/internal/finding"
	"github.com/gates-brightly/swimlane/internal/module"
	"github.com/gates-brightly/swimlane/internal/version"
)

var (
	// sdkModuleRE is an SDK's r.module("notify", ...) or r.Module("notify", ...).
	sdkModuleRE = regexp.MustCompile(`\.[Mm]odule(Env|_env)?\(\s*["'\x60]([a-z][a-z0-9_-]*)["'\x60](?:\s*,\s*["'\x60]([^"'\x60]*)["'\x60])?`)
	// sdkModuleEnvRE is the SDKs' env form: env=True, env: true, Env: true.
	sdkModuleEnvRE = regexp.MustCompile(`\b[Ee]nv\s*[=:]\s*(?:True|true)\b`)
)

// modOptsWithValue are mod's options that take a value.
var modOptsWithValue = map[string]bool{"--label": true, "--timeout": true, "--retry": true, "--backoff": true, "--retry-on": true}

// modCall is one mod or mod_env call a script makes.
type modCall struct {
	Line int // 0-based
	Cmd  string
	Args []string // the literal arguments that follow (up to the first built at run time)
	Env  bool     // mod_env (or an SDK call with env)
}

// modCalls lists the module commands the script calls by a literal name.
func (s *Script) modCalls() []modCall {
	var out []modCall
	if s.Lib {
		// mod / mod_env commands: never a word in quoted text or a comment.
		for _, c := range s.v.commands() {
			if c.Sub || len(c.Words) == 0 || c.Words[0].Quoted || c.Words[0].Text != "mod" && c.Words[0].Text != "mod_env" {
				continue
			}
			words := c.Words[1:]
			for k := 0; k < len(words); k++ {
				w := words[k]
				if modOptsWithValue[w.Text] {
					k++
					continue
				}
				if strings.HasPrefix(w.Text, "--") {
					continue
				}
				if literal(w) {
					call := modCall{Line: c.Line, Cmd: w.Text, Env: c.Words[0].Text == "mod_env"}
					for _, a := range words[k+1:] {
						if !literal(a) {
							break
						}
						call.Args = append(call.Args, a.Text)
					}
					out = append(out, call)
				}
				break
			}
		}
		return out
	}
	for i, line := range s.v.code {
		if !s.v.isCode(i) {
			continue
		}
		for _, m := range sdkModuleRE.FindAllStringSubmatch(line, -1) {
			call := modCall{Line: i, Cmd: m[2], Env: m[1] != "" || sdkModuleEnvRE.MatchString(line)}
			if m[3] != "" {
				call.Args = []string{m[3]}
			}
			out = append(out, call)
		}
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
	// Syntax 4, or an older swim runs the round with every module call
	// failing outside a step.
	if len(s.Info.Modules) > 0 && s.Info.Syntax < 4 {
		out = append(out, f(finding.Error, "module", at, "change the syntax line to `# swim: syntax 4`",
			"uses modules but declares syntax %d: an older swim would run the round without them", s.Info.Syntax))
	}
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
		change = s.changeLine()
	}
	var guarded []bool
	if s.Lib {
		guarded = s.guardedLines()
	}
	var out []finding.Finding
	for _, call := range s.modCalls() {
		m := module.ForCommand(mods, call.Cmd)
		if m == nil {
			continue
		}
		if m.DestructiveCall(call.Args) && guarded != nil && !guarded[call.Line] {
			out = append(out, f(finding.Warn, "module-effects", call.Line, "put it inside `if guard FLAG \"<action>\"; then ... fi`",
				"%s %s changes things, and runs outside a guard", call.Cmd, call.Args[0]))
		}
		if m.Effects.Credentials && !call.Env {
			out = append(out, f(finding.Warn, "module-effects", call.Line, "use mod_env "+call.Cmd+" (the SDKs: env=True)",
				"module %s gives the lane credentials, but mod drops them (the step fails)", m.Name))
		}
		if s.Info.Staged && call.Line < change && !m.ReadOnlyCall(call.Args) {
			out = append(out, f(finding.Warn, "module-effects", call.Line, "move it after `stage change`, or use a module whose module.yml says read_only: true",
				"module %s isn't read-only, and runs before `stage change`, so swim plan --check would run it for real", m.Name))
		}
	}
	return out
}

// checkModuleLock: a built-in module the lane uses that the repo's
// .swim.lock doesn't pin, so another machine's swim may run another
// version of it; or one it pins at another version than this swim's,
// which swim run refuses here. Repos without a lock aren't judged.
func checkModuleLock(c *Context, s *Script) []finding.Finding {
	if c.Root == "" || len(s.Info.Modules) == 0 {
		return nil
	}
	lock, ok, err := version.ReadLock(c.Root)
	if err != nil || !ok {
		return nil
	}
	mods, _ := module.Resolve(c.Root, c.Cfg.Modules, s.Info.Modules)
	at := max(s.headerLine("Modules"), 0)
	var out []finding.Finding
	for _, m := range mods {
		if !m.Builtin {
			continue
		}
		if why := module.LockProblem(lock, m); why != "" {
			out = append(out, f(finding.Warn, "module-lock", at, "see the message: update swim, or swim modules lock", "%s", why))
		} else if _, pinned := lock.Builtin(m.Name); !pinned {
			out = append(out, f(finding.Warn, "module-lock", at, "swim modules lock (and commit .swim.lock)",
				"built-in %s %s isn't pinned in %s: another machine's swim may run another version", m.Name, m.Version, version.LockName))
		}
	}
	return out
}
