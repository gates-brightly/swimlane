package version

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestStringFormat(t *testing.T) {
	old := BuildDate
	defer func() { BuildDate = old }()
	BuildDate = "20261009"
	if String() != "1.20261009" {
		t.Fatalf("String() = %q", String())
	}
	BuildDate = ""
	if !regexp.MustCompile(`^1\.(\d{8}|dev)$`).MatchString(String()) {
		t.Fatalf("String() = %q", String())
	}
}

func TestEnsureCreatesAndChecks(t *testing.T) {
	root := t.TempDir()
	created, err := Ensure(root)
	if err != nil || !created {
		t.Fatalf("first Ensure: %v %v", created, err)
	}
	if created, err = Ensure(root); err != nil || created {
		t.Fatalf("second Ensure: %v %v", created, err)
	}
	data, _ := os.ReadFile(LockPath(root))
	if !strings.Contains(string(data), "breaking: 1") {
		t.Fatalf("lock:\n%s", data)
	}

	// A lock from a newer breaking version: update swim.
	os.WriteFile(LockPath(root), []byte(fmt.Sprintf("breaking: %d\nversion: %d.20270101\n", Breaking+1, Breaking+1)), 0o644)
	var m ErrMismatch
	if err := Check(root); !errors.As(err, &m) || !strings.Contains(err.Error(), "needs a newer swim") || !strings.Contains(err.Error(), InstallCmd) {
		t.Errorf("newer lock: %v", err)
	}
	if _, err := Ensure(root); !errors.As(err, &m) {
		t.Errorf("Ensure must refuse a mismatched lock, got %v", err)
	}
	// A lock from an older breaking version: confirm the upgrade.
	if msg := (ErrMismatch{Lock: Lock{Breaking: Breaking - 1, Version: "0.20250101"}}).Error(); !strings.Contains(msg, "swim lock --upgrade") {
		t.Errorf("older lock message:\n%s", msg)
	}

	os.WriteFile(LockPath(root), []byte("not: [valid"), 0o644)
	if err := Check(root); err == nil {
		t.Error("corrupt lock accepted")
	}
}
