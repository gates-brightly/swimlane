package status

import (
	"os"
	"strings"
	"testing"
)

// An update that changes nothing leaves status.yml as it was, updated_at
// included, however much time has passed; a real change writes it.
func TestUpdateWritesOnlyChanges(t *testing.T) {
	root := t.TempDir()
	if err := UpdateFile(root, 2, func(f *File) error { f.Get(1).Round = "first"; return nil }); err != nil {
		t.Fatal(err)
	}
	// As if the clock had moved on since that write.
	data, _ := os.ReadFile(Path(root))
	lines := strings.Split(string(data), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "updated_at:") {
			lines[i] = `updated_at: "2000-01-01T00:00:00Z"`
		}
	}
	os.WriteFile(Path(root), []byte(strings.Join(lines, "\n")), 0o644)
	before, _ := os.ReadFile(Path(root))

	if err := UpdateFile(root, 2, func(f *File) error { f.Get(1).Round = "first"; return nil }); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(Path(root)); string(after) != string(before) {
		t.Errorf("an update that changed nothing rewrote status.yml:\n%s", after)
	}

	if err := UpdateFile(root, 2, func(f *File) error { f.Get(1).Round = "second"; return nil }); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(Path(root))
	if strings.Contains(string(after), "2000-01-01") || !strings.Contains(string(after), "round: second") {
		t.Errorf("a change didn't write, or kept the old updated_at:\n%s", after)
	}
}
