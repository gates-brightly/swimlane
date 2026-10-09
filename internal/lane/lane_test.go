package lane

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestParseScriptMetadataAndDefaults(t *testing.T) {
	full := `#!/usr/bin/env bash
# swim: syntax 2
# Round:   Cut over orders-api
# Job:     job-0001-aaaa
# After:   1 3
# Owner:   zach
# Created: 2026-10-01
# Guards:  ORDERS_ALLOW_REMOVE  remove SST stack (2026-10-09: tf owns it)
# Timeout: 1h30m
# Ticket:  OPS-42
#
# Goal: not a header key (prose after the block)
lane_init 2
if guard ORDERS_ALLOW_REMOVE "x"; then run x true; fi
if guard EXTRA_FLAG "y"; then run y true; fi
summary
`
	i := ParseScript(full)
	if i.Syntax != 2 || i.Round != "Cut over orders-api" || i.Job != "job-0001-aaaa" || strings.Join(i.After, ",") != "1,3" {
		t.Fatalf("core: %+v", i)
	}
	if i.Owner != "zach" || i.Created != "2026-10-01" || i.Timeout != 90*time.Minute || i.TimeoutText != "1h30m" {
		t.Fatalf("ops: %+v", i)
	}
	if len(i.Guards) != 2 || i.Guards[0].Flag != "ORDERS_ALLOW_REMOVE" || !strings.HasPrefix(i.Guards[0].Desc, "remove SST") || i.Guards[1].Flag != "EXTRA_FLAG" || i.Guards[1].Desc != "" {
		t.Fatalf("guards: %+v", i.Guards)
	}
	if len(i.Extra) != 1 || i.Extra[0].K != "Ticket" || i.Extra[0].V != "OPS-42" {
		t.Fatalf("extra keys (Goal: is prose, not a key): %+v", i.Extra)
	}

	minimal := "#!/usr/bin/env bash\n# swim: syntax 2\n# Round: x\n# Timeout: soon\nlane_init 1\n"
	m := ParseScript(minimal)
	if m.Owner != "" || m.Timeout != 0 || len(m.After) != 0 || len(m.Guards) != 0 || len(m.Problems) != 1 {
		t.Fatalf("defaults: %+v", m)
	}
	root := t.TempDir()
	os.WriteFile(Script(root, 1), []byte(minimal), 0o755)
	if info, err := ReadScript(root, 1); err != nil || len(info.Created) != 10 {
		t.Fatalf("Created should default to the file date: %+v %v", info, err)
	}
	os.WriteFile(Script(root, 2), []byte("#!/usr/bin/env bash\n# swim: syntax 9\n# Round: from the future\n"), 0o755)
	if _, err := ReadScript(root, 2); err == nil || !strings.Contains(err.Error(), "syntax 9") || !strings.Contains(err.Error(), "go install") {
		t.Fatalf("too-new script: %v", err)
	}
}

const v1Template = `#!/usr/bin/env bash
# Round: Old template round
# Job:   old-job-0001
# Lane:  swim 1    Written: 2026-10-01
#
# Steps:
#   1. Snapshot current state (read-only, saved under .swim/snapshots/)

_swim_lib=$("${SWIM_BIN:-swim}" lib) || { echo "swim not found on PATH" >&2; exit 1; }
eval "$_swim_lib"
lane_init 1

# 1. Snapshot (read-only)
snapshot "current state" echo state

# 2. Checks
run "precheck" true

# 3. Change — one guard flag per destructive action
# if guard X "y"; then run z true; fi

# 4. Verify
run "verify" true

summary
`

func TestMigrateScript(t *testing.T) {
	got, ok := MigrateScript(v1Template)
	if !ok {
		t.Fatal("not migrated")
	}
	for _, want := range []string{
		"#!/usr/bin/env bash\n# swim: syntax 2\n# Round: Old template round\n# Job:   old-job-0001\n# Created: 2026-10-01\n# Lane:",
		"# 1. Snapshot (read-only)\nstage snapshot\nsnapshot",
		"# 2. Checks\nstage check\n",
		"# 3. Change — one guard flag per destructive action\nstage change\n",
		"# 4. Verify\nstage verify\nrun \"verify\" true",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("migrated script missing %q:\n%s", want, got)
		}
	}
	// The header's "#   1. Snapshot" step list is prose, not a section.
	if strings.Count(got, "stage snapshot") != 1 {
		t.Errorf("stage snapshot inserted %d times", strings.Count(got, "stage snapshot"))
	}
	if again, ok := MigrateScript(got); ok || again != got {
		t.Error("migrating a syntax 2 script must be a no-op")
	}
	i := ParseScript(got)
	if i.Syntax != 2 || i.Round != "Old template round" || i.Created != "2026-10-01" {
		t.Fatalf("parsed after migration: %+v", i)
	}
	stub, ok := MigrateScript("#!/usr/bin/env bash\n# swim:stub\n# Round: (none) - nothing pending\n# done\necho x\n")
	if !ok || !strings.HasPrefix(stub, "#!/usr/bin/env bash\n# swim: syntax 2\n# swim:stub\n") {
		t.Fatalf("stub: %q", stub)
	}
	if s := ParseScript(stub); !s.Stub || s.Message != "done" {
		t.Fatalf("stub after migration: %+v", s)
	}
}

func TestIndentedProseIsNotAHeaderKey(t *testing.T) {
	src := "#!/usr/bin/env bash\n# swim: syntax 2\n# Round:   r\n# After:\n# Timeout:\n#\n#   After:   lanes that must pass first, e.g. \"1\"\n#   Timeout: limit for the whole round\n#   Guards:  one line per flag\nlane_init 1\n"
	i := ParseScript(src)
	if len(i.After) != 0 || i.Timeout != 0 || len(i.Guards) != 0 || len(i.Problems) != 0 {
		t.Fatalf("prose read as header keys: %+v", i)
	}
}
