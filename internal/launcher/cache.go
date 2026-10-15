package launcher

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gates-brightly/swimlane/internal/cache"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
)

// Environment a lane with caches gets besides each preset's variables:
// the repo's cache directory and its declared presets, for swim step (an
// exclusive preset's write lock) and swim cache.
const (
	CacheRepoEnv    = "SWIM_CACHE_REPO"
	CachePresetsEnv = "SWIM_CACHE_PRESETS"
)

// holdCaches opens lane n's caches (# Cache:) and holds each for as long
// as the lane runs: the files go to the lane's process with its lock
// files, so swim cache rm and eviction leave a cache alone while a lane
// uses it. A lane doesn't start when a lockfile it names is missing (the
// tool would have nothing to check the cache against) or its cache
// directory fails its checks.
func holdCaches(o Options, n int, info lane.Info) (lane.FileLocks, error) {
	if len(info.Cache) == 0 {
		return nil, nil
	}
	if err := checkLockfiles(o.Root, n, info); err != nil {
		return nil, err
	}
	r, err := o.Cfg.OpenCache()
	if err != nil {
		return nil, err
	}
	var held lane.FileLocks
	for _, d := range info.Cache {
		if _, err := r.Path(d.Preset); err != nil {
			held.Release()
			return nil, err
		}
		f, err := r.Use(d.Preset)
		if err != nil {
			held.Release()
			return nil, fmt.Errorf("cache %s: %w", d.Preset, err)
		}
		held = append(held, f)
		r.Touch(d.Preset, n)
	}
	// Over cache_max: trim the caches no running lane holds, least
	// recently used first, without holding the lane up.
	go r.Evict(o.Cfg.CacheMaxBytes())
	return held, nil
}

// cacheEnv is lane n's caches' environment (nil without # Cache:).
func cacheEnv(o Options, n int) []string {
	info, _ := lane.ReadScript(o.Root, n)
	env, _ := CacheEnvFor(o.Cfg, o.Root, n, info)
	return env // an error refused the lane already (holdCaches)
}

// CacheEnvFor is the environment of a lane with info's caches: each
// preset's variables, and CacheRepoEnv and CachePresetsEnv. It refuses
// what holdCaches refuses, so a lane run directly (lane_init, swim
// _cache-env) fails the same way.
func CacheEnvFor(cfg *config.Config, root string, n int, info lane.Info) ([]string, error) {
	if len(info.Cache) == 0 {
		return nil, nil
	}
	if err := checkLockfiles(root, n, info); err != nil {
		return nil, err
	}
	r, err := cfg.OpenCache()
	if err != nil {
		return nil, err
	}
	env, err := r.Env(info.Cache, os.Environ())
	if err != nil {
		return nil, err
	}
	names := make([]string, len(info.Cache))
	for i, d := range info.Cache {
		names[i] = d.Preset
	}
	return append(env, CacheRepoEnv+"="+r.Dir, CachePresetsEnv+"="+strings.Join(names, " ")), nil
}

// checkLockfiles: every preset names a lockfile, and every one exists.
func checkLockfiles(root string, n int, info lane.Info) error {
	for _, d := range info.Cache {
		if len(d.Lockfiles) == 0 {
			return fmt.Errorf("Cache: %s names no lockfile (Cache: %s @path/to/lockfile); see swim lint %d", d.Preset, d.Preset, n)
		}
		for _, f := range d.Lockfiles {
			if fi, err := os.Stat(filepath.Join(root, f)); err != nil || !fi.Mode().IsRegular() {
				return fmt.Errorf("Cache: %s's lockfile %s is missing; the tool needs it to check what it takes from the cache", d.Preset, f)
			}
		}
	}
	return nil
}

// PlanCache is one of a lane's caches in swim plan --yaml.
type PlanCache struct {
	Preset    string   `yaml:"preset"`
	Lockfiles []string `yaml:"lockfiles,flow"`
}

// cacheText is a lane's caches for swim plan: "terraform (infra/.terraform.lock.hcl), npm (web/package-lock.json)".
func cacheText(decls []cache.Decl) string {
	parts := make([]string, len(decls))
	for i, d := range decls {
		parts[i] = d.Preset + " (" + strings.Join(d.Lockfiles, ", ") + ")"
	}
	return strings.Join(parts, ", ")
}
