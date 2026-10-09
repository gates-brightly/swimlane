package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestScenarios runs every whole-workflow scenario in e2e/scenarios through
// e2e/run.sh, against the binary TestMain built. They take ~35s; skip them
// with `go test -short` (or `make test-unit`, which skips this package).
func TestScenarios(t *testing.T) {
	if testing.Short() {
		t.Skip("scenarios skipped with -short")
	}
	_, file, _, _ := runtime.Caller(0)
	e2e := filepath.Join(filepath.Dir(file), "..", "..", "e2e")
	dirs, err := filepath.Glob(filepath.Join(e2e, "scenarios", "*", "scenario.sh"))
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no scenarios under %s: %v", e2e, err)
	}
	for _, d := range dirs {
		name := filepath.Base(filepath.Dir(d))
		t.Run(name, func(t *testing.T) {
			c := exec.Command("bash", filepath.Join(e2e, "run.sh"), name)
			c.Env = append(os.Environ(), "SWIM_E2E_BIN="+bin)
			out, err := c.CombinedOutput()
			if err != nil {
				t.Fatalf("scenario %s failed: %v\n%s", name, err, out)
			}
			if !strings.Contains(string(out), name) || !strings.Contains(string(out), "PASS") {
				t.Fatalf("unexpected runner output:\n%s", out)
			}
			if testing.Verbose() {
				t.Logf("\n%s", out)
			}
		})
	}
}
