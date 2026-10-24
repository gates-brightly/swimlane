package manifest

import (
	"strings"
	"testing"
)

// The manifest hash text is frozen (docs/formats.md): approvals, queue
// entries, schedules and hub commands pin these hashes, so a change here
// stales every one of them. Change it only with a dual-hash transition
// (docs/formats.md).
const goldenV2 = "7ced577be71ab4bc4701b0d1ccc3d6b0935e26b67143ffc28a4795fd3ce8fd32"

func TestManifestHashFrozen(t *testing.T) {
	m := build(t, repo(t), 3)
	if m.SHA256 != goldenV2 {
		t.Errorf("manifest v2 hash is %s, want %s: the hash text changed (docs/formats.md)", m.SHA256, goldenV2)
	}
	if !m.Matches(goldenV2) || m.Matches("") || m.Matches("other") {
		t.Error("Matches should accept the manifest's hash and nothing else")
	}
	text := m.Pinned.text("swim manifest v2", 3, "config: "+m.Pinned.Config.SHA256)
	for _, line := range []string{"swim manifest v2\nlane: 3\nscript: lane.3.sh ", "\ninclude: lib/deep.sh ", "\nsidecar: lane.3.yml ", "\nrunner: ", "\nconfig: " + m.Pinned.Config.SHA256 + "\nlookup-rules: "} {
		if !strings.Contains(text, line) {
			t.Errorf("hash text lacks %q:\n%s", line, text)
		}
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
