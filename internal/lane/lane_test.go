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
	// The header is the lane's guards: EXTRA_FLAG, used but not declared,
	// isn't one (it's refused when it asks).
	if len(i.Guards) != 1 || i.Guards[0].Flag != "ORDERS_ALLOW_REMOVE" || !strings.HasPrefix(i.Guards[0].Desc, "remove SST") {
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

func TestIndentedProseIsNotAHeaderKey(t *testing.T) {
	src := "#!/usr/bin/env bash\n# swim: syntax 2\n# Round:   r\n# After:\n# Timeout:\n#\n#   After:   lanes that must pass first, e.g. \"1\"\n#   Timeout: limit for the whole round\n#   Guards:  one line per flag\nlane_init 1\n"
	i := ParseScript(src)
	if len(i.After) != 0 || i.Timeout != 0 || len(i.Guards) != 0 || len(i.Problems) != 0 {
		t.Fatalf("prose read as header keys: %+v", i)
	}
}

func TestFileLocksAllOrNothing(t *testing.T) {
	root := t.TempDir()
	a, name, _, err := TryFileLocks(root, []string{"tf/orders", "vpc"}, "run A")
	if err != nil || name != "" || len(a) != 2 {
		t.Fatalf("first take: %v %q %v", a, name, err)
	}
	held := HeldLocks(root)
	if len(held) != 2 || held[0].Name != "tf/orders" || held[0].By != "run A" {
		t.Fatalf("held: %+v", held)
	}
	b, name, by, err := TryFileLocks(root, []string{"other", "vpc"}, "run B")
	if err != nil || b != nil || name != "vpc" || by != "run A" {
		t.Fatalf("second take should fail on vpc held by run A: %v %q %q %v", b, name, by, err)
	}
	// All or nothing: "other" must not be left held by the failed attempt.
	for _, h := range HeldLocks(root) {
		if h.Name == "other" {
			t.Fatal("partial lock left behind")
		}
	}
	a.Release()
	if len(HeldLocks(root)) != 0 {
		t.Fatal("locks still held after release")
	}
	if !ValidLockName("tf/orders-1.2_x") || ValidLockName("a b") || ValidLockName("../x") || ValidLockName("") {
		t.Fatal("ValidLockName")
	}
}

func TestNewJobIDPrefixNeverNumeric(t *testing.T) {
	for i := 0; i < 5000; i++ {
		id := NewJobID()
		if strings.Trim(id[:8], "0123456789") == "" {
			t.Fatalf("job id %s has an all-digit prefix (reads as a lane number)", id)
		}
		if !ValidJobID(id) {
			t.Fatalf("invalid job id %s", id)
		}
	}
}

// A lane's guards are its header's: nothing in the body adds one, quoted
// or not, and "*" lets it name flags at run time.
func TestGuardsFromHeaderOnly(t *testing.T) {
	src := "#!/usr/bin/env bash\n# Round: r\n# Guards: ALLOW_X  do x\n" +
		`if guard ALLOW_X "do x, unless the guard was declined"; then run "x" true; fi` + "\n" +
		`run "say" bash -c 'echo "no report: the guard was declined"'` + "\n" +
		"guard ALLOW_Y 'y'\n" +
		`guard "$FLAG" "dynamic"` + "\n"
	info := ParseScript(src)
	if got := strings.Join(info.GuardFlags(), " "); got != "ALLOW_X" || info.DynamicGuards != 0 {
		t.Errorf("guards: %v, dynamic %d", got, info.DynamicGuards)
	}
	info = ParseScript("#!/usr/bin/env bash\n# Round: r\n# Guards: *  flags from the inventory\n" + `guard "$FLAG" "x"` + "\n")
	if len(info.Guards) != 0 || info.DynamicGuards != 1 {
		t.Errorf("Guards: *: %+v, dynamic %d", info.Guards, info.DynamicGuards)
	}
}

// The body is read for stages and handlers, quotes and comments aside.
func TestBodyStages(t *testing.T) {
	for src, staged := range map[string]bool{
		"stage check\n":                                     true,
		"  if true; then stage change; fi\n":                true,
		`"$SWIM_BIN" stage verify` + "\n":                   true,
		`subprocess.run(["swim", "stage", "check"])` + "\n": true,
		`echo "next: stage change"` + "\n":                  false,
		"# stage check\n":                                   false,
		"stage nonsense\n":                                  false,
	} {
		if got := ParseScript("#!/usr/bin/env bash\n# Round: r\n" + src).Staged; got != staged {
			t.Errorf("%q: Staged %v, want %v", src, got, staged)
		}
	}
}

// The corpus: stages, evidence steps and handlers named where bash never
// runs them (a heredoc, a string over several lines, a comment) aren't
// any of them.
func TestBodyTextIsNotCode(t *testing.T) {
	src := "#!/usr/bin/env bash\n# Round: r\n" +
		"cat <<'EOF'\nstage change\nevidence \"plan\" tf plan\nmod --evidence tf plan\non_failure() {\nEOF\n" +
		"cat <<EOF\nstage verify\nEOF\n" +
		"echo \"over\nstage check\nevidence \\\"x\\\" true\non_exit() {\"\n" +
		"# stage check; evidence \"x\" true\n"
	info := ParseScript(src)
	if info.Staged || info.UsesEvidence {
		t.Errorf("text read as code: staged %v, evidence %v", info.Staged, info.UsesEvidence)
	}
	if info := ParseScript(src + "stage check\nevidence \"plan\" true\n"); !info.Staged || !info.UsesEvidence {
		t.Errorf("code after the text: staged %v, evidence %v", info.Staged, info.UsesEvidence)
	}
}
