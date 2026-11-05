// Package ui holds the terminal colour palette shared by the live view,
// the step wrapper and `swim status`. Colour is only ever written to the
// terminal; logs and status.yml stay plain.
package ui

import (
	"os"
	"strconv"
	"strings"
	"sync"

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

// Lane colours: each lane keeps one colour for its [N], its panel row and
// its section headers, cycling when there are more lanes than colours.
// The first six are the terminal theme's own colours (as swim always used);
// after them, a terminal with 256 colours gets ten more hues, so sixteen
// lanes run side by side before one repeats, and any other gets the
// theme's bright colours (eleven in all).
var (
	laneColors16 = []string{Cyan, Mag, Blue, Yellow, Green, "\x1b[91m",
		"\x1b[96m", "\x1b[95m", "\x1b[94m", "\x1b[93m", "\x1b[92m"}
	laneColors256 = []string{Cyan, Mag, Blue, Yellow, Green, "\x1b[91m",
		c256(208), // orange
		c256(205), // pink
		c256(112), // lime
		c256(37),  // teal
		c256(141), // purple
		c256(75),  // sky
		c256(178), // gold
		c256(209), // salmon
		c256(177), // violet
		c256(72),  // sea green
	}
)

func c256(n int) string { return "\x1b[38;5;" + strconv.Itoa(n) + "m" }

// LaneColor returns the fixed colour for lane n (1-based).
func LaneColor(n int) string {
	return laneColor(n, Colors256())
}

func laneColor(n int, wide bool) string {
	if n < 1 {
		return ""
	}
	colors := laneColors16
	if wide {
		colors = laneColors256
	}
	return colors[(n-1)%len(colors)]
}

var colors256 struct {
	once sync.Once
	on   bool
}

// Colors256 reports whether the terminal shows 256 colours, for the lane
// palette: SWIM_COLORS=256 or 16 says so; else COLORTERM (truecolor,
// 24bit), a TERM naming 256color, or a terminal known to (Ghostty, iTerm,
// WezTerm, VS Code, Apple Terminal). Lanes inherit the same environment, so
// a lane's own output and the view agree on its colour. Read once.
func Colors256() bool {
	colors256.once.Do(func() { colors256.on = detect256(os.Getenv) })
	return colors256.on
}

func detect256(getenv func(string) string) bool {
	switch getenv("SWIM_COLORS") {
	case "256":
		return true
	case "16":
		return false
	}
	switch strings.ToLower(getenv("COLORTERM")) {
	case "truecolor", "24bit":
		return true
	}
	term := getenv("TERM")
	if strings.Contains(term, "256color") || strings.Contains(term, "ghostty") || strings.Contains(term, "kitty") {
		return true
	}
	switch getenv("TERM_PROGRAM") {
	case "ghostty", "iTerm.app", "WezTerm", "vscode", "Apple_Terminal":
		return true
	}
	return false
}

// Job paints a lane or job id ("1", "1.4", "1.5.9") a part at a time, each
// number in its own lane colour: in 1.4 the 1 is lane 1's colour and the
// 4 lane 4's. extra (a style such as Bold) applies to every part.
func (p Painter) Job(id, extra string) string {
	if !p.On {
		return id
	}
	parts := strings.Split(id, ".")
	for i, part := range parts {
		n, _ := strconv.Atoi(part)
		parts[i] = p.Paint(LaneColor(n)+extra, part)
	}
	return strings.Join(parts, p.Paint(LaneColor(firstNum(id))+extra, "."))
}

func firstNum(id string) int {
	head, _, _ := strings.Cut(id, ".")
	n, _ := strconv.Atoi(head)
	return n
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
	case strings.HasPrefix(s, "fail"), strings.HasPrefix(s, "interrupted"), strings.HasPrefix(s, "stop"), strings.HasPrefix(s, "blocked"):
		return Red
	case strings.HasPrefix(s, "skip"), strings.HasPrefix(s, "waiting"), strings.HasPrefix(s, "running?"):
		return Yellow
	case strings.HasPrefix(s, "running"):
		return Cyan
	case strings.HasPrefix(s, "idle"):
		return Dim
	}
	return ""
}
