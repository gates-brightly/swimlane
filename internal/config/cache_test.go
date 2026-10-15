package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"10GB": 10 << 30, "500mb": 500 << 20, "1.5G": 3 << 29, "0": 0, "2048": 2048, " 4 KB ": 4096} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "lots", "-1GB", "10XB"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("ParseSize(%q) accepted", bad)
		}
	}
}

func TestCacheSettings(t *testing.T) {
	writeConfig(t, "defaults:\n  cache_dir: /tmp/swim_cache\n  cache_max: 2GB\n")
	root := t.TempDir()
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if base, _ := c.CacheBase(); !strings.HasPrefix(base, "/tmp/swim_cache-") {
		t.Errorf("CacheBase = %q, want a per-user suffix", base)
	}
	if c.CacheMaxBytes() != 2<<30 {
		t.Errorf("CacheMaxBytes = %d", c.CacheMaxBytes())
	}
	if got, _ := c.Get("cache_max"); got != "2.0 GB" {
		t.Errorf("Get(cache_max) = %q", got)
	}

	// Where caches live, and which repos share one, isn't the repo's call.
	for _, key := range []string{"cache_dir: /tmp/shared", "cache_slug: other-repo"} {
		os.WriteFile(filepath.Join(root, "swim.yml"), []byte(key+"\n"), 0o644)
		if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "belong in") {
			t.Errorf("swim.yml %s: %v", key, err)
		}
	}
	// cache_max may be shared.
	os.WriteFile(filepath.Join(root, "swim.yml"), []byte("cache_max: 1GB\n"), 0o644)
	if c, err := Load(root); err != nil || c.CacheMaxBytes() != 1<<30 {
		t.Errorf("swim.yml cache_max: %v", err)
	}
	os.WriteFile(filepath.Join(root, "swim.yml"), []byte("cache_max: huge\n"), 0o644)
	if _, err := Load(root); err == nil {
		t.Error("a bad cache_max was accepted")
	}
}
