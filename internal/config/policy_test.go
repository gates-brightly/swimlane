package config

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func loadWith(t *testing.T, swimYML string) *Config {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	if swimYML != "" {
		os.WriteFile(filepath.Join(root, "swim.yml"), []byte(swimYML), 0o644)
	}
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Every policy key has a value PolicyHash reads, and nothing else does.
func TestPolicyValuesCoverPolicyKeys(t *testing.T) {
	vals := (&Config{}).policyValues()
	var got []string
	for k := range vals {
		got = append(got, k)
	}
	want := append([]string(nil), PolicyKeys...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("policyValues has %v, PolicyKeys %v", got, want)
	}
}

// The policy hash is frozen (docs/formats.md): "swim policy v2", then each
// key swim.yml sets with its value's hash. A key swim.yml doesn't set adds
// nothing, so a key swim adds later changes no repo's hash.
func TestPolicyHashFrozen(t *testing.T) {
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	if got, want := loadWith(t, "").PolicyHash(), sum("swim policy v2\n"); got != want {
		t.Errorf("no policy keys set: %s, want %s", got, want)
	}
	c := loadWith(t, "signing: required\nmax_parallel: 3\n")
	if got, want := c.PolicyHash(), sum("swim policy v2\nsigning "+sum("signing: required\n")+"\n"); got != want {
		t.Errorf("signing set: %s, want %s", got, want)
	}
	if set := c.PolicySet(); len(set) != 1 || set["signing"] == "" {
		t.Errorf("PolicySet = %v, want signing alone", set)
	}
	// An empty list swim.yml writes is set: it can mean "none" where
	// leaving it out means the defaults.
	if loadWith(t, "blocked_commands: []\n").PolicyHash() == loadWith(t, "").PolicyHash() {
		t.Error("blocked_commands: [] should count as set")
	}
}

// The v1 policy hash is what swim before manifest v2 computed: v1 pins
// match only while it is.
func TestLegacyPolicyHashFrozen(t *testing.T) {
	const empty = "48dd9621d956e6a5fb846357507fbe83e22384fc11179b1c85405ad5872837a8"
	if got := loadWith(t, "").LegacyPolicyHash(); got != empty {
		t.Errorf("v1 policy hash of an empty swim.yml is %s, want %s", got, empty)
	}
}
