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
// A run of consecutive output lines folds when
//   - they are the same line, three or more times: the line once, then
//     "… N more times"; or
//   - they differ only in their numbers (a percentage, a counter, a
//     timestamp), four or more: the first, "… N more like these", the last.
//
// Each output line is cleaned first: of a line redrawn with carriage
// returns (a progress bar, a spinner) only what the terminal ended up
// showing is kept, and escape sequences and spinner frames go. A line left
// empty by that (a spinner, alone) is dropped.

// Folds take this many lines in a run, at least.
const (
	foldSame = 3
	foldLike = 4
)

// FoldNote starts the line that stands for folded output: indented as the
// step's lines are, but not command output (no "| ").
const FoldNote = Indent + "… "

// FoldRepeats returns lines with repeated output folded.
func FoldRepeats(lines []string) []string {
	out := make([]string, 0, len(lines))
	var run []string // the current run's output, cleaned
	runKey := ""
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
		key := likeKey(body)
		if len(run) > 0 && key != runKey {
			flush()
		}
		run, runKey = append(run, body), key
	}
	flush()
	return out
}

// foldRun renders one run of output lines that share a likeKey.
func foldRun(run []string) []string {
	n := len(run)
	if n == 0 {
		return nil
	}
	same := true
	for _, b := range run[1:] {
		same = same && b == run[0]
	}
	switch {
	case same && n >= foldSame:
		return []string{OutPrefix + run[0], fmt.Sprintf("%s%d more times", FoldNote, n-1)}
	case !same && n >= foldLike:
		return []string{OutPrefix + run[0], fmt.Sprintf("%s%d more like these", FoldNote, n-2), OutPrefix + run[n-1]}
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

// likeKey is a line with its numbers and spinner frames taken out: lines
// that differ only in those share it.
func likeKey(s string) string {
	var b strings.Builder
	inNum := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			if !inNum {
				b.WriteByte('#')
			}
			inNum = true
			continue
		case spinner(r):
			continue
		}
		inNum = false
		b.WriteRune(r)
	}
	return b.String()
}
