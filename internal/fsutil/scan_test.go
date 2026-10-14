package fsutil

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestStateWritesAreDurable fails on a bare os.WriteFile or os.Rename in
// the packages that write swim's records: status, queue entries, the chain
// and approvals files go through WriteAtomic and Rename, which fsync.
func TestStateWritesAreDurable(t *testing.T) {
	bare := regexp.MustCompile(`\bos\.(WriteFile|Rename)\(`)
	for _, pkg := range []string{"status", "queue", "chain", "sigfile"} {
		files, err := filepath.Glob(filepath.Join("..", pkg, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no files in internal/%s: %v", pkg, err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for i, line := range strings.Split(string(data), "\n") {
				if bare.MatchString(line) {
					t.Errorf("%s:%d: %s (use fsutil.WriteAtomic or fsutil.Rename)", f, i+1, strings.TrimSpace(line))
				}
			}
		}
	}
}
