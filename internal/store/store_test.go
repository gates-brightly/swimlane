package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/config"
)

// An object is kept once, read-only, by its hash, and reads back only
// while its bytes still hash to its name.
func TestPutGet(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	sum, err := s.Put([]byte("echo hi\n"))
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := s.Put([]byte("echo hi\n")); again != sum {
		t.Fatalf("the same bytes kept as %s and %s", sum, again)
	}
	if got, err := s.Get(sum); err != nil || string(got) != "echo hi\n" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	path := s.objectPath(sum)
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o400 {
		t.Fatalf("object mode %v, %v; want read-only", st.Mode().Perm(), err)
	}
	if st, _ := os.Stat(filepath.Join(s.Dir, "objects")); st.Mode().Perm() != 0o700 {
		t.Errorf("objects/ mode %v, want 0700", st.Mode().Perm())
	}
	os.Chmod(path, 0o600)
	os.WriteFile(path, []byte("echo bye\n"), 0o400)
	if _, err := s.Get(sum); err == nil || !strings.Contains(err.Error(), "was changed") {
		t.Errorf("Get of a changed object: %v", err)
	}
	if _, err := s.Get("../../etc/passwd"); err == nil {
		t.Error("Get took a path for a hash")
	}
}

// Update keeps what an earlier write recorded: the stage's record, then
// the summary's.
func TestUpdateMerges(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if err := s.Update("repo-abc123", "r-1", 2, func(r *Round) { r.Script, r.Staged, r.StoredAt = "aa", true, "2026-11-04T10:00:00Z" }); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish("repo-abc123", "r-1", 2, "", "the round", "passed", 0, "== ROUND\n"); err != nil {
		t.Fatal(err)
	}
	r, err := s.Load("repo-abc123", "r-1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if r.Script != "aa" || !r.Staged || r.StoredAt != "2026-11-04T10:00:00Z" || r.Result != "passed" || r.Log != Sum([]byte("== ROUND\n")) || r.Schema != Schema {
		t.Errorf("merged record: %+v", r)
	}
	if err := s.Update("repo-abc123", "../r-1", 2, func(*Round) {}); err == nil {
		t.Error("Update took a run id with a path in it")
	}
	rounds, err := s.Rounds("repo-abc123")
	if err != nil || len(rounds) != 1 {
		t.Errorf("Rounds = %v, %v", rounds, err)
	}
}

// store_dir: off is no store; unset is the XDG data directory.
func TestStoreBase(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/data")
	if dir, ok, err := (&config.Config{}).StoreBase(); dir != "/data/swim/store" || !ok || err != nil {
		t.Errorf("default = %q %v %v", dir, ok, err)
	}
	c := &config.Config{Settings: config.Settings{StoreDir: config.StoreOff}}
	if s, _, err := Open(c); s != nil || err != nil {
		t.Errorf("off: %v %v", s, err)
	}
	c.StoreDir = "relative/dir"
	if _, _, err := c.StoreBase(); err == nil {
		t.Error("a relative store_dir was taken")
	}
}
