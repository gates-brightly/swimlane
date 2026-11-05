package helpers

import (
	"os/exec"
	"strings"
	"testing"
)

func TestModules(t *testing.T) {
	if got := strings.Join(Modules(), " "); got != "aws docker gh git os tool" {
		t.Errorf("modules: %s", got)
	}
	for _, m := range Modules() {
		if Summary(m) == "" {
			t.Errorf("%s: no summary (first line: # swim helpers, module %s: ...)", m, m)
		}
		src, _ := Source(m)
		// Every function a module defines is <module>_<name> (or _<module>_ for its own).
		for _, line := range strings.Split(src, "\n") {
			name, ok := strings.CutSuffix(line, "() {")
			if ok && !strings.HasPrefix(name, m+"_") && !strings.HasPrefix(name, "_"+m+"_") {
				t.Errorf("%s defines %s, outside its namespace", m, name)
			}
		}
		if out, err := exec.Command("bash", "-n", "-c", src).CombinedOutput(); err != nil {
			t.Errorf("%s: bash -n: %v %s", m, err, out)
		}
	}
}

func TestCheck(t *testing.T) {
	cur, _ := Stamped("gh", "0.20261106.1")
	if c := Check(cur); c.State != Current || c.Module != "gh" || c.From != "0.20261106.1" {
		t.Errorf("as written: %+v", c)
	}
	if c := Check(cur + "# a local edit\n"); c.State != Edited {
		t.Errorf("edited: %+v", c)
	}
	// An older swim's gh: stamped with its own hash, not this swim's text.
	src, _ := Source("gh")
	old := strings.Replace(src, "gh_pr_fetch", "gh_pr_fetch_old", 1)
	if c := Check("# swim helpers: gh, from swim 0.20260101.1, sha256 " + hash(old) + "\n" + old); c.State != Differs {
		t.Errorf("another swim's: %+v", c)
	}
	if c := Check("# swim helpers: nope, from swim 1, sha256 " + hash("x\n") + "\nx\n"); c.State != Unknown {
		t.Errorf("unknown module: %+v", c)
	}
	if c := Check("#!/bin/bash\necho hi\n"); c.State != NotHelper {
		t.Errorf("not a copy: %+v", c)
	}
}

// Every public function is documented: swim helpers MODULE lists it with
// its usage, from the comment above it.
func TestFuncsDocumentEveryFunction(t *testing.T) {
	for _, m := range Modules() {
		documented := map[string]bool{}
		for _, f := range Funcs(m) {
			if f.Doc == "" || !strings.HasPrefix(f.Usage, f.Names[0]) {
				t.Errorf("%s: entry %+v", m, f)
			}
			for _, n := range f.Names {
				documented[n] = true
			}
		}
		for _, n := range public(m) {
			if !documented[n] {
				t.Errorf("%s: %s has no `# %s ARGS: what it does` comment above it", m, n, n)
			}
		}
	}
	fs := Funcs("os")
	if len(fs) != 2 || strings.Join(fs[0].Names, " ") != "os_mac os_linux" || fs[0].Usage != "os_mac / os_linux" {
		t.Errorf("os: %+v", fs)
	}
}

// public are the functions module m defines outside its private _m_ ones.
func public(m string) []string {
	src, _ := Source(m)
	var out []string
	for _, line := range strings.Split(src, "\n") {
		if name, ok := strings.CutSuffix(line, "() {"); ok && !strings.HasPrefix(name, "_") {
			out = append(out, name)
		}
	}
	return out
}
