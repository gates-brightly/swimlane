package step

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/status"
)

func TestQuoteRoundTripsThroughBash(t *testing.T) {
	args := []string{"printf", "%s|", "plain", "two words", "it's", `"dq"`, "$HOME", "", "a\nb", "*", "--flag=x;y"}
	line := Quote(args)
	out, err := exec.Command("bash", "-c", line).Output()
	if err != nil {
		t.Fatalf("bash -c %q: %v", line, err)
	}
	want := strings.Join(args[2:], "|") + "|"
	if string(out) != want {
		t.Fatalf("got %q want %q (line %s)", out, want, line)
	}
	if got := Quote([]string{"aws", "s3", "ls", "s3://bucket/key-1.txt"}); got != "aws s3 ls s3://bucket/key-1.txt" {
		t.Fatalf("safe args quoted unnecessarily: %s", got)
	}
}

func TestStripWriter(t *testing.T) {
	in := "\x1b[1;31mred\x1b[0m plain \x1b]0;title\x07after \x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\ \x1b(Bdone\n"
	var buf bytes.Buffer
	w := &StripWriter{W: &buf}
	// Feed one byte at a time to prove state carries across writes.
	for i := 0; i < len(in); i++ {
		w.Write([]byte{in[i]})
	}
	if got, want := buf.String(), "red plain after link done\n"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func newOpts(t *testing.T, args ...string) (Options, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	var out, errb bytes.Buffer
	return Options{
		Args:      args,
		LogPath:   filepath.Join(root, "agent1.log"),
		Root:      root,
		HeaderEnv: []string{"STAGE"},
		Stdout:    &out,
		Stderr:    &errb,
	}, &out, &errb
}

func TestRunRecordsStepAndPreservesExitCode(t *testing.T) {
	t.Setenv("STAGE", "dev")
	o, out, _ := newOpts(t, "bash", "-c", `printf '\033[32mgreen\033[0m\n'; echo err >&2; exit 3`)
	o.Label = "colour test"
	o.Lane, o.Lanes = 1, 2
	res, err := Run(o)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("exit = %d", res.ExitCode)
	}
	if !strings.Contains(out.String(), "\x1b[32mgreen") {
		t.Fatalf("terminal output should keep colour: %q", out.String())
	}
	data, _ := os.ReadFile(o.LogPath)
	log := string(data)
	for _, want := range []string{"=== STEP ", "colour test", "$ bash -c ", "env: STAGE=dev", "--- output\n", "green\n", "err\n", "--- exit 3 (", "FAIL  colour test (exit 3)"} {
		if !strings.Contains(log, want) {
			t.Errorf("log missing %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "\x1b") {
		t.Errorf("log contains ANSI escapes:\n%q", log)
	}
	st, _ := status.Load(o.Root)
	l := st.Get(1)
	if l.Fail != 1 || len(l.FailedSteps) != 1 || l.CurrentStep != "colour test" {
		t.Fatalf("status lane: %+v", l)
	}
}

func TestRunNewTruncatesAndSnapshotSaves(t *testing.T) {
	o, _, _ := newOpts(t, "echo", "state-v1")
	os.WriteFile(o.LogPath, []byte("old content\n"), 0o644)
	o.New, o.Snapshot, o.Label, o.Lane, o.Lanes = true, true, "current state", 1, 1
	res, err := Run(o)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	data, _ := os.ReadFile(o.LogPath)
	if strings.Contains(string(data), "old content") {
		t.Fatal("--new did not truncate")
	}
	if !strings.Contains(string(data), "PASS  snapshot: current state") {
		t.Fatalf("log:\n%s", data)
	}
	snap, err := os.ReadFile(res.Saved)
	if err != nil || string(snap) != "state-v1\n" {
		t.Fatalf("snapshot %s: %q %v", res.Saved, snap, err)
	}
}

func TestRunMissingCommand(t *testing.T) {
	o, _, _ := newOpts(t, "definitely-not-a-command-xyz")
	o.Label = "missing"
	res, _ := Run(o)
	if res.ExitCode != 127 {
		t.Fatalf("exit = %d", res.ExitCode)
	}
}
