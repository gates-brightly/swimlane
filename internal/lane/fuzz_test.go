package lane

import (
	"strings"
	"testing"
)

// FuzzParseScript feeds the lane script header parser arbitrary text: it
// never panics, every lock it keeps is a valid name, dependencies are
// never empty, and a guard flag is listed once. Seeds: lane scripts in
// testdata/fuzz/FuzzParseScript and the ones below.
func FuzzParseScript(f *testing.F) {
	f.Add("#!/usr/bin/env bash\n# swim: syntax 2\n# Round: deploy\n# Job: 1b2c\n# After: 1, swim 2\n# Locks: db, cache/${STAGE}\n# Guards: ALLOW_X drop the table\n# Timeout: 30m\nlane_init 3\nrun \"a\" true\nsummary\n")
	f.Add("// swim: syntax 2\n// Round: js lane\n// Matrix: SVC=a,b\n// On-Failure: notify\nconsole.log(1)\n")
	f.Add("# Round:\n# After: -\n# Locks: none, bad name!, a//b\n# Guards: X\n# Guards: X again\n# Timeout: nope\n")
	f.Add("")
	f.Fuzz(func(t *testing.T, src string) {
		info := ParseScript(src)
		for _, l := range info.Locks {
			if !ValidLockName(strings.NewReplacer("${", "", "}", "", "$", "").Replace(l)) {
				t.Errorf("kept an invalid lock %q", l)
			}
		}
		for _, a := range info.After {
			if a == "" {
				t.Error("an empty After: entry")
			}
		}
		seen := map[string]bool{}
		for _, g := range info.Guards {
			if seen[g.Flag] {
				t.Errorf("guard %s listed twice", g.Flag)
			}
			seen[g.Flag] = true
		}
		if info.Timeout < 0 || info.StepTimeout < 0 {
			t.Errorf("a negative limit: %v %v", info.Timeout, info.StepTimeout)
		}
	})
}
