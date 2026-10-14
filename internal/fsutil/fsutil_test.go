package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.yml")
	if err := WriteAtomic(p, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(p, []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil || string(data) != "two\n" {
		t.Fatalf("read %q, %v", data, err)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", st.Mode().Perm())
	}
	// No temporary file is left behind, and a failed write leaves the old file.
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Errorf("left %d files: %v", len(ents), ents)
	}
	if err := WriteAtomic(filepath.Join(dir, "missing", "x"), []byte("x"), 0o644); err == nil {
		t.Error("write into a missing directory succeeded")
	}

	q := filepath.Join(dir, "sub", "moved.yml")
	os.Mkdir(filepath.Dir(q), 0o755)
	if err := Rename(p, q); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(q); string(data) != "two\n" {
		t.Errorf("moved file holds %q", data)
	}
	if err := SyncDir(dir); err != nil {
		t.Error(err)
	}
}
