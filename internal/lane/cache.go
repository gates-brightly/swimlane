package lane

import "github.com/gates-brightly/swimlane/internal/cache"

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
