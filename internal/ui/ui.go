// Package ui holds the terminal colour palette shared by the live view,
// the step wrapper and `swim status`. Colour is only ever written to the
// terminal; logs and status.yml stay plain.
package ui

import (
	"os"
	"strings"

	"golang.org/x/term"
)

const (
	Reset  = "\x1b[0m"
	Bold   = "\x1b[1m"
	Dim    = "\x1b[2m"
	Red    = "\x1b[31m"
	Green  = "\x1b[32m"
	Yellow = "\x1b[33m"
	Blue   = "\x1b[34m"
	Mag    = "\x1b[35m"
	Cyan   = "\x1b[36m"
)

// laneColors cycles so each lane keeps one colour for its prefix and panel row.
var laneColors = []string{Cyan, Mag, Blue, Yellow, Green, "\x1b[91m"}

// LaneColor returns the fixed colour for lane n (1-based).
func LaneColor(n int) string {
	if n < 1 {
		return ""
	}
	return laneColors[(n-1)%len(laneColors)]
}

// IsTTY reports whether f is a terminal.
func IsTTY(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// ColorEnabled decides whether output to f may be coloured. SWIM_COLOR=1
// forces colour (the launcher sets it for lanes whose output it relays to a
// terminal through pipes); NO_COLOR or SWIM_COLOR=0 disables it.
func ColorEnabled(f *os.File) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("SWIM_COLOR") == "0" {
		return false
	}
	if os.Getenv("SWIM_COLOR") == "1" {
		return true
	}
	return IsTTY(f)
}

// Painter applies colour only when enabled.
type Painter struct{ On bool }

func (p Painter) Paint(color, s string) string {
	if !p.On || color == "" || s == "" {
		return s
	}
	return color + s + Reset
}

// StateColor maps a result or lane state word to its colour.
func StateColor(state string) string {
	s := strings.ToLower(state)
	switch {
	case strings.HasPrefix(s, "pass"), strings.HasPrefix(s, "approved"):
		return Green
	case strings.HasPrefix(s, "fail"), strings.HasPrefix(s, "interrupted"), strings.HasPrefix(s, "stop"):
		return Red
	case strings.HasPrefix(s, "skip"), strings.HasPrefix(s, "drift"), strings.HasPrefix(s, "waiting"), strings.HasPrefix(s, "running?"):
		return Yellow
	case strings.HasPrefix(s, "running"):
		return Cyan
	case strings.HasPrefix(s, "idle"):
		return Dim
	}
	return ""
}
