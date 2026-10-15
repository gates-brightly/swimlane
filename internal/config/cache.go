package config

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gates-brightly/swimlane/internal/cache"
)

// DefaultCacheMax is a repo's cache cap when cache_max is unset.
const DefaultCacheMax = 10 << 30

// CacheBase is where tool caches live (cache_dir, else the user's cache
// directory; see cache.Base).
func (c *Config) CacheBase() (string, error) { return cache.Base(c.CacheDir) }

// CacheSlugFor is the repo's cache directory name (cache_slug, else its
// name and a hash of its remote).
func (c *Config) CacheSlugFor() (string, error) { return cache.Slug(c.Root, c.CacheSlug) }

// CacheMaxBytes is cache_max in bytes: the size a repo's caches are
// trimmed to, 0 for no cap.
func (c *Config) CacheMaxBytes() int64 {
	if c.CacheMax == "" {
		return DefaultCacheMax
	}
	n, err := ParseSize(c.CacheMax)
	if err != nil {
		return DefaultCacheMax
	}
	return n
}

// OpenCache opens the repo's caches, creating and checking their
// directories.
func (c *Config) OpenCache() (*cache.Repo, error) {
	base, err := c.CacheBase()
	if err != nil {
		return nil, err
	}
	slug, err := c.CacheSlugFor()
	if err != nil {
		return nil, err
	}
	return cache.Open(base, slug)
}

// ParseSize reads a size like 10GB, 500MB, 2g or 0 (bytes, decimal
// numbers allowed; units are powers of 1024).
func ParseSize(s string) (int64, error) {
	t := strings.ToUpper(strings.TrimSpace(s))
	units := []struct {
		suffix string
		mul    float64
	}{{"TB", 1 << 40}, {"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"T", 1 << 40}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}, {"B", 1}}
	mul := 1.0
	for _, u := range units {
		if strings.HasSuffix(t, u.suffix) {
			t, mul = strings.TrimSpace(strings.TrimSuffix(t, u.suffix)), u.mul
			break
		}
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("cache_max must be a size like 10GB or 500MB (0 for no cap), got %q", s)
	}
	return int64(f * mul), nil
}

// FormatSize writes n bytes for people: 1.5 GB, 300 MB.
func FormatSize(n int64) string {
	switch {
	case n >= 1<<30:
		return strconv.FormatFloat(float64(n)/(1<<30), 'f', 1, 64) + " GB"
	case n >= 1<<20:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MB"
	case n >= 1<<10:
		return strconv.FormatFloat(float64(n)/(1<<10), 'f', 1, 64) + " KB"
	}
	return strconv.FormatInt(n, 10) + " B"
}
