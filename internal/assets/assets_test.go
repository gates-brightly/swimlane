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
	for _, lang := range Langs {
		src, err := ScriptIn(lang, 3, "cut over orders", ". ~/.nvm/nvm.sh", "job-12345678", "zach")
		if err != nil {
			t.Fatalf("%s: %v", lang, err)
		}
		if !strings.Contains(src, "cut over orders") || !strings.Contains(src, "job-12345678") {
			t.Errorf("%s template lacks the goal or job:\n%s", lang, src)
		}
		switch lang {
		case "sh":
			check("lane.3.sh", src, "bash", "-n")
		case "py":
			check("lane.3.py", src, "python3", "-m", "py_compile")
		case "js":
			check("lane.3.js", src, "node", "--check")
		case "ts":
			if strings.HasPrefix(src, "#!") {
				t.Error("the ts template has a #! line")
			}
		}
	}
	if _, err := ScriptIn("cobol", 1, "x", "", "j", "o"); err == nil {
		t.Error("an unknown language rendered")
	}
	stub, _ := Stub(2, `done "for now" $HOME`)
	check("stub.sh", stub, "bash", "-n")
	check("lib.sh", LibFor("/usr/local/bin/swim"), "bash", "-n")
	check("git-shim.sh", GitShim, "bash", "-n")
}
