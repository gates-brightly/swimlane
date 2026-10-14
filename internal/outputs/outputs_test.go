package outputs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckHashAndInputs(t *testing.T) {
	root := t.TempDir()
	dir := Dir(root, "job-1", "r-1")
	os.MkdirAll(filepath.Join(dir, "pages"), 0o755)
	os.WriteFile(filepath.Join(dir, "stats.json"), []byte("{}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "pages", "b"), []byte("b"), 0o644)
	os.WriteFile(filepath.Join(dir, "pages", "a"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(dir, "extra"), []byte("x"), 0o644)
	if err := SetValue(dir, "VERSION", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := SetValue(dir, "bad key", "x"); err == nil {
		t.Error("a bad key was accepted")
	}
	res, err := Check(dir, []string{"stats.json", "pages/", "missing.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Missing, ",") != "missing.txt" || strings.Join(res.Undeclared, ",") != "extra" || len(res.Outputs) != 3 {
		t.Errorf("check: %+v", res)
	}
	sums := res.Sums()
	if v, ok := Value(dir, "VERSION"); !ok || v != "v1" || sums[ValuePrefix+"VERSION"] == "" {
		t.Errorf("value: %q %v %v", v, ok, sums)
	}
	// A directory's hash is its sorted manifest: the order files were
	// written in doesn't matter, their contents do.
	h1, size, files, _ := Hash(filepath.Join(dir, "pages"))
	os.WriteFile(filepath.Join(dir, "pages", "a"), []byte("A"), 0o644)
	h2, _, _, _ := Hash(filepath.Join(dir, "pages"))
	if h1 == h2 || size != 2 || files != 2 {
		t.Errorf("dir hash: %s %s %d %d", h1, h2, size, files)
	}
	Freeze(dir)
	if st, _ := os.Stat(filepath.Join(dir, "stats.json")); st.Mode().Perm()&0o222 != 0 {
		t.Error("Freeze left a file writable")
	}
	AddInput(dir, Input{Key: "2:x", Sum: "old", Round: "j r"})
	AddInput(dir, Input{Key: "2:x", Sum: "new", Round: "j r"})
	AddInput(dir, Input{Key: "3:y", Sum: "s", Round: "k r"})
	if in := Inputs(dir); len(in) != 2 || in[0].Sum != "new" || in[1].Key != "3:y" {
		t.Errorf("inputs: %+v", in)
	}
}

func TestNamesPruneAndSizes(t *testing.T) {
	for name, ok := range map[string]bool{"stats.json": true, "pages/": true, "a/b.txt": true, "../x": false, "/etc/passwd": false,
		"a/../b": false, ".swim-values": false, "": false} {
		if ValidName(name) != ok {
			t.Errorf("ValidName(%q) = %v", name, !ok)
		}
	}
	root := t.TempDir()
	for _, d := range []string{Dir(root, "old", "r-1"), Dir(root, "keep", "r-1")} {
		os.MkdirAll(d, 0o755)
		past := time.Now().Add(-48 * time.Hour)
		os.Chtimes(d, past, past)
	}
	os.MkdirAll(Dir(root, "new", "r-2"), 0o755)
	keep := func(job, run string) bool { return job == "keep" }
	if stale := Stale(root, 24*time.Hour, keep); len(stale) != 1 || !strings.Contains(stale[0], "old") {
		t.Errorf("stale: %v", stale)
	}
	if removed := Prune(root, 24*time.Hour, keep); len(removed) != 1 {
		t.Errorf("pruned: %v", removed)
	}
	if _, err := os.Stat(Dir(root, "old", "r-1")); err == nil {
		t.Error("the old round's outputs survived")
	}
	for n, want := range map[int64]string{12: "12 B", 2048: "2.0 KB", 3 << 20: "3.0 MB", 5 << 30: "5.0 GB"} {
		if HumanSize(n) != want {
			t.Errorf("HumanSize(%d) = %s", n, HumanSize(n))
		}
	}
}
