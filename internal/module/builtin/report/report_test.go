package report

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/module/gomod/gomodtest"
)

func TestReport(t *testing.T) {
	out := t.TempDir()
	os.WriteFile(filepath.Join(out, "report.md"), []byte("# Report\nAll steps passed."), 0o644)
	os.WriteFile(filepath.Join(out, "big.txt"), []byte(strings.Repeat("x", 3000)), 0o644)
	env := map[string]string{"SWIM_OUT": out}

	f := gomodtest.NewFake(t.TempDir(), env, false, "report.md", "big.txt", "--max", "1KiB")
	if err := Run(&f.Ctx); err != nil {
		t.Fatal(err)
	}
	got := f.StdoutB.String()
	for _, want := range []string{"== report.md  26 B  sha256 ", "# Report\nAll steps passed.\n\n== big.txt  2.9 KiB", "showed the first 1.0 KiB of 2.9 KiB"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	// The hash is of the whole file, not what was shown.
	whole := sha256.Sum256([]byte(strings.Repeat("x", 3000)))
	if !strings.Contains(got, "== big.txt  2.9 KiB  sha256 "+hex.EncodeToString(whole[:])[:12]) {
		t.Errorf("big.txt's hash isn't the whole file's:\n%s", got)
	}

	f = gomodtest.NewFake(t.TempDir(), env, false, "nope.md")
	if err := Run(&f.Ctx); err == nil || !strings.Contains(err.Error(), "not there: nope.md") {
		t.Errorf("missing: %v", err)
	}
	f = gomodtest.NewFake(t.TempDir(), env, false, "nope.md", "--optional")
	if err := Run(&f.Ctx); err != nil || !strings.Contains(f.StdoutB.String(), "== nope.md: not there (no such file or directory)") {
		t.Errorf("optional: %v %s", err, f.StdoutB.String())
	}
	for _, bad := range [][]string{{}, {"../x"}, {"r.md", "--max", "lots"}} {
		f := gomodtest.NewFake(t.TempDir(), env, false, bad...)
		if err := Run(&f.Ctx); err == nil {
			t.Errorf("%v passed", bad)
		}
	}
}
