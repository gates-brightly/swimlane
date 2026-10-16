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
	"strconv"
	"strings"

	"github.com/gates-brightly/swimlane/internal/version"
)

// Current is the syntax this swim writes, and the newest it reads.
//
//	1  before versioning: no syntax line, no stages, === STEP log framing
//	2  header metadata and fixed stages in lane scripts; grouped, fenced logs
const Current = 2

// Script is the newest lane script syntax this swim reads. Script syntax 3
// adds outcome handlers (on_success, on_failure, on_exit) and Fail: hard: a
// script that uses them declares it, so an older swim, which would skip its
// rollback, refuses it. Syntax 4 adds modules (# Modules:, mod, mod_env):
// an older swim would run the round with every module call failing outside
// a step. Syntax 2 and 3 scripts are read as they are; nothing is migrated,
// and logs stay at Current.
//
//	3  outcome handlers and Fail: (lane scripts only)
//	4  modules: # Modules:, mod and mod_env (lane scripts only)
const Script = 4

// State is the syntax of swim's state files: .swim/status.yml, the chain
// (.swim/chain.jsonl), queued batches and signed approvals. Unlike lane
// scripts and logs they don't say which syntax they use, so a repo's
// .swim.lock pins it: bump it when they change incompatibly.
//
//	1  the state files as of swim 7.20261013
//	2  execution manifests: chain entries, signed approvals and queued
//	   batches name manifest_sha256 (a swim reading state 1 can't verify
//	   them), approve entries the lookup.yml they were checked under
//	   (lookup_sha256), status records it, and runs stage under
//	   .swim/runs/<run>/
const State = 2

// The lock (version.Check) compares a repo's pinned syntaxes with these.
func init() { version.Current = version.Syntaxes{Script: Script, Log: Current, State: State} }

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
	// "# swim lane log | syntax N | swim M"
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "#")
	if !ok {
		return 1
	}
	parts := strings.Split(rest, "|")
	if len(parts) < 2 || strings.Join(strings.Fields(parts[0]), " ") != "swim lane log" {
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
	Max  int // the newest syntax this swim reads for that kind of file (default Current)
}

func (e ErrTooNew) Error() string {
	return fmt.Sprintf("%s uses swim syntax %d, but this swim (%s) reads up to syntax %d.\n\n"+
		"Update swim, then run again:\n  %s\n  (or, in a checkout of swimlane: git pull && make install)\n"+
		"Check with: swim --version",
		e.File, e.Got, version.String(), max(e.Max, Current), version.InstallCmd)
}

// Check returns ErrTooNew if got is newer than Current.
func Check(file string, got int) error {
	if got > Current {
		return ErrTooNew{File: file, Got: got}
	}
	return nil
}

// CheckScript returns ErrTooNew if a lane script's syntax is newer than
// Script.
func CheckScript(file string, got int) error {
	if got > Script {
		return ErrTooNew{File: file, Got: got, Max: Script}
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
