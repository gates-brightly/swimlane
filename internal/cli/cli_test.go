package cli

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDidYouMean(t *testing.T) {
	cmds := []string{"status", "changelog", "run", "verify", "queue"}
	for word, want := range map[string]string{
		"stauts":   " (did you mean status?)",
		"changlog": " (did you mean changelog?)",
		"deploy":   "",
		"x":        "",
	} {
		if got := didYouMean(word, cmds); got != want {
			t.Errorf("didYouMean(%q) = %q, want %q", word, got, want)
		}
	}
}

// An unknown flag names the closest one.
func TestUnknownFlagSuggests(t *testing.T) {
	var plain bool
	_, err := flags{bools: map[string]*bool{"plain": &plain}}.parse([]string{"--pain"})
	if err == nil || err.Error() != "unknown flag --pain (did you mean --plain?)" {
		t.Errorf("err = %v", err)
	}
}

// --force acknowledges gaps and deletions, never changed content.
func TestAmendable(t *testing.T) {
	for p, want := range map[string]bool{
		"entry #3 (line 2): entry #2 is missing before it (removed)":                                                          true,
		"entry #3 (line 2): prev hash doesn't match the entry before it (an earlier entry was changed, removed or reordered)": true,
		"the logs of 2 rounds are gone (deleted, not archived), so their contents can't be rechecked: #1, 3":                  true,
		"entry #4: the script copy 1a2b3c4d5e6f.sh is missing":                                                                true,
		"entry #4 (line 4): signature doesn't match the entry (it was changed after signing): bad":                            false,
		"entry #5: swim 1's log for run r-1 was changed after the round ended":                                                false,
		"entry #4: the kept script 1a2b3c4d5e6f.sh was changed":                                                               false,
		"entry #6: swim 1's staged copy in run r-1: manifest changed since staged (manifest.yml)":                             false,
	} {
		if amendable(p) != want {
			t.Errorf("amendable(%q) = %v", p, !want)
		}
	}
}

// The forms people type where swim reads a lane, or a time: each is
// read as what it unambiguously means.
func TestUnambiguousForms(t *testing.T) {
	if got := strings.Join(laneArgs([]string{"2,3", "17", "--yaml", "a,,b"}), " "); got != "2 3 17 --yaml a b" {
		t.Errorf("laneArgs: %q", got)
	}
	// A lane script's path is this repo's lane only when it's in this
	// repo's root; any other is refused, never read by its file name.
	root := t.TempDir()
	other := t.TempDir()
	t.Chdir(root)
	for in, want := range map[string]string{"lane.17.sh": "17", "./lane.3.py": "3", filepath.Join(root, "lane.4"): "4", "17": "17", "lanes.x.sh": "lanes.x.sh", "3f2a9c1e": "3f2a9c1e"} {
		if got, _, err := lanePath(root, in); err != nil || got != want {
			t.Errorf("lanePath(%q) = %q %v, want %q", in, got, err, want)
		}
	}
	for _, in := range []string{filepath.Join(other, "lane.22.sh"), "/etc/lane.22.sh", "../lane.3.sh", "sub/lane.3.sh"} {
		if _, ok, err := lanePath(root, in); !ok || err == nil || !strings.Contains(err.Error(), "isn't one of this repo's lanes") {
			t.Errorf("lanePath(%q): ok %v, err %v; want it refused", in, ok, err)
		}
	}
	for in, want := range map[string]time.Duration{"90m": 90 * time.Minute, "1h": time.Hour, "2d": 48 * time.Hour} {
		if got, ok := sinceAgo(in); !ok || got != want {
			t.Errorf("sinceAgo(%q) = %v %v", in, got, ok)
		}
	}
	for _, bad := range []string{"0d", "-1h", "soon", "3f2a9c1e"} {
		if _, ok := sinceAgo(bad); ok {
			t.Errorf("sinceAgo(%q) read as a duration", bad)
		}
	}
	yaml := false
	if _, err := (flags{bools: map[string]*bool{"yaml": &yaml}}).parse([]string{"--json"}); err == nil || !strings.Contains(err.Error(), "--yaml") {
		t.Errorf("--json: %v", err)
	}
	if _, err := (flags{}).parse([]string{"--dry-run"}); err == nil || !strings.Contains(err.Error(), "already a dry run") {
		t.Errorf("--dry-run: %v", err)
	}
}
