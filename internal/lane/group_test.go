package lane

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const groupSrc = `#!/usr/bin/env bash
# swim: syntax 2
# Shared by every lane:
export SHARED=1

# swim lane 1 -------
# Round:  fmt
# Always: yes
gate "fmt" true

# swim lane 3: lint
# Round:  lint
# After:  1
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 3
run "lint" true
summary
`

func TestParseGroup(t *testing.T) {
	gs, probs := ParseGroup("lanes.ci.sh", groupSrc)
	if len(probs) > 0 || len(gs) != 2 {
		t.Fatalf("lanes %d, problems %v", len(gs), probs)
	}
	one, three := gs[0], gs[1]
	if one.N != 1 || one.Line != 6 || three.N != 3 || three.Line != 11 {
		t.Errorf("lanes: %d@%d %d@%d", one.N, one.Line, three.N, three.Line)
	}
	info := ParseScript(one.Src)
	if info.Round != "fmt" || !info.Always || info.Syntax != 2 {
		t.Errorf("lane 1 header: %+v", info)
	}
	for _, want := range []string{"lane_init 1\n", "export SHARED=1\n", "gate \"fmt\" true\n", "\nsummary\n"} {
		if !strings.Contains(one.Src, want) {
			t.Errorf("lane 1 script lacks %q:\n%s", want, one.Src)
		}
	}
	// A section that loads the library itself doesn't get it twice.
	if strings.Count(three.Src, "lane_init") != 1 || strings.Count(three.Src, "summary") != 1 {
		t.Errorf("lane 3 script:\n%s", three.Src)
	}
	// Lines map back to the group file; added lines to the marker.
	lines := strings.Split(one.Src, "\n")
	for i, l := range lines {
		if l == `gate "fmt" true` && one.GroupLine(i+1) != 9 {
			t.Errorf("gate maps to %d, want 9", one.GroupLine(i+1))
		}
		if l == "lane_init 1" && one.GroupLine(i+1) != 6 {
			t.Errorf("lane_init maps to %d, want the marker", one.GroupLine(i+1))
		}
	}

	if _, probs := ParseGroup("lanes.x.sh", "# Round: lost\n# swim lane 2\n# Round: a\n# swim lane 2\n# Round: b\n"); len(probs) != 2 {
		t.Errorf("key before the first lane and a lane twice: %v", probs)
	}
	if _, probs := ParseGroup("lanes.x.sh", "#!/bin/bash\necho hi\n"); len(probs) != 1 {
		t.Errorf("no lanes: %v", probs)
	}
}

func TestGroupLanes(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "lanes.ci.sh"), []byte(groupSrc), 0o644)
	if got := Script(root, 3); got != GroupScript(root, 3) {
		t.Errorf("Script = %s", got)
	}
	if got := ScriptName(root, 3); got != "lanes.ci.sh[swim 3]" {
		t.Errorf("ScriptName = %s", got)
	}
	info, err := ReadScript(root, 3)
	if err != nil || info.Round != "lint" || len(info.After) != 1 {
		t.Errorf("ReadScript: %+v %v", info, err)
	}
	if got := ScriptsBeyond(root, 2); len(got) != 1 || got[0] != 3 {
		t.Errorf("ScriptsBeyond = %v", got)
	}
	if err := WriteScript(root, 1, "# Round: x\n", true); err == nil || !strings.Contains(err.Error(), "lanes.ci.sh") {
		t.Errorf("WriteScript into a grouped lane: %v", err)
	}
	os.WriteFile(filepath.Join(root, "lane.1.sh"), []byte("# Round: also\n"), 0o755)
	if _, err := ReadScript(root, 1); err == nil || !strings.Contains(err.Error(), "lanes.ci.sh:6") {
		t.Errorf("lane in a group and a lane.N.sh: %v", err)
	}
}
