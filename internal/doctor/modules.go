package doctor

import (
	"github.com/gates-brightly/swimlane/internal/finding"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/module"
)

// checkModuleGo: a lane declares a Go module from the repo, which swim
// builds with the operator's Go toolchain, and there's none on PATH here.
func checkModuleGo(o *Options) []finding.Finding {
	if len(LookPathAll("go", o.PATH)) > 0 {
		return nil
	}
	var out []finding.Finding
	seen := map[string]bool{}
	for _, n := range o.Cfg.Active {
		info, err := lane.ReadScript(o.Root, n)
		if err != nil || len(info.Modules) == 0 {
			continue
		}
		mods, _ := module.Resolve(o.Root, o.Cfg.Modules, info.Modules)
		for _, m := range mods {
			if m.Builtin || m.Kind != module.Go || seen[m.Dir] {
				continue
			}
			seen[m.Dir] = true
			out = append(out, fd(finding.Error, "module-go", m.Dir, "install Go (https://go.dev/dl/) on this machine, or use a bash module",
				"lane %d uses ./%s, a Go module swim builds with Go, and there's no go on PATH: its steps will fail", n, m.Dir))
		}
	}
	return out
}
