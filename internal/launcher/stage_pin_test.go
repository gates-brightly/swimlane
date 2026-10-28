package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/manifest"
	"github.com/gates-brightly/swimlane/internal/version"
)

// A queued lane whose manifest no longer matches is refused; when another
// swim queued it, the reason names both swims, the likely cause.
func TestPinnedManifestNamesTheSwim(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "lane.1.sh"), []byte("#!/usr/bin/env bash\n# swim: syntax 4\n# Round: r\n"), 0o755)
	cfg := &config.Config{Root: root, Lanes: 1}
	m, err := manifest.Build(root, cfg, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stage(root, "r-test"); err != nil { // as swim run does
		t.Fatal(err)
	}
	o := Options{Root: root, Cfg: cfg, manifests: map[int]*manifest.Manifest{1: m},
		PinManifest: map[int]string{1: strings.Repeat("ab", 32)}}

	why, refuse := pinned(o, 1)
	if refuse || !strings.Contains(why, "its manifest changed since it was queued") || strings.Contains(why, "queued by swim") {
		t.Fatalf("same swim: %q (refuse %v)", why, refuse)
	}
	o.QueuedBy = version.Long()
	if why, _ := pinned(o, 1); strings.Contains(why, "queued by swim") {
		t.Errorf("queued by this swim, yet blamed: %q", why)
	}
	o.QueuedBy = "0.20261014 (08c1d3e)"
	if why, _ := pinned(o, 1); !strings.Contains(why, "queued by swim 0.20261014 (08c1d3e), run by swim "+version.Long()) {
		t.Errorf("another swim: %q", why)
	}
}
