package manifest

import (
	"strings"
	"testing"
)

// The manifest hash text is frozen (docs/reference/formats.md): chain
// entries and queue entries pin these hashes, so a change here stales
// every one of them. Change it only with a dual-hash transition
// (docs/reference/formats.md). (The fixture's lookup.yml has no rules: a
// swim that read them hashed this lane the same. It has no swim.yml: a
// swim that read one hashed this lane the same.)
const goldenV2 = "0bd1b730c43f22950df4b08f9eed59a025d165c317895871309d53071d43b016"

func TestManifestHashFrozen(t *testing.T) {
	m := build(t, repo(t), 3)
	if m.SHA256 != goldenV2 {
		t.Errorf("manifest v2 hash is %s, want %s: the hash text changed (docs/reference/formats.md)", m.SHA256, goldenV2)
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
