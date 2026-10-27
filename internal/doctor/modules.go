package doctor

import (
	"path/filepath"
	"sort"

	"github.com/gates-brightly/swimlane/internal/finding"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/module"
	"github.com/gates-brightly/swimlane/internal/version"
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

// checkModuleVersion: a lane uses a built-in module the repo's .swim.lock
// pins at another version (or with other files) than this swim has: swim
// run refuses it. A built-in the lock doesn't pin yet is a warning: swim
// modules lock pins it, so every machine runs the same one.
func checkModuleVersion(o *Options) []finding.Finding {
	lock, ok, err := version.ReadLock(o.Root)
	if err != nil || !ok {
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
			if !m.Builtin || seen[m.Name] {
				continue
			}
			seen[m.Name] = true
			if why := module.LockProblem(lock, m); why != "" {
				out = append(out, fd(finding.Error, "module-version", version.LockName, "see the message: update swim, or swim modules lock",
					"lane %d: %s", n, why))
			} else if _, pinned := lock.Builtin(m.Name); !pinned {
				out = append(out, fd(finding.Warn, "module-version", version.LockName, "swim modules lock (and commit .swim.lock)",
					"lane %d uses built-in %s %s, which .swim.lock doesn't pin: another machine's swim may run another version", n, m.Name, m.Version))
			}
		}
	}
	return out
}

// checkModuleFetched: each fetched module .swim.lock pins is in the store
// as pinned (a lane using it doesn't start otherwise), and some lane still
// names it.
func checkModuleFetched(o *Options) []finding.Finding {
	lock, ok, err := version.ReadLock(o.Root)
	if err != nil || !ok || lock.Modules == nil || len(lock.Modules.Fetched) == 0 {
		return nil
	}
	named := map[string]bool{}
	for _, n := range o.Cfg.Active {
		info, err := lane.ReadScript(o.Root, n)
		if err != nil {
			continue
		}
		for _, text := range info.Modules {
			if r, err := module.ParseRef(text); err == nil && r.Address != "" {
				named[r.Address] = true
			}
		}
	}
	var out []finding.Finding
	addrs := make([]string, 0, len(lock.Modules.Fetched))
	for a := range lock.Modules.Fetched {
		addrs = append(addrs, a)
	}
	sort.Strings(addrs)
	for _, a := range addrs {
		pin := lock.Modules.Fetched[a]
		files, err := module.ReadDir(filepath.Join(o.Root, filepath.FromSlash(module.StoreRel(pin.SHA256))))
		switch {
		case err != nil || len(files) == 0:
			out = append(out, fd(finding.Error, "module-fetched", version.LockName, "swim modules get",
				"%s %s isn't in %s: lanes that use it won't start", a, pin.Version, module.StoreRel(pin.SHA256)))
		case module.TreeHash(files) != pin.SHA256:
			out = append(out, fd(finding.Error, "module-fetched", version.LockName, "swim modules get (fetches it again)",
				"%s %s in %s changed since it was fetched: lanes that use it won't start", a, pin.Version, module.StoreRel(pin.SHA256)))
		}
		if !named[a] {
			out = append(out, fd(finding.Warn, "module-fetched", version.LockName, "swim modules get (drops pins no lane uses)",
				"%s %s is pinned, and no lane names it", a, pin.Version))
		}
	}
	return out
}
