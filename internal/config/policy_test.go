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

// modules: (removed) still counts in the policy hash exactly as it did, so
// a repo that sets it keeps its approvals when swim drops modules.
func TestRemovedModulesPolicyHashFrozen(t *testing.T) {
	c := loadWith(t, "modules:\n  allow: [notify, ./modules/*]\n  dir: modules\n")
	if want := "8f272f14ca05de3eb965aa31dbd723605daa60c98545b77bd83b4a3bd67a9f15"; c.PolicyHash() != want {
		t.Errorf("modules: set: %s, want %s", c.PolicyHash(), want)
	}
}
