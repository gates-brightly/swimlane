// Package syntax is the one version of the text formats swim reads and
// writes: lane scripts say "# swim: syntax N", lane logs start with
// "# swim log | syntax N | swim <lane>". swim refuses only a newer file.
package syntax

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gates-brightly/swimlane/internal/version"
)

// Version is the syntax this swim writes, and the newest it reads.
const Version = 4

// Reads is the newest syntax this swim reads: Version, or what a test
// build stands in for (version.TestSyntax).
var Reads = Version

func init() {
	if n, err := strconv.Atoi(version.TestSyntax); err == nil && n > 0 {
		Reads = n
	}
}

// scriptSyntax reads a script's syntax line, "# swim: syntax N" (spaces
// free around each part), from a trimmed line.
func scriptSyntax(line string) (int, bool) {
	rest, ok := strings.CutPrefix(line, "#")
	if !ok {
		return 0, false
	}
	rest, ok = strings.CutPrefix(strings.TrimLeft(rest, " \t"), "swim:")
	if !ok {
		return 0, false
	}
	f := strings.Fields(rest)
	if len(f) != 2 || f[0] != "syntax" {
		return 0, false
	}
	return number(f[1])
}

// number reads a run of ASCII digits.
func number(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// IsScriptLine reports whether a (trimmed) line is a script's syntax line.
func IsScriptLine(line string) bool {
	_, ok := scriptSyntax(line)
	return ok
}

// LogHeader is the first line of a lane log for lane n.
func LogHeader(n int) string {
	return fmt.Sprintf("# swim log | syntax %d | lane %d", Version, n)
}

// IsLogHeader reports whether line is a lane log's first line (the current
// "# swim log" or the older "# swim lane log").
func IsLogHeader(line string) bool {
	return strings.HasPrefix(line, "# swim log") || strings.HasPrefix(line, "# swim lane log")
}

// OfScript returns the syntax a lane script declares in its first few lines
// (1 when it declares none).
func OfScript(src string) int {
	sc := bufio.NewScanner(strings.NewReader(src))
	for i := 0; i < 5 && sc.Scan(); i++ {
		if n, ok := scriptSyntax(strings.TrimSpace(sc.Text())); ok {
			return n
		}
	}
	return 1
}

// OfLog returns the syntax of a lane log from its first line (1 when it has
// no header, including an empty log).
func OfLog(r io.Reader) int {
	line, _ := bufio.NewReader(r).ReadString('\n')
	// "# swim log | syntax N | swim M" (older logs: "# swim lane log")
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "#")
	if !ok {
		return 1
	}
	parts := strings.Split(rest, "|")
	if kind := strings.Join(strings.Fields(parts[0]), " "); len(parts) < 2 || kind != "swim log" && kind != "swim lane log" {
		return 1
	}
	f := strings.Fields(parts[1])
	if len(f) < 2 || f[0] != "syntax" {
		return 1
	}
	if n, ok := number(f[1]); ok {
		return n
	}
	return 1
}

// OfLogFile is OfLog for a path; a missing file is 0 (nothing to read).
func OfLogFile(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() == 0 {
		return 0
	}
	return OfLog(f)
}

// ErrTooNew is returned for a file written by a newer swim.
type ErrTooNew struct {
	File string
	Got  int
}

func (e ErrTooNew) Error() string {
	return fmt.Sprintf("%s uses swim syntax %d, but this swim (%s) reads up to syntax %d.\n\n"+
		"Update swim, then run again:\n  %s\n  (or, in a checkout of swimlane: git pull && just install)\n"+
		"Check with: swim --version",
		e.File, e.Got, version.String(), Reads, version.InstallCmd)
}

// Check returns ErrTooNew if got is newer than this swim reads.
func Check(file string, got int) error {
	if got > Reads {
		return ErrTooNew{File: file, Got: got}
	}
	return nil
}

// CheckRepo returns ErrTooNew for the first lane script (lane.N.sh) or
// lane log (.swim/logs/laneN*.log) under root that's newer than this swim
// reads.
func CheckRepo(root string) error {
	scripts, _ := filepath.Glob(filepath.Join(root, "lane.*.sh"))
	for _, p := range scripts {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if err := Check(filepath.Base(p), OfScript(string(data))); err != nil {
			return err
		}
	}
	logs, _ := filepath.Glob(filepath.Join(root, ".swim", "logs", "lane*.log"))
	for _, p := range logs {
		rel, _ := filepath.Rel(root, p)
		if err := Check(rel, OfLogFile(p)); err != nil {
			return err
		}
	}
	return nil
}

// Stages every job has, in order. Steps before the first `stage` line
// belong to Setup.
const (
	Setup    = "setup"
	Snapshot = "snapshot"
	CheckSt  = "check"
	Change   = "change"
	Verify   = "verify"
)

// Stages lists the standard stages in order (Setup excluded).
var Stages = []string{Snapshot, CheckSt, Change, Verify}

// StageIndex returns a stage's position (Setup is -1), or -2 if unknown.
func StageIndex(name string) int {
	if name == Setup {
		return -1
	}
	for i, s := range Stages {
		if s == name {
			return i
		}
	}
	return -2
}
