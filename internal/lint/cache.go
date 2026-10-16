package lint

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gates-brightly/swimlane/internal/cache"
	"github.com/gates-brightly/swimlane/internal/finding"
)

// isCacheProblem reports whether a header problem is about # Cache: (or
// a sidecar's cache:): checkCache reports those, as errors.
func isCacheProblem(p string) bool {
	f := strings.Fields(p)
	return len(f) > 0 && f[0] == "Cache:" || len(f) > 1 && f[1] == "cache:"
}

// checkCache: a # Cache: that names an unknown preset or no lockfile, a
// lockfile that isn't on disk, or a pip requirements file without
// hashes. Without its lockfile a tool has nothing to check the cache
// against, and swim run refuses the lane.
func checkCache(c *Context, s *Script) []finding.Finding {
	var out []finding.Finding
	line := s.headerLine("Cache")
	for _, p := range s.Info.Problems {
		if isCacheProblem(p) {
			out = append(out, f(finding.Error, "cache", line, "name a preset ("+presetList()+") and its lockfile: # Cache: npm @web/package-lock.json", "%s", p))
		}
	}
	if c.Root == "" {
		return out
	}
	for _, d := range s.Info.Cache {
		for _, lf := range d.Lockfiles {
			data, err := os.ReadFile(filepath.Join(c.Root, lf))
			if err != nil {
				out = append(out, f(finding.Error, "cache", line, "commit the lockfile, or fix the path (relative to the repo root)",
					"Cache: %s's lockfile %s doesn't exist; swim run refuses the lane", d.Label(), lf))
				continue
			}
			if d.Preset == "pip" && !strings.Contains(string(data), "--hash=") {
				out = append(out, f(finding.Error, "cache", line, "generate it with hashes (pip-compile --generate-hashes, or uv pip compile --generate-hashes)",
					"Cache: %s has no --hash entries, so pip has nothing to check its cache against", lf))
			}
		}
	}
	return out
}

var (
	npmInstallRE = regexp.MustCompile(`(?:^|[\s;&|('"])npm\s+(?:install|i|add)(?:\s|$|['"])`)
	pipInstallRE = regexp.MustCompile(`(?:^|[\s;&|('"/])pip3?\s+install\b|python3?\s+-m\s+pip\s+install\b`)
)

// checkCacheBypass: commands that take from a cache without checking it
// against the lockfile, in a lane that declares that cache.
func checkCacheBypass(c *Context, s *Script) []finding.Finding {
	has := map[string]bool{}
	for _, d := range s.Info.Cache {
		has[d.Preset] = true
	}
	if len(has) == 0 {
		return nil
	}
	var out []finding.Finding
	for i, line := range s.v.code {
		if s.v.doc[i] {
			continue
		}
		switch {
		case has["npm"] && npmInstallRE.MatchString(line):
			out = append(out, f(finding.Warn, "cache-bypass", i, "use npm ci, which installs exactly the lockfile",
				"npm install can resolve versions the lockfile doesn't pin, so the npm cache isn't checked against it"))
		case has["pip"] && pipInstallRE.MatchString(line) && !strings.Contains(line, "--require-hashes") && !strings.Contains(line, "-r "):
			out = append(out, f(finding.Warn, "cache-bypass", i, "install from the hashed lockfile: pip install --require-hashes -r requirements.txt",
				"pip install without --require-hashes takes what it finds in its cache unchecked"))
		case has["terraform"] && strings.Contains(line, "TF_PLUGIN_CACHE_MAY_BREAK_DEPENDENCY_LOCK_FILE"):
			out = append(out, f(finding.Warn, "cache-bypass", i, "remove it: swim's terraform cache relies on the lock file's checksums",
				"TF_PLUGIN_CACHE_MAY_BREAK_DEPENDENCY_LOCK_FILE lets terraform take providers from the cache that the lock file doesn't list"))
		}
	}
	return out
}

func presetList() string { return strings.Join(cache.Names(), ", ") }
