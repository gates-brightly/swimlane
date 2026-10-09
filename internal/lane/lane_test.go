package lane

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadScript(t *testing.T) {
	root := t.TempDir()
	if info, _ := ReadScript(root, 1); info.Exists {
		t.Fatal("missing lane script reported as existing")
	}
	os.WriteFile(Script(root, 1), []byte("#!/usr/bin/env bash\n# Round: Cut over X\nlane_init 1\n"), 0o755)
	os.WriteFile(Script(root, 2), []byte("#!/usr/bin/env bash\n"+StubMarker+"\n# Round: (none)\n# done with cutover\n"), 0o755)
	os.WriteFile(Script(root, 3), []byte("#!/usr/bin/env bash\necho half written\n"), 0o755)

	if info, _ := ReadScript(root, 1); !info.Pending() || info.Round != "Cut over X" {
		t.Fatalf("lane 1: %+v", info)
	}
	if info, _ := ReadScript(root, 2); !info.Stub || info.Pending() || info.Message != "done with cutover" {
		t.Fatalf("lane 2: %+v", info)
	}
	if info, _ := ReadScript(root, 3); info.Pending() {
		t.Fatalf("lane 3 without Round: should not be pending: %+v", info)
	}
}

func TestRunningAndRefusals(t *testing.T) {
	root := t.TempDir()
	if err := WritePID(root, 1, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if pid, ok := Running(root, 1); !ok || pid != os.Getpid() {
		t.Fatalf("Running = %d %v", pid, ok)
	}
	os.MkdirAll(LogDir(root), 0o755)
	os.WriteFile(Log(root, 1), []byte("x"), 0o644)

	var er ErrRunning
	if _, err := Archive(root, 1, "x"); !errors.As(err, &er) {
		t.Fatalf("Archive on running lane: %v", err)
	}
	if err := WriteScript(root, 1, "x", true); !errors.As(err, &er) {
		t.Fatalf("WriteScript on running lane: %v", err)
	}
	RemovePID(root, 1)
	if _, ok := Running(root, 1); ok {
		t.Fatal("still running after RemovePID")
	}

	// A stale pidfile (dead pid) does not block.
	os.WriteFile(PIDFile(root, 1), []byte("999999999\n"), 0o644)
	if _, ok := Running(root, 1); ok {
		t.Fatal("dead pid reported running")
	}
}

func TestArchive(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(LogDir(root), 0o755)
	os.WriteFile(Log(root, 2), []byte("log"), 0o644)
	dst, err := Archive(root, 2, "Cut over API!")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(dst) != "agent2.prev-cut-over-api.log" || filepath.Dir(dst) != LogDir(root) {
		t.Fatalf("dst = %s", dst)
	}
	os.WriteFile(Log(root, 2), []byte("log2"), 0o644)
	if _, err := Archive(root, 2, "cut over api"); err == nil {
		t.Fatal("expected refusal to overwrite archive")
	}
}

func TestWriteScriptRefusesPending(t *testing.T) {
	root := t.TempDir()
	if err := WriteScript(root, 1, "# Round: A\n", false); err != nil {
		t.Fatal(err)
	}
	if err := WriteScript(root, 1, "# Round: B\n", false); err == nil {
		t.Fatal("expected refusal to replace pending round")
	}
	if err := WriteScript(root, 1, "# Round: B\n", true); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(Script(root, 1))
	if st.Mode().Perm()&0o100 == 0 {
		t.Fatal("lane script not executable")
	}
}

func TestJobIDs(t *testing.T) {
	id := NewJobID()
	if len(id) != 36 || id[14] != '4' || !ValidJobID(id) || id == NewJobID() {
		t.Fatalf("NewJobID = %q", id)
	}
	for ref, want := range map[string]bool{id: true, id[:8]: true, id[:7]: false, "zzzzzzzz": false, "": false} {
		if MatchJob(id, ref) != want {
			t.Errorf("MatchJob(%q, %q) != %v", id, ref, want)
		}
	}
	for s, want := range map[string]bool{"orders-cutover": true, "12345678": false, "short": false, "has space x": false} {
		if ValidJobID(s) != want {
			t.Errorf("ValidJobID(%q) != %v", s, want)
		}
	}
	root := t.TempDir()
	os.WriteFile(Script(root, 1), []byte("# Round: X\n# Job:   "+id+"\nlane_init 1\n"), 0o755)
	if info, _ := ReadScript(root, 1); info.Job != id || info.Round != "X" {
		t.Fatalf("ReadScript: %+v", info)
	}
}
