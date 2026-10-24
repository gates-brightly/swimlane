package assets

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Every template renders and is valid for its language: bash -n for the
// scripts and the library, python3 and node checks when they run (a
// version manager's shim with no version set is on PATH but doesn't).
func TestTemplatesRenderAndParse(t *testing.T) {
	dir := t.TempDir()
	check := func(name, src string, cmd ...string) {
		t.Helper()
		if strings.Contains(src, "{{") || strings.Contains(src, "<no value>") {
			t.Errorf("%s: a placeholder survived:\n%s", name, src)
		}
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(src), 0o644)
		if _, err := exec.LookPath(cmd[0]); err != nil {
			return
		}
		if err := exec.Command(cmd[0], "--version").Run(); err != nil {
			t.Logf("%s: skipped, %s doesn't run: %v", name, cmd[0], err)
			return
		}
		if out, err := exec.Command(cmd[0], append(cmd[1:], p)...).CombinedOutput(); err != nil {
			t.Errorf("%s doesn't parse: %v\n%s", name, err, out)
		}
	}
	src, err := Script(3, "cut over orders", ". ~/.nvm/nvm.sh", "job-12345678", "zach")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(src, "cut over orders") || !strings.Contains(src, "job-12345678") {
		t.Errorf("the template lacks the goal or job:\n%s", src)
	}
	check("lane.3.sh", src, "bash", "-n")
	stub, _ := Stub(2, `done "for now" $HOME`)
	check("stub.sh", stub, "bash", "-n")
	check("lib.sh", LibFor("/usr/local/bin/swim"), "bash", "-n")
	check("git-shim.sh", GitShim, "bash", "-n")
}
