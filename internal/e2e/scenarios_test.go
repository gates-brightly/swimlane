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
// e2e/run.sh, against the binary TestMain built. They take ~20s; skip them
// with `go test -short` (or `just test`, which skips this package).
func TestScenarios(t *testing.T) {
	if testing.Short() {
		t.Skip("scenarios skipped with -short")
	}
	parallel(t) // alongside the Go tests, not after them
	_, file, _, _ := runtime.Caller(0)
	e2e := filepath.Join(filepath.Dir(file), "..", "..", "e2e")
	dirs, err := filepath.Glob(filepath.Join(e2e, "scenarios", "*", "scenario.sh"))
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no scenarios under %s: %v", e2e, err)
	}
	for _, d := range dirs {
		name := filepath.Base(filepath.Dir(d))
		t.Run(name, func(t *testing.T) {
			if linuxOnlyScenarios[name] && runtime.GOOS == "darwin" && os.Getenv("SWIM_E2E_ALL_SCENARIOS") == "" {
				t.Skip("Linux only (T32): the scheduler under load is the same on macOS; plain dag99, -interrupt and -force cover bash 3.2 and signals there. SWIM_E2E_ALL_SCENARIOS=1 runs it")
			}
			t.Parallel() // each scenario has its own scratch repo and config
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

// linuxOnlyScenarios are the dag99 variants that test the scheduler (locks,
// retries, a parallel cap) on 99 lanes: 100-140s each on a macOS runner,
// the long pole of CI there (T32). What macOS adds, bash 3.2 and BSD
// userland, plain dag99, dag99-interrupt and dag99-force still cover.
var linuxOnlyScenarios = map[string]bool{"dag99-locks": true, "dag99-retry": true, "dag99-parallel": true}
