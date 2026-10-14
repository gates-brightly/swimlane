package step

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/logparse"
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
		Args:    args,
		LogPath: filepath.Join(root, "agent1.log"),
		Root:    root,
		Stdout:  &out,
		Stderr:  &errb,
	}, &out, &errb
}

func TestRunWritesV2BlockAndPreservesExitCode(t *testing.T) {
	o, out, _ := newOpts(t, "bash", "-c", `printf '\033[32mgreen\033[0m\n'; echo err >&2; echo 'FAIL  fake (exit 1)'; exit 3`)
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
	if !strings.HasPrefix(log, "# swim lane log | syntax 2 | swim 1\n") {
		t.Fatalf("no header:\n%s", log)
	}
	for _, want := range []string{"  FAIL  colour test (exit 3)", "        $ bash -c ", "        | green\n", "        | err\n", "        | FAIL  fake (exit 1)\n"} {
		if !strings.Contains(log, want) {
			t.Errorf("log missing %q:\n%s", want, log)
		}
	}
	// The result line comes before the command and output.
	if strings.Index(log, "  FAIL  colour test") > strings.Index(log, "        $ bash") {
		t.Errorf("result line should come first:\n%s", log)
	}
	if strings.Contains(log, "\x1b") {
		t.Errorf("log contains ANSI escapes:\n%q", log)
	}
	if _, err := os.Stat(SpoolPath(o.LogPath)); err == nil {
		t.Error("spool left behind")
	}
	r, _ := logparse.ParseFile(o.LogPath)
	if r.Fail != 1 || r.Steps != 1 {
		t.Fatalf("parsed: %+v (command output must not count)", r)
	}
	st, _ := status.Load(o.Root)
	if l := st.Get(1); l.Fail != 1 || len(l.FailedSteps) != 1 || l.CurrentStep != "colour test" {
		t.Fatalf("status lane: %+v", l)
	}
}

func TestRunNewTruncatesAndSnapshotSaves(t *testing.T) {
	o, _, _ := newOpts(t, "echo", "state-v1")
	os.WriteFile(o.LogPath, []byte("# swim lane log | syntax 2 | swim 1\nold content\n"), 0o644)
	o.New, o.Snapshot, o.Label, o.Lane, o.Lanes = true, true, "current state", 1, 1
	res, err := Run(o)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	data, _ := os.ReadFile(o.LogPath)
	if strings.Contains(string(data), "old content") {
		t.Fatal("--new did not start a fresh log")
	}
	if !strings.Contains(string(data), "  PASS  snapshot: current state") || !strings.Contains(string(data), "        saved: .swim/snapshots/lane1-") {
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

func TestRunStopsAtDeadline(t *testing.T) {
	o, _, _ := newOpts(t, "sleep", "30")
	o.Label, o.TimeoutText = "slow", "1s"
	o.Deadline = time.Now().Add(500 * time.Millisecond)
	start := time.Now()
	res, _ := Run(o)
	if !res.TimedOut || res.ExitCode != 124 || time.Since(start) > 10*time.Second {
		t.Fatalf("res=%+v after %s", res, time.Since(start))
	}
	data, _ := os.ReadFile(o.LogPath)
	if !strings.Contains(string(data), "  FAIL  slow (timeout: Timeout 1s reached)") || !strings.Contains(string(data), "time limit (Timeout: 1s) ran out") {
		t.Fatalf("log:\n%s", data)
	}
	// Past the deadline a step doesn't start at all.
	o.Label, o.Args = "late", []string{"touch", filepath.Join(o.Root, "ran")}
	res, _ = Run(o)
	if !res.TimedOut || res.ExitCode != 124 {
		t.Fatalf("late: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(o.Root, "ran")); err == nil {
		t.Fatal("a step ran past the deadline")
	}
}

func TestRecoverSpool(t *testing.T) {
	o, _, _ := newOpts(t, "true")
	logparse.EnsureV2(o.LogPath, 1)
	os.WriteFile(SpoolPath(o.LogPath), []byte(spoolMagic+"\tkilled step\t12:00:00\tdo thing\npartial line\n"), 0o644)
	RecoverSpool(o.LogPath)
	data, _ := os.ReadFile(o.LogPath)
	for _, want := range []string{"  FAIL  killed step (cut off: swim step was killed, output recovered)", "        $ do thing", "        | partial line"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %q:\n%s", want, data)
		}
	}
	if _, err := os.Stat(SpoolPath(o.LogPath)); err == nil {
		t.Error("spool not removed")
	}
}

func TestReadSpool(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "agent1.log")
	if _, ok := ReadSpool(logPath); ok {
		t.Fatal("no spool: nothing running")
	}
	clock := time.Now().Add(-3 * time.Second).Format("15:04:05")
	os.WriteFile(SpoolPath(logPath), []byte(spoolMagic+"\tdeploy\t"+clock+"\tdeploy --all\nline one\n"+spoolMark+"attempt 1/2\nhalf a li"), 0o644)
	sp, ok := ReadSpool(logPath)
	if !ok || sp.Label != "deploy" || sp.Cmd != "deploy --all" || sp.Partial != "half a li" || len(sp.Output) != 2 || sp.Output[1] != "-- attempt 1/2" {
		t.Fatalf("spool: %+v", sp)
	}
	if e := sp.Elapsed(); e < 2*time.Second || e > 10*time.Second {
		t.Errorf("elapsed %s", e)
	}
	b := sp.Block("in progress")
	want := []string{"        $ deploy --all", "        | line one", "        -- attempt 1/2", "        | half a li"}
	if !strings.HasPrefix(b[0], "  RUNNING  deploy (in progress)") || strings.Join(b[1:], "\n") != strings.Join(want, "\n") {
		t.Fatalf("block:\n%s", strings.Join(b, "\n"))
	}
	// RUNNING never counts as a result if a log ever held it.
	r, _ := logparse.Parse(strings.NewReader("# swim lane log | syntax 2 | swim 1\n\n== ROUND 2026-10-13T00:00:00Z  job=j  t\n" + strings.Join(b, "\n") + "\n"))
	if r.Steps != 0 || len(r.Results) != 0 {
		t.Errorf("RUNNING was parsed as a result: %+v", r.Results)
	}
}

// RecordStep writes a finished step (a whole program, an SDK's in-process
// step) as a block the log parser reads back.
func TestRecordStep(t *testing.T) {
	log := filepath.Join(t.TempDir(), "agent1.log")
	os.WriteFile(log, []byte("# swim lane log | syntax 2 | swim 1\n\n== ROUND 2026-10-14T00:00:00Z  job=j  r\n"), 0o644)
	line, err := RecordStep(log, logparse.Pass, "native check", "in-process", "(in-process)", time.Now(), 1500*time.Millisecond, strings.NewReader("items: 0\nok\n"))
	if err != nil || line != "PASS  native check (in-process)" {
		t.Fatalf("%q %v", line, err)
	}
	RecordOutput(log, "lane.1.py", "python3 ./lane.1.py", time.Now(), time.Second, 3, strings.NewReader("boom\n"))
	r, _ := logparse.ParseFile(log)
	if len(r.Results) != 2 || r.Results[0].Cmd != "(in-process)" || strings.Join(r.Results[0].Output, "|") != "items: 0|ok" ||
		r.Results[1].Kind != logparse.Fail || r.Results[1].Detail != "exit 3" || r.Pass != 1 || r.Fail != 1 {
		t.Errorf("parsed: %+v", r.Results)
	}
}
