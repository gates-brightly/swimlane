package lane

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseScriptName(t *testing.T) {
	for name, want := range map[string]int{
		"lane.1.sh": 1, "lane.12.py": 12, "lane.3": 3, "lane.4.ts": 4, "lane.5.bin": 5,
		"lane.1.yml": 0, "lane.1.sh.tmp": 0, "lane.0.sh": 0, "lane.x.sh": 0, "lane.1.json": 0, "notlane.1.sh": 0,
	} {
		n, _, ok := ParseScriptName(name)
		if (want == 0) == ok || (ok && n != want) {
			t.Errorf("%s: got %d %v, want %d", name, n, ok, want)
		}
	}
	if n, ok := ParseLaneFile("lane.7.yml"); !ok || n != 7 {
		t.Errorf("sidecar: %d %v", n, ok)
	}
}

func TestScriptsDiscovery(t *testing.T) {
	root := t.TempDir()
	write := func(name, src string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(src), mode); err != nil {
			t.Fatal(err)
		}
	}
	if got := filepath.Base(Script(root, 1)); got != "lane.1.sh" {
		t.Errorf("no script: %s", got)
	}
	write("lane.1.py", "# Round: py\n", 0o644)
	write("lane.1.yml", "round: from sidecar\n", 0o644)
	write("lane.1.notes", "not runnable\n", 0o644)
	if got := ScriptName(root, 1); got != "lane.1.py" {
		t.Errorf("Script = %s", got)
	}
	if err := ScriptConflict(root, 1); err != nil {
		t.Error(err)
	}
	write("lane.1.cfg", "#!/bin/sh\n", 0o755) // executable: a script
	if err := ScriptConflict(root, 1); err == nil || !strings.Contains(err.Error(), "lane.1.cfg, lane.1.py") {
		t.Errorf("conflict: %v", err)
	}
	if _, err := ReadScript(root, 1); err == nil {
		t.Error("ReadScript of a lane with two scripts: no error")
	}
	os.Remove(filepath.Join(root, "lane.1.cfg"))
	info, err := ReadScript(root, 1)
	if err != nil || info.Round != "from sidecar" || !info.Pending() {
		t.Errorf("sidecar wins: %+v %v", info, err)
	}
	write("lane.6.js", "", 0o644)
	if got := Associated(root); fmt.Sprint(got) != "[1 6]" {
		t.Errorf("associated: %v", got)
	}
	// WriteScriptAs replaces the lane's script in any language and its sidecar.
	if err := WriteScriptAs(root, 1, "sh", "# swim:stub\n", true); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"lane.1.py", "lane.1.yml"} {
		if _, err := os.Stat(filepath.Join(root, gone)); err == nil {
			t.Errorf("%s still there", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "lane.1.notes")); err != nil {
		t.Error("lane.1.notes (not a script) was removed")
	}
}

func TestSlashHeaderAndSidecar(t *testing.T) {
	src := "#!/usr/bin/env node\n// swim: syntax 2\n// Round: js round\n// Job:   job-abcdef12\n// After: 1 2\n// Guards: ALLOW_X  do x\n// Timeout: 5m\nspawnSync(\"swim\", [\"guard\", \"BODY_FLAG\", \"y\"])\n// swim guard COMMENTED nope\n"
	info := ParseScript(src)
	if info.Round != "js round" || info.Job != "job-abcdef12" || strings.Join(info.After, ",") != "1,2" || info.Timeout.String() != "5m0s" || info.Syntax != 2 {
		t.Errorf("header: %+v", info)
	}
	if got := strings.Join(info.GuardFlags(), ","); got != "ALLOW_X" {
		t.Errorf("guards = %s", got)
	}

	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "lane.2"), []byte("\x7fELF\x00\x01binary"), 0o755)
	os.WriteFile(filepath.Join(root, "lane.2.yml"), []byte("round: compiled\njob: job-12345678\nafter: \"1, 3\"\nguards:\n  SHIP: ship it\n  bad-name: x\nlocks: [db, cache]\nstep-timeout: 30s\ntimeout: soon\n"), 0o644)
	info, err := ReadScript(root, 2)
	if err != nil {
		t.Fatal(err)
	}
	if info.Round != "compiled" || info.Job != "job-12345678" || strings.Join(info.After, ",") != "1,3" ||
		strings.Join(info.Locks, ",") != "db,cache" || info.StepTimeout.String() != "30s" || info.Timeout != 0 ||
		strings.Join(info.GuardFlags(), ",") != "SHIP" || info.Guards[0].Desc != "ship it" {
		t.Errorf("sidecar: %+v", info)
	}
	if len(info.Problems) != 2 {
		t.Errorf("problems (bad guard name, bad timeout): %q", info.Problems)
	}
	os.WriteFile(filepath.Join(root, "lane.2.yml"), []byte("round: x\nrund: typo\n"), 0o644)
	if _, err := ReadScript(root, 2); err == nil || !strings.Contains(err.Error(), "lane.2.yml") {
		t.Errorf("unknown sidecar key: %v", err)
	}
}

func TestEndClaim(t *testing.T) {
	root := t.TempDir()
	if _, ok := EndCode(root, 1); ok {
		t.Fatal("claimed before any claim")
	}
	if !ClaimEnd(root, 1, 130) || ClaimEnd(root, 1, 1) {
		t.Fatal("only the first claim counts")
	}
	if c, ok := EndCode(root, 1); !ok || c != 130 {
		t.Errorf("EndCode = %d %v", c, ok)
	}
	ClearEnd(root, 1)
	if _, ok := EndCode(root, 1); ok {
		t.Error("cleared claim still there")
	}
}

func TestAlwaysHeaders(t *testing.T) {
	info := ParseScript("# Round: lint\n# Always: yes\n# Rerun-Children: true\n")
	if !info.Always || !info.RerunChildren || len(info.Problems) > 0 {
		t.Errorf("header: always=%v rerun=%v problems=%v", info.Always, info.RerunChildren, info.Problems)
	}
	info = ParseScript("# Round: x\n# Rerun-Children: yes\n")
	if info.RerunChildren || len(info.Problems) != 1 || !strings.Contains(info.Problems[0], "needs `Always: yes`") {
		t.Errorf("Rerun-Children without Always: rerun=%v problems=%v", info.RerunChildren, info.Problems)
	}
	if info = ParseScript("# Round: x\n# Always: sometimes\n"); info.Always || len(info.Problems) != 1 {
		t.Errorf("bad value: always=%v problems=%v", info.Always, info.Problems)
	}
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "lane.1.py"), []byte("# Round: py\n"), 0o644)
	os.WriteFile(filepath.Join(root, "lane.1.yml"), []byte("always: true\nrerun-children: true\n"), 0o644)
	if info, err := ReadScript(root, 1); err != nil || !info.Always || !info.RerunChildren {
		t.Errorf("sidecar: %+v %v", info, err)
	}
}
