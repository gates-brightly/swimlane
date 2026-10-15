package lane

import (
	"strings"
	"testing"
)

// Finally: in any comment syntax, and finally jobs in a lane of jobs; an
// Always lane can't be one.
func TestParseFinally(t *testing.T) {
	for _, src := range []string{
		"#!/usr/bin/env bash\n# Round: tear down\n# Finally: 1 2, 3\n",
		"// Round: tear down\n// Finally: 1 2, 3\n",
	} {
		if got := strings.Join(ParseScript(src).Finally, " "); got != "1 2 3" {
			t.Errorf("Finally from %q: %q", src, got)
		}
	}
	info := ParseScript("# Round: setup\n# Always: yes\n# Finally: 1\n")
	info.checkAlways("Always:")
	if len(info.Finally) != 0 || !strings.Contains(strings.Join(info.Problems, " "), "`Finally:` on an Always lane") {
		t.Errorf("Always + Finally: %v %v", info.Finally, info.Problems)
	}
	g, ok := ParseGroup("lanes.ci.sh", `#!/usr/bin/env bash
# Round: stack
# swim job 3.1 -----
# Round: provision
true
# swim job 3.2
# Round: test
true
# swim job 3.9 finally -----
# Round: tear down
true
`, 3)
	if !ok || len(g.Subs) != 3 {
		t.Fatalf("group: %+v", g.Problems)
	}
	fin := map[string]bool{}
	for _, s := range g.Info.Subs {
		fin[s.Name] = s.Info.FinallyJob
	}
	if fin["1"] || fin["2"] || !fin["9"] {
		t.Errorf("finally jobs: %v", fin)
	}
}
