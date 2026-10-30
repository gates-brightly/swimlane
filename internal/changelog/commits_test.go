package changelog

import (
	"encoding/base64"
	"reflect"
	"testing"
)

// stamp is what the justfile's git log makes: a record per commit, its
// files after the last field.
func stamp(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestCommits(t *testing.T) {
	log := "\x1eb111496\x1f2026-10-30\x1fswim watch: ask for guards\x1fWith the screen up,\na guard asks.\n\nMore detail.\n\nCo-Authored-By: Claude <noreply@anthropic.com>\n\x1f\n\ninternal/cli/watchask.go\ninternal/cli/watchview.go\ndocs/usage.md\nCHANGELOG.md\n" +
		"\x1ea22d658\x1f2026-10-30\x1ffeat: update git controls\x1f\x1f\n\ninternal/policy/policy.go\n" +
		"\x1e4d786fe\x1f2026-10-29\x1ftrailers only\x1fSigned-off-by: someone <a@b>\n\x1f\n"
	got := Commits(stamp(log))
	want := []Commit{
		{Hash: "b111496", Date: "2026-10-30", Subject: "swim watch: ask for guards", Summary: "With the screen up,\na guard asks.",
			Areas: []AreaCount{{"internal/cli", 2}, {"CHANGELOG.md", 1}, {"docs", 1}}},
		{Hash: "a22d658", Date: "2026-10-30", Subject: "feat: update git controls", Areas: []AreaCount{{"internal/policy", 1}}},
		{Hash: "4d786fe", Date: "2026-10-29", Subject: "trailers only"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Commits =\n%#v\nwant\n%#v", got, want)
	}
	for _, s := range []string{"", "not base64!", stamp("no records here")} {
		if c := Commits(s); c != nil {
			t.Errorf("Commits(%q) = %v, want nil", s, c)
		}
	}
}

func TestArea(t *testing.T) {
	for path, want := range map[string]string{
		"CHANGELOG.md":                  "CHANGELOG.md",
		"internal/cli/watchask.go":      "internal/cli",
		"internal/lint/testdata/x/y.sh": "internal/lint",
		"skills/swim/SKILL.md":          "skills/swim",
		"cmd/swim/main.go":              "cmd/swim",
		"docs/usage.md":                 "docs",
		"e2e/scenarios/blocked/s.sh":    "e2e",
	} {
		if got := Area(path); got != want {
			t.Errorf("Area(%q) = %q, want %q", path, got, want)
		}
	}
}
