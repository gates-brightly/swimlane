package ui

import (
	"strings"
	"unicode/utf8"
)

// Safe makes text a lane supplied (a guard's description, evidence, a
// step label, a status message, log lines) harmless to show on a
// terminal: a lane could otherwise use escape sequences, carriage returns
// or backspaces to hide or rewrite what the operator reads, or bidi
// controls to make it read in a different order. It removes whole escape
// sequences (CSI, OSC including hyperlinks, DCS and the rest), C0 controls
// except tab (so newlines too: Safe text is one line), DEL, C1 controls
// and bidi controls, and replaces invalid UTF-8 with U+FFFD. Printable
// text, tabs and multi-byte UTF-8 are kept.
func Safe(s string) string { return sanitize(s, false) }

// SafeKeepSGR is Safe, but keeps SGR sequences (colours and bold), for a
// lane's output in the live view.
func SafeKeepSGR(s string) string { return sanitize(s, true) }

// SafeLines is Safe for multi-line text: each line made safe, the
// newlines kept (a \r\n ending becomes \n).
func SafeLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = Safe(l)
	}
	return strings.Join(lines, "\n")
}

// SafeField is Safe with tabs as spaces, for a one-line field such as a
// step label that lands in tab-separated or column-aligned records.
func SafeField(s string) string { return strings.ReplaceAll(Safe(s), "\t", " ") }

func sanitize(s string, keepSGR bool) string {
	if plain(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := EscEnd(s, i)
			if keepSGR && IsSGR(s[i:j]) {
				b.WriteString(s[i:j])
			}
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if r == utf8.RuneError && size == 1 {
			b.WriteRune(utf8.RuneError)
			continue
		}
		if unsafeRune(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// plain reports whether s is printable ASCII and tabs only: nothing to do.
func plain(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 && c != '\t' || c >= 0x7f {
			return false
		}
	}
	return true
}

func unsafeRune(r rune) bool {
	switch {
	case r == '\t':
		return false
	case r < 0x20, r >= 0x7f && r <= 0x9f: // C0, DEL, C1
		return true
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0x200e, r == 0x200f, r == 0x061c: // bidi
		return true
	}
	return false
}

// IsSGR reports whether seq is exactly one SGR sequence: ESC [ params m.
func IsSGR(seq string) bool {
	if len(seq) < 3 || !strings.HasPrefix(seq, "\x1b[") || seq[len(seq)-1] != 'm' {
		return false
	}
	for i := 2; i < len(seq)-1; i++ {
		if c := seq[i]; !(c >= '0' && c <= '9' || c == ';' || c == ':') {
			return false
		}
	}
	return true
}

// EscEnd returns the index just past the escape sequence starting at s[i]
// (an ESC). An unterminated sequence runs to the end of s.
func EscEnd(s string, i int) int {
	if i+1 >= len(s) {
		return len(s)
	}
	switch s[i+1] {
	case '[': // CSI: parameters, then a final byte 0x40-0x7e
		for j := i + 2; j < len(s); j++ {
			if s[j] >= 0x40 && s[j] <= 0x7e {
				return j + 1
			}
		}
		return len(s)
	case ']', 'P', 'X', '^', '_': // OSC, DCS, SOS, PM, APC: end with BEL or ESC \
		for j := i + 2; j < len(s); j++ {
			if s[j] == 0x07 {
				return j + 1
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
		}
		return len(s)
	}
	j := i + 1
	for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f { // intermediates, as in ESC ( B
		j++
	}
	if j < len(s) {
		j++
	}
	return j
}
