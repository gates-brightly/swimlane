package syntax

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOfScriptAndLog(t *testing.T) {
	for src, want := range map[string]int{
		"#!/usr/bin/env bash\n# swim: syntax 2\n":   2,
		"# swim: syntax 3\n":                        3,
		"#!/bin/bash\n# Round: old\n":               1,
		"\n\n\n\n\n# swim: syntax 2\n":              1, // only the first five lines count
		"#!/usr/bin/env bash\n#   swim:   syntax 2": 2,
	} {
		if got := OfScript(src); got != want {
			t.Errorf("OfScript(%q) = %d, want %d", src, got, want)
		}
	}
	for log, want := range map[string]int{
		LogHeader(3) + "\n":                     Version,
		"# swim lane log | syntax 2 | swim 1\n": 2, // an older log
		"=== ROUND START\n":                     1,
	} {
		if got := OfLog(strings.NewReader(log)); got != want {
			t.Errorf("OfLog(%q) = %d, want %d", log, got, want)
		}
	}
	if !strings.HasPrefix(LogHeader(3), "# swim log | syntax 4 | lane 3") || !IsLogHeader(LogHeader(3)) || !IsLogHeader("# swim lane log | syntax 2 | swim 1") {
		t.Error("LogHeader")
	}
	dir := t.TempDir()
	if OfLogFile(filepath.Join(dir, "missing")) != 0 {
		t.Error("a missing log is syntax 0")
	}
	empty := filepath.Join(dir, "empty")
	os.WriteFile(empty, nil, 0o644)
	if OfLogFile(empty) != 0 {
		t.Error("an empty log is syntax 0")
	}
}

func TestCheck(t *testing.T) {
	if Check("lane.1.sh", Version) != nil || Check("log", 2) != nil {
		t.Error("files up to Version read")
	}
	err := Check("lane.1.sh", Version+1)
	var tooNew ErrTooNew
	if !errors.As(err, &tooNew) || !strings.Contains(err.Error(), "reads up to syntax 4") || !strings.Contains(err.Error(), "Update swim") {
		t.Errorf("Check: %v", err)
	}
}

func TestCheckRepo(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".swim", "logs"), 0o755)
	os.WriteFile(filepath.Join(root, "lane.1.sh"), []byte("#!/usr/bin/env swim\n# swim: syntax 3\n"), 0o755)
	os.WriteFile(filepath.Join(root, ".swim", "logs", "lane1.prev-x.log"), []byte("# swim lane log | syntax 2 | swim 1\n"), 0o644)
	os.WriteFile(filepath.Join(root, ".swim", "logs", "lane2.log"), []byte("no header\n"), 0o644)
	if err := CheckRepo(root); err != nil {
		t.Fatalf("older files: %v", err)
	}
	os.WriteFile(filepath.Join(root, ".swim", "logs", "lane3.log"), []byte("# swim log | syntax 9 | swim 3\n"), 0o644)
	if err := CheckRepo(root); err == nil || !strings.Contains(err.Error(), "lane3.log uses swim syntax 9") {
		t.Fatalf("newer log: %v", err)
	}
}

func TestStages(t *testing.T) {
	if StageIndex("snapshot") != 0 || StageIndex("verify") != 3 || StageIndex("setup") != -1 || StageIndex("nope") != -2 {
		t.Error("StageIndex")
	}
}
