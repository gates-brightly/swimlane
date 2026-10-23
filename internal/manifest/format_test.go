package manifest

import (
	"strings"
	"testing"
)

// The manifest hash text is frozen (docs/formats.md): approvals, queue
// entries, schedules and hub commands pin these hashes, so a change here
// stales every one of them. Change it only with a dual-hash transition,
// as v1 to v2 was (Matches, HashV1).
const (
	goldenV2 = "7ced577be71ab4bc4701b0d1ccc3d6b0935e26b67143ffc28a4795fd3ce8fd32"
	// goldenV1 is what swim before manifest v2 computed for the same repo.
	goldenV1 = "72cd40405c466024b39e19c6c8ff9c408b0b8004da92846d92dc4b40bd4df9a4"
)

func TestManifestHashFrozen(t *testing.T) {
	m := build(t, repo(t), 3)
	if m.SHA256 != goldenV2 {
		t.Errorf("manifest v2 hash is %s, want %s: the hash text changed (docs/formats.md)", m.SHA256, goldenV2)
	}
	if m.LegacySHA256 != goldenV1 {
		t.Errorf("manifest v1 hash is %s, want %s: v1 pins would stop matching", m.LegacySHA256, goldenV1)
	}
	if !m.Matches(goldenV1) || !m.Matches(goldenV2) || m.Matches("") || m.Matches("other") {
		t.Error("Matches should accept the v1 and v2 hashes, and nothing else")
	}
	text := m.Pinned.text("swim manifest v2", 3, "config: "+m.Pinned.Config.SHA256)
	for _, line := range []string{"swim manifest v2\nlane: 3\nscript: lane.3.sh ", "\ninclude: lib/deep.sh ", "\nsidecar: lane.3.yml ", "\nrunner: ", "\nconfig: " + m.Pinned.Config.SHA256 + "\nlookup-rules: "} {
		if !strings.Contains(text, line) {
			t.Errorf("hash text lacks %q:\n%s", line, text)
		}
	}
}

// A staged manifest from before v2 still verifies, and still hashes as v1.
func TestOldManifestStillVerifies(t *testing.T) {
	m := build(t, repo(t), 3)
	old := m.Pinned
	old.Config = Policy{SHA256: m.Pinned.Config.LegacySHA256, Version: 3, Keys: m.Pinned.Config.Keys, Rules: m.Pinned.Config.Rules}
	if got := old.Hash(3); got != goldenV1 {
		t.Errorf("a v1 manifest hashes as %s, want %s", got, goldenV1)
	}
}

// A stale approval's reason names the policy key that changed.
func TestDiffNamesPolicyKey(t *testing.T) {
	root := repo(t)
	before := build(t, root, 3)
	write(t, root, "swim.yml", "blocked_commands: [\"rm -rf /\"]\nmax_parallel: 2\nsigning: required\n")
	after := build(t, root, 3)
	if after.Matches(before.SHA256) {
		t.Fatal("setting a policy key should change the manifest")
	}
	why := strings.Join(Diff(before.Pinned, after.Pinned), "; ")
	if !strings.Contains(why, "signing") || strings.Contains(why, "blocked_commands") {
		t.Errorf("Diff = %q, want it to name signing alone", why)
	}
	// max_parallel isn't policy: changing it changes nothing.
	write(t, root, "swim.yml", "blocked_commands: [\"rm -rf /\"]\nmax_parallel: 5\nsigning: required\n")
	if again := build(t, root, 3); again.SHA256 != after.SHA256 {
		t.Error("max_parallel changed the manifest")
	}
}
