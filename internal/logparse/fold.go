package logparse

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/gates-brightly/swimlane/internal/ui"
)

// Folding repeated output, for swim log and swim report: a polling loop's
// "waiting…" fifty times, or a progress bar redrawn as it counts, is one
// line and a count, not fifty. The log file keeps every line (swim log
// --raw shows it as it is).
//
// A run of the same output line, three or more times, folds to the line
// once, then "… N more times". Lines that differ only in their numbers
// never fold: "host-3: 500" among "host-N: 200" lines, or "deleted 9124
// rows" among "deleted 0 rows", is the line a reader needs.
//
// Each output line is cleaned first: of a line redrawn with carriage
// returns (a progress bar, a spinner) only what the terminal ended up
// showing is kept, and escape sequences and spinner frames go. A line left
// empty by that (a spinner, alone) is dropped.

// A fold takes this many lines in a run, at least.
const foldSame = 3

// FoldNote starts the line that stands for folded output: indented as the
// step's lines are, but not command output (no "| ").
const FoldNote = Indent + "… "

// FoldRepeats returns lines with repeated output folded.
func FoldRepeats(lines []string) []string {
	out := make([]string, 0, len(lines))
	var run []string // the current run of one output line, cleaned
	flush := func() {
		out = append(out, foldRun(run)...)
		run = run[:0]
	}
	for _, l := range lines {
		body, ok := strings.CutPrefix(l, OutPrefix)
		if !ok {
			flush()
			out = append(out, l)
			continue
		}
		body, keep := CleanOutput(body)
		if !keep {
			continue
		}
		if len(run) > 0 && body != run[0] {
			flush()
		}
		run = append(run, body)
	}
	flush()
	return out
}

// foldRun renders one run of the same output line.
func foldRun(run []string) []string {
	n := len(run)
	if n >= foldSame {
		return []string{OutPrefix + run[0], fmt.Sprintf("%s%d more times", FoldNote, n-1)}
	}
	lines := make([]string, n)
	for i, b := range run {
		lines[i] = OutPrefix + b
	}
	return lines
}

// CleanOutput is a line of command output as a terminal would have left
// it: after its last carriage return, without escape sequences or spinner
// frames. keep is false for a line with something on it that cleaning
// emptied (a spinner frame, alone); a line that was blank stays.
func CleanOutput(s string) (clean string, keep bool) {
	if strings.TrimSpace(s) == "" {
		return s, true
	}
	if i := strings.LastIndexByte(strings.TrimRight(s, "\r"), '\r'); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimRight(ui.SafeField(s), " ")
	if t := strings.TrimLeftFunc(s, func(r rune) bool { return spinner(r) || unicode.IsSpace(r) }); t != strings.TrimLeftFunc(s, unicode.IsSpace) {
		s = t // a spinner frame led the line: it goes, with the space after it
	}
	return s, s != ""
}

// spinner reports whether r is a spinner frame's character: the braille
// dots most spinners draw with.
func spinner(r rune) bool { return r >= 0x2800 && r <= 0x28ff }
