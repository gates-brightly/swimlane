package version

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
)

func init() { Current = Syntaxes{Script: 3, Log: 2, State: 1} } // as the syntax package sets them

func TestStringFormat(t *testing.T) {
	oldDate, oldTag := BuildDate, BuildTag
	defer func() { BuildDate, BuildTag = oldDate, oldTag }()
	BuildDate, BuildTag = "20261009", ""
	if String() != "0.20261009" {
		t.Fatalf("String() between releases = %q", String())
	}
	BuildTag = "v0.20261014.2"
	if String() != "0.20261014.2" || Date() != "20261014" {
		t.Fatalf("String() of a release = %q (date %s)", String(), Date())
	}
	if Tag("20261014", 2) != "v0.20261014.2" {
		t.Fatalf("Tag() = %q", Tag("20261014", 2))
	}
	for tag, ok := range map[string]bool{"v0.20261014.2": true, "v0.20261014.0": false, "v0.7.20261013": false, "v.0.11.20261014": false} {
		if _, _, got := ParseTag(tag); got != ok {
			t.Errorf("ParseTag(%q) ok = %v, want %v", tag, got, ok)
		}
	}
	BuildDate, BuildTag = "", ""
	if !regexp.MustCompile(`^0\.(\d{8}|dev)(\.\d+)?$`).MatchString(String()) {
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
	if !strings.Contains(string(data), "script: 3") || !strings.Contains(string(data), "state: 1") {
		t.Fatalf("lock:\n%s", data)
	}

	// A lock from before syntax pinning (breaking: 7) means today's formats.
	os.WriteFile(LockPath(root), []byte("breaking: 7\nversion: 7.20261013\n"), 0o644)
	if err := Check(root); err != nil {
		t.Errorf("legacy breaking 7 lock: %v", err)
	}
	// An older one asks for an upgrade.
	os.WriteFile(LockPath(root), []byte("breaking: 6\nversion: 6.20261012\n"), 0o644)
	if err := Check(root); err == nil || !strings.Contains(err.Error(), "swim lock --upgrade") {
		t.Errorf("legacy breaking 6 lock: %v", err)
	}

	// A lock with a newer syntax: update swim.
	os.WriteFile(LockPath(root), []byte("syntax: {script: 3, log: 2, state: 2}\nversion: 0.20270101.1\n"), 0o644)
	var m ErrMismatch
	if err := Check(root); !errors.As(err, &m) || !strings.Contains(err.Error(), "needs a newer swim") || !strings.Contains(err.Error(), InstallCmd) {
		t.Errorf("newer lock: %v", err)
	}
	if _, err := Ensure(root); !errors.As(err, &m) {
		t.Errorf("Ensure must refuse a mismatched lock, got %v", err)
	}
	// An older syntax: confirm the upgrade.
	if msg := (ErrMismatch{Lock: Lock{Syntax: Syntaxes{Script: 3, Log: 1, State: 1}, Version: "0.20250101.1"}}).Error(); !strings.Contains(msg, "swim lock --upgrade") {
		t.Errorf("older lock message:\n%s", msg)
	}

	os.WriteFile(LockPath(root), []byte("not: [valid"), 0o644)
	if err := Check(root); err == nil {
		t.Error("corrupt lock accepted")
	}
}
