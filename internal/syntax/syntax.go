// Package syntax versions the text formats swim reads and writes: lane
// scripts (lane.N.sh) and lane logs (.swim/logs/agentN*.log).
//
// Each file says which syntax it uses:
//
//	lane script, near the top:  # swim: syntax 2
//	lane log, first line:       # swim lane log | syntax 2 | swim 3
//
// A file without the line is syntax 1 (written before versioning). swim
// reads and migrates older syntaxes, and refuses files newer than Current,
// asking for a swim update.
package syntax

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/gates-brightly/swimlane/internal/version"
)

// Current is the syntax this swim writes, and the newest it reads.
//
//	1  before versioning: no syntax line, no stages, === STEP log framing
//	2  header metadata and fixed stages in lane scripts; grouped, fenced logs
const Current = 2

var (
	scriptRE = regexp.MustCompile(`^#\s*swim:\s*syntax\s+(\d+)\s*$`)
	logRE    = regexp.MustCompile(`^#\s*swim lane log\s*\|\s*syntax\s+(\d+)`)
)

// ScriptLine is the line that marks a lane script's syntax.
func ScriptLine() string { return fmt.Sprintf("# swim: syntax %d", Current) }

// LogHeader is the first line of a lane log for lane n.
func LogHeader(n int) string {
	return fmt.Sprintf("# swim lane log | syntax %d | swim %d", Current, n)
}

// OfScript returns the syntax a lane script declares in its first few lines
// (1 when it declares none).
func OfScript(src string) int {
	sc := bufio.NewScanner(strings.NewReader(src))
	for i := 0; i < 5 && sc.Scan(); i++ {
		if m := scriptRE.FindStringSubmatch(strings.TrimSpace(sc.Text())); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n
		}
	}
	return 1
}

// OfLog returns the syntax of a lane log from its first line (1 when it has
// no header, including an empty log).
func OfLog(r io.Reader) int {
	line, _ := bufio.NewReader(r).ReadString('\n')
	if m := logRE.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
		n, _ := strconv.Atoi(m[1])
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
		"Update swim, then run again:\n  %s\n  (or, in a checkout of swimlane: git pull && make install)\n"+
		"Check with: swim --version",
		e.File, e.Got, version.String(), Current, version.InstallCmd)
}

// Check returns ErrTooNew if got is newer than Current.
func Check(file string, got int) error {
	if got > Current {
		return ErrTooNew{File: file, Got: got}
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
