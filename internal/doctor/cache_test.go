package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/config"
)

func TestCacheChecks(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{Root: root, Settings: config.Settings{Lanes: 1, CacheDir: filepath.Join(t.TempDir(), "c")}}
	o := Options{Root: root, Cfg: cfg}
	o.defaults()
	if fs := append(checkCacheDir(&o), checkCacheUnused(&o)...); len(fs) != 0 {
		t.Fatalf("no cache yet: %+v", fs)
	}
	r, err := cfg.OpenCache()
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := r.Path("npm")
	os.WriteFile(filepath.Join(dir, "x"), []byte("x"), 0o600)
	// Never used (no index entry): unused.
	if fs := checkCacheUnused(&o); len(fs) != 1 || !strings.Contains(fs[0].Message, "npm") {
		t.Errorf("unused: %+v", fs)
	}
	r.Touch("npm", 1)
	if fs := checkCacheUnused(&o); len(fs) != 0 {
		t.Errorf("just used: %+v", fs)
	}
	os.Chmod(dir, 0o777)
	if fs := checkCacheDir(&o); len(fs) != 1 || fs[0].Code != "cache-dir" || !strings.Contains(fs[0].Message, "writable by others") {
		t.Errorf("mode 777: %+v", fs)
	}
}
