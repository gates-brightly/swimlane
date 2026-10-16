package module

import (
	"fmt"
	"sort"

	"github.com/gates-brightly/swimlane/internal/version"
)

// LockProblem says why a lane can't use m in a repo whose .swim.lock is
// lock ("" if it can): the lock pins another version, or other files, of
// the built-in m. A built-in the lock doesn't name is fine (swim modules
// lock pins it); a repo module is pinned by the manifest, not the lock.
func LockProblem(lock version.Lock, m *Module) string {
	if !m.Builtin {
		return ""
	}
	return PinProblem(lock, m.Name, m.Version, m.SHA256)
}

// PinProblem is LockProblem for a built-in module this swim has as
// version and sha (a manifest's pin of it).
func PinProblem(lock version.Lock, name, ver, sha string) string {
	p, ok := lock.Builtin(name)
	if !ok || p.SHA256 == sha {
		return ""
	}
	pins := fmt.Sprintf("%s pins built-in %s %s, and this swim's is %s", version.LockName, name, p.Version, ver)
	moveTo := fmt.Sprintf("swim modules lock moves the repo to %s %s (re-approve the lanes that use it)", name, ver)
	switch CompareVersions(p.Version, ver) {
	case 1:
		return pins + ": update swim (" + version.InstallCmd + "), or " + moveTo
	case -1:
		return pins + ": " + moveTo + ", or use the swim that wrote the lock (" + lock.Version + ")"
	}
	return fmt.Sprintf("%s pins built-in %s %s (%s), and this swim's %s has other files (%s): use the swim that wrote the lock (%s), or %s",
		version.LockName, name, p.Version, short(p.SHA256), ver, short(sha), lock.Version, moveTo)
}

// LockProblems is LockProblem for each of mods that has one.
func LockProblems(lock version.Lock, mods []*Module) []string {
	var out []string
	for _, m := range mods {
		if why := LockProblem(lock, m); why != "" {
			out = append(out, why)
		}
	}
	return out
}

// LockPins are the lock entries for the built-ins among mods, as this swim
// has them: what swim modules lock writes.
func LockPins(mods []*Module) map[string]version.LockedModule {
	out := map[string]version.LockedModule{}
	for _, m := range mods {
		if m.Builtin {
			out[m.Name] = version.LockedModule{Version: m.Version, SHA256: m.SHA256}
		}
	}
	return out
}

// PinChanges says how pins differ from what the lock has: "tf 1.1.0 ->
// 1.2.0", "notify (new) 1.0.0", "wait 1.0.0 (no lane uses it)".
func PinChanges(lock version.Lock, pins map[string]version.LockedModule) []string {
	var out []string
	var old map[string]version.LockedModule
	if lock.Modules != nil {
		old = lock.Modules.Builtin
	}
	for name, p := range pins {
		o, ok := old[name]
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("%s %s (new)", name, p.Version))
		case o.SHA256 != p.SHA256:
			out = append(out, fmt.Sprintf("%s %s -> %s", name, o.Version, p.Version))
		}
	}
	for name, o := range old {
		if _, ok := pins[name]; !ok {
			out = append(out, fmt.Sprintf("%s %s (removed: no lane uses it)", name, o.Version))
		}
	}
	sort.Strings(out)
	return out
}

func short(sha string) string { return sha[:min(12, len(sha))] }
