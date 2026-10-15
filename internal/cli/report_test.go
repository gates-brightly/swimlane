package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/earmark"
	"github.com/gates-brightly/swimlane/internal/history"
)

func writeLog(t *testing.T, path string, lines ...string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

const (
	roundA = "== ROUND 2026-10-15T10:00:00Z  job=aaaa  first"
	roundB = "== ROUND 2026-10-15T11:00:00Z  job=bbbb  second"
)

// A lane's delta: the rest of the round the earmark is in, then later
// rounds (an archived round included); a rewritten or missing round falls
// back to the rounds since the earmark, saying why.
func TestStreamDelta(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".swim", "logs")
	writeLog(t, filepath.Join(dir, "agent1.prev-old.log"), roundA, "  PASS  a", "  PASS  b", "")
	writeLog(t, filepath.Join(dir, "agent1.log"), roundB, "  PASS  c")
	writeLog(t, filepath.Join(dir, "agent1.x.log"), roundB, "  PASS  cell")
	ss := laneStreams(root, 1)
	if len(ss) != 2 || ss[0].key != "1" || ss[1].key != "1[x]" || len(ss[0].files) != 2 {
		t.Fatalf("streams: %+v %+v", ss[0], ss[len(ss)-1])
	}
	s := ss[0]

	// Mid-round in the archive: the rest of it, then the new round whole.
	pos := &earmark.Pos{Round: roundA, Lines: 2, SHA: lineSHA("  PASS  a")}
	out, warn, end := s.delta(pos, "2026-10-15T10:30:00Z")
	if warn != "" || len(out) != 2 || out[0].from != 2 || out[1].from != 0 || out[1].round.lines[0] != roundB {
		t.Fatalf("delta: %+v %q", out, warn)
	}
	if end.Round != roundB || end.Lines != 2 || end.SHA != lineSHA("  PASS  c") {
		t.Errorf("end: %+v", end)
	}
	// At the end: nothing.
	if out, warn, _ := s.delta(&end, "2026-10-15T11:30:00Z"); len(out) != 0 || warn != "" {
		t.Errorf("caught up: %+v %q", out, warn)
	}
	// Rewritten: the line it read no longer hashes the same.
	bad := *pos
	bad.SHA = lineSHA("something else")
	if out, warn, _ := s.delta(&bad, "2026-10-15T10:30:00Z"); !strings.Contains(warn, "rewritten") || len(out) != 1 || out[0].round.lines[0] != roundB {
		t.Errorf("rewritten: %+v %q", out, warn)
	}
	// Gone (pruned): rounds since the earmark.
	gone := &earmark.Pos{Round: "== ROUND 2026-10-15T09:00:00Z  job=zzzz  pruned", Lines: 3, SHA: "x"}
	if out, warn, _ := s.delta(gone, "2026-10-15T10:30:00Z"); !strings.Contains(warn, "is gone") || len(out) != 1 {
		t.Errorf("gone: %+v %q", out, warn)
	}
	// No earmark: the current log's rounds (the work in progress).
	if out, _, _ := s.delta(nil, ""); len(out) != 1 || out[0].round.lines[0] != roundB {
		t.Errorf("no earmark: %+v", out)
	}
}

// .swim.log is read by offset; a replaced file falls back to timestamps.
func TestHistoryDelta(t *testing.T) {
	root := t.TempDir()
	path := history.Path(root)
	l1 := "2026-10-15T10:00:00Z  zach      pass         swim 1"
	l2 := "2026-10-15T11:00:00Z  zach      fail         swim 2"
	writeLog(t, path, "# swim project log", l1)
	_, end, _ := historyDelta(root, nil, "")
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(l2 + "\n" + "2026-10-15T12:00:00Z  half a li")
	f.Close()
	lines, end2, warn := historyDelta(root, &end, "2026-10-15T10:30:00Z")
	if warn != "" || len(lines) != 1 || lines[0] != l2 {
		t.Fatalf("after: %q %q", lines, warn)
	}
	if end2.SHA != lineSHA(l2) {
		t.Errorf("end includes a half-written line: %+v", end2)
	}
	writeLog(t, path, "# swim project log", "2026-10-15T09:00:00Z  x  replaced", l2)
	lines, _, warn = historyDelta(root, &end2, "2026-10-15T10:30:00Z")
	if !strings.Contains(warn, "replaced or edited") || len(lines) != 1 || lines[0] != l2 {
		t.Errorf("replaced: %q %q", lines, warn)
	}
}

func TestEarmarkName(t *testing.T) {
	t.Setenv(earmark.SessionEnv, "")
	if n, _ := earmark.Name(""); n != earmark.Default {
		t.Errorf("default: %q", n)
	}
	t.Setenv(earmark.SessionEnv, "planner-7f2c")
	if n, _ := earmark.Name(""); n != "planner-7f2c" {
		t.Errorf("session: %q", n)
	}
	if n, _ := earmark.Name("mine"); n != "mine" {
		t.Errorf("flag: %q", n)
	}
	if _, err := earmark.Name("../x"); err == nil {
		t.Error("a path as a name")
	}
}

// Days a recurring schedule missed (no worker) are superseded by the
// latest; none after a day it handled.
func TestMissedDays(t *testing.T) {
	r := &recurring{First: "2026-10-10", Last: "2026-10-12"}
	if got := strings.Join(missedDays(r, "2026-10-15", time.UTC), " "); got != "2026-10-13 2026-10-14" {
		t.Errorf("missed: %q", got)
	}
	r.Last = ""
	if got := strings.Join(missedDays(r, "2026-10-11", time.UTC), " "); got != "2026-10-10" {
		t.Errorf("from first: %q", got)
	}
	r.Last = "2026-10-14"
	if got := missedDays(r, "2026-10-15", time.UTC); len(got) != 0 {
		t.Errorf("none: %q", got)
	}
}
