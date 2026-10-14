package lane

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const groupSrc = `#!/usr/bin/env bash
# swim: syntax 2
# Round: CI
# After: 4
export SHARED=1

# swim job 1.1 -------
# Round:  fmt
# Always: yes
gate "fmt" true

# swim job 1.3: lint
# Round:  lint
# Job:    job-lint
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 1
run "lint" true
summary
`

func TestParseGroup(t *testing.T) {
	g, ok := ParseGroup("lanes.ci.sh", groupSrc, 0)
	if !ok || len(g.Problems) > 0 || g.N != 1 || len(g.Subs) != 2 {
		t.Fatalf("lane %d, jobs %d, problems %v", g.N, len(g.Subs), g.Problems)
	}
	one, three := g.Subs[0], g.Subs[1]
	if one.Name() != "1" || one.Line != 7 || three.Name() != "3" || three.Line != 12 {
		t.Errorf("jobs: %s@%d %s@%d", one.Name(), one.Line, three.Name(), three.Line)
	}
	// The lane: its header, Always because a job is, jobs as cells in order.
	info := g.Info
	if info.Round != "CI" || len(info.After) != 1 || !info.Always || !info.IsMatrix() || info.MatrixFail != "stop" {
		t.Errorf("lane: %+v", info)
	}
	if got := CellNamesOf(info.Cells()); got != "1,3" {
		t.Errorf("cells %s", got)
	}
	if a := info.AlwaysCells(); len(a) != 1 || a[0] != "1" {
		t.Errorf("always cells %v", a)
	}
	c, _ := info.Cell("3")
	if sub := info.ForCell(c); sub.Round != "lint" || sub.Job != "job-lint" || sub.Always || len(sub.After) > 0 {
		t.Errorf("job 1.3: %+v", sub)
	}
	for _, want := range []string{"lane_init 1\n", "export SHARED=1\n", "gate \"fmt\" true\n", "\nsummary\n"} {
		if !strings.Contains(one.Src, want) {
			t.Errorf("job 1.1 script lacks %q:\n%s", want, one.Src)
		}
	}
	// A job that loads the library itself doesn't get it twice.
	if strings.Count(three.Src, "lane_init") != 1 || strings.Count(three.Src, "summary") != 1 {
		t.Errorf("job 1.3 script:\n%s", three.Src)
	}
	for i, l := range strings.Split(one.Src, "\n") {
		if l == `gate "fmt" true` && one.GroupLine(i+1) != 10 {
			t.Errorf("gate maps to %d, want 10", one.GroupLine(i+1))
		}
		if l == "lane_init 1" && one.GroupLine(i+1) != 7 {
			t.Errorf("lane_init maps to %d, want the marker", one.GroupLine(i+1))
		}
	}

	for src, want := range map[string]string{
		"# Always: yes\n# swim job 2.1\n# Round: a\nrun x true\n":                          "belongs on a job",
		"# swim job 2.1\n# Round: a\n# After: 3\nrun x true\n":                             "belongs on the lane",
		"# swim job 2.1\n# Round: a\nrun x true\n# swim job 2.1\n# Round: b\nrun x true\n": "already defined",
		"# swim job 2.1\n# Round: a\nrun x true\n# swim job 3.2\n# Round: b\nrun x true\n": "its jobs are 2.K",
		"#!/bin/bash\necho hi\n": "no jobs",
		"# swim job 2.1\n# Round: a\n# swim job 2.2\n# Round: b\nrun x true\n": "no steps and no jobs under it",
	} {
		g, _ := ParseGroup("lanes.x.sh", src, 0)
		if len(g.Problems) != 1 || !strings.Contains(g.Problems[0].Message, want) {
			t.Errorf("%q: problems %v, want %q", src, g.Problems, want)
		}
	}
}

func CellNamesOf(cs []Cell) string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return strings.Join(out, ",")
}

func TestGroupLane(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "lanes.ci.sh"), []byte(groupSrc), 0o644)
	if got := ScriptName(root, 1); got != "lanes.ci.sh" {
		t.Errorf("ScriptName = %s", got)
	}
	info, err := ReadScript(root, 1)
	if err != nil || info.Round != "CI" || !info.IsGroup() {
		t.Errorf("ReadScript: %+v %v", info, err)
	}
	// In a sub-job's process, the lane is that job, running its own script.
	t.Setenv(CellEnv, "3")
	if got := Script(root, 1); got != SubScript(root, 1, "3") {
		t.Errorf("Script in job 1.3 = %s", got)
	}
	if info, _ := ReadScript(root, 1); info.Round != "lint" {
		t.Errorf("ReadScript in job 1.3: %+v", info)
	}
	// Its hash is the group file's, from anywhere.
	data, _ := ScriptBytes(root, 1)
	if !strings.HasPrefix(string(data), groupSrc) {
		t.Errorf("ScriptBytes in a job:\n%s", data)
	}
	t.Setenv(CellEnv, "")

	if ref, cell, ok := ParseCellRef("1.3"); !ok || ref != "1" || cell != "3" {
		t.Errorf("ParseCellRef 1.3: %s %s %v", ref, cell, ok)
	}
	if err := WriteScript(root, 1, "# Round: x\n", true); err == nil || !strings.Contains(err.Error(), "lanes.ci.sh") {
		t.Errorf("WriteScript into a group lane: %v", err)
	}
	os.WriteFile(filepath.Join(root, "lane.1.sh"), []byte("# Round: also\n"), 0o755)
	if _, err := ReadScript(root, 1); err == nil || !strings.Contains(err.Error(), "lanes.ci.sh") {
		t.Errorf("lane in a group file and a lane.N.sh: %v", err)
	}
}

const nestedSrc = `#!/usr/bin/env bash
# swim: syntax 2
# Round: nested

# swim job 3.1
# Round: setup
# Always: yes

# swim job 3.1.1
# Round: a
run "a" true

# swim job 3.1.2
# Round: b
run "b" true

# swim job 3.5.9.9
# Round: deep
run "deep" true

# swim job 3.5
# Round: five
run "five" true
`

func TestNestedJobs(t *testing.T) {
	g, ok := ParseGroup("lane.3.sh", nestedSrc, 3)
	if !ok || len(g.Problems) > 0 {
		t.Fatalf("problems %v", g.Problems)
	}
	// Runnable jobs, in file order; 3.1 only groups.
	if got := CellNamesOf(g.Info.Cells()); got != "1.1,1.2,5.9.9,5" {
		t.Errorf("cells %s", got)
	}
	// Always: passed down from 3.1.
	if got := strings.Join(g.Info.AlwaysCells(), ","); got != "1.1,1.2" {
		t.Errorf("always %s", got)
	}
	// A job names itself and every job under it.
	for ref, want := range map[string]string{"1": "1.1,1.2", "5": "5.9.9,5", "5.9": "5.9.9", "1.2": "1.2", "51": "", "2": ""} {
		if got := CellNamesOf(g.Info.CellsUnder(ref)); got != want {
			t.Errorf("CellsUnder(%s) = %q, want %q", ref, got, want)
		}
	}
	for ref, want := range map[string][2]string{"3.5.9.9": {"3", "5.9.9"}, "1.1.3": {"1", "1.3"}} {
		if r, c, ok := ParseCellRef(ref); !ok || r != want[0] || c != want[1] {
			t.Errorf("ParseCellRef(%s) = %s %s %v", ref, r, c, ok)
		}
	}
	if !strings.Contains(g.Subs[2].Src, "# Generated by swim from lane.3.sh (swim job 3.5.9.9)") {
		t.Errorf("deep job script:\n%s", g.Subs[2].Src)
	}

	// A lane.N.sh with jobs is a lane of jobs.
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "lane.3.sh"), []byte(nestedSrc), 0o755)
	info, err := ReadScript(root, 3)
	if err != nil || !info.IsGroup() || info.Round != "nested" || !info.Always {
		t.Fatalf("ReadScript: %+v %v", info, err)
	}
	t.Setenv(CellEnv, "5.9.9")
	if got := Script(root, 3); got != SubScript(root, 3, "5.9.9") {
		t.Errorf("Script in job 3.5.9.9 = %s", got)
	}
	if got := filepath.Base(Log(root, 3)); got != "agent3.5.9.9.log" {
		t.Errorf("log %s", got)
	}
	if _, err := ParseGroup("lane.3.sh", "# swim job 4.1\nrun x true\n", 3); err {
		t.Errorf("a job of another lane in lane.3.sh parsed")
	}
}
