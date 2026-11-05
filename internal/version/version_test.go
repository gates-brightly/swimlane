package version

import (
	"regexp"
	"testing"
)

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
