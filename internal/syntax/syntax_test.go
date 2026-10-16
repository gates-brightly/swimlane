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
	if OfLog(strings.NewReader(LogHeader(3)+"\n")) != Current || OfLog(strings.NewReader("=== ROUND START\n")) != 1 {
		t.Error("OfLog")
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

func TestCheckLimits(t *testing.T) {
	if Check("log", Current) != nil || Check("log", Current+1) == nil {
		t.Error("logs read up to Current")
	}
	if CheckScript("lane.1.sh", Script) != nil {
		t.Error("scripts read up to Script")
	}
	err := CheckScript("lane.1.sh", Script+1)
	var tooNew ErrTooNew
	if !errors.As(err, &tooNew) || !strings.Contains(err.Error(), "reads up to syntax 4") {
		t.Errorf("CheckScript: %v", err)
	}
}

func TestStages(t *testing.T) {
	if StageIndex("snapshot") != 0 || StageIndex("verify") != 3 || StageIndex("setup") != -1 || StageIndex("nope") != -2 {
		t.Error("StageIndex")
	}
}
