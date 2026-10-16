package lane

import (
	"slices"
	"strings"

	"github.com/gates-brightly/swimlane/internal/cache"
)

// setCache adds a # Cache: value's presets (where names the line, for
// problems).
func (i *Info) setCache(v, where string) {
	decls, problems := cache.ParseDecl(v)
	for _, p := range problems {
		i.Problems = append(i.Problems, where+" "+p)
	}
	for _, d := range decls {
		dup := false
		for _, have := range i.Cache {
			dup = dup || have.Preset == d.Preset
		}
		if !dup {
			i.Cache = append(i.Cache, d)
		}
	}
}

// setModules adds # Modules: names (or a sidecar's modules:), once each.
func (i *Info) setModules(names []string) {
	for _, m := range names {
		if m == "" || m == "-" || strings.EqualFold(m, "none") || slices.Contains(i.Modules, m) {
			continue
		}
		i.Modules = append(i.Modules, m)
	}
}
