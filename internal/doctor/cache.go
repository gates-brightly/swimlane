package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/cache"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/finding"
)

// repoCache is the repo's cache directory, if it has one yet (doctor never
// creates it).
func repoCache(o *Options) (*cache.Repo, string, bool) {
	base, err := o.Cfg.CacheBase()
	if err != nil {
		return nil, "", false
	}
	slug, err := o.Cfg.CacheSlugFor()
	if err != nil {
		return nil, "", false
	}
	r := &cache.Repo{Dir: filepath.Join(base, slug), Slug: slug}
	if _, err := os.Lstat(r.Dir); err != nil {
		return nil, base, false
	}
	return r, base, true
}

// checkCacheDir: a cache directory that's a link, isn't the user's, or
// that others can write. Lanes with # Cache: refuse to start on one.
func checkCacheDir(o *Options) []finding.Finding {
	r, base, ok := repoCache(o)
	if !ok {
		return nil
	}
	dirs := []string{base, r.Dir}
	for _, name := range cache.Names() {
		if _, err := os.Lstat(filepath.Join(r.Dir, name)); err == nil {
			dirs = append(dirs, filepath.Join(r.Dir, name))
		}
	}
	var out []finding.Finding
	for _, d := range dirs {
		if err := cache.CheckDir(d); err != nil {
			out = append(out, fd(finding.Error, "cache-dir", d, "chmod 700 it, or remove it (swim makes it again), or set cache_dir elsewhere", "%s; lanes with # Cache: won't start", err))
		}
	}
	return out
}

// checkCacheUnused: preset caches no lane has used for 30 days, and the
// repo's caches' size against cache_max.
func checkCacheUnused(o *Options) []finding.Finding {
	r, _, ok := repoCache(o)
	if !ok || cache.CheckDir(r.Dir) != nil {
		return nil
	}
	list, err := r.List()
	if err != nil {
		return nil
	}
	var stale []string
	var total, staleSize int64
	for _, c := range list {
		total += c.Size
		if t, err := time.Parse(time.RFC3339, c.LastUsed); (err != nil || time.Since(t) > 30*24*time.Hour) && !c.InUse {
			stale = append(stale, c.Name)
			staleSize += c.Size
		}
	}
	var out []finding.Finding
	if len(stale) > 0 {
		out = append(out, fd(finding.Info, "cache-unused", r.Dir, "swim cache prune --older 30d",
			"caches unused for 30 days: %s (%s)", strings.Join(stale, ", "), config.FormatSize(staleSize)))
	}
	if m := o.Cfg.CacheMaxBytes(); m > 0 && total > m {
		out = append(out, fd(finding.Info, "cache-unused", r.Dir, "swim cache prune (the next run trims it too)",
			"the repo's caches take %s, over cache_max (%s)", config.FormatSize(total), config.FormatSize(m)))
	}
	return out
}
