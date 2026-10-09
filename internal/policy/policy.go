// Package policy holds swim's hard rules about what lanes may run. Today
// that is one rule: swim never pushes, commits or pulls. A lane command that
// contains a blocked pattern is refused (BLOCKED) and its round stops.
package policy

import (
	"regexp"
	"strings"
)

// Builtins are always blocked; config can add patterns but never remove these.
var Builtins = []string{"git push", "git commit", "git pull"}

// ExitBlocked is the exit code of a refused command (swim step and the git
// shim). It is distinct from 126/127 and signal codes.
const ExitBlocked = 87

var spaces = regexp.MustCompile(`\s+`)

// Normalize collapses runs of whitespace to one space.
func Normalize(s string) string { return strings.TrimSpace(spaces.ReplaceAllString(s, " ")) }

// Patterns returns the built-ins plus extra, normalised and de-duplicated.
func Patterns(extra []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range append(append([]string(nil), Builtins...), extra...) {
		p = Normalize(p)
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// IsBuiltin reports whether p is one of the built-in patterns.
func IsBuiltin(p string) bool {
	for _, b := range Builtins {
		if Normalize(p) == b {
			return true
		}
	}
	return false
}

// Match returns every pattern found in cmd (case-sensitive substrings, after
// collapsing whitespace).
func Match(cmd string, patterns []string) []string {
	cmd = Normalize(cmd)
	var hits []string
	for _, p := range patterns {
		if p != "" && strings.Contains(cmd, p) {
			hits = append(hits, p)
		}
	}
	return hits
}

// Hit is a blocked pattern found in a lane script.
type Hit struct {
	Line    int // 1-based
	Pattern string
	Text    string // the line, trimmed
}

// ScanScript reports blocked patterns in a lane script's code. Comment lines
// (including the header) are skipped; a trailing comment on a code line is
// ignored only when it starts with " #".
func ScanScript(src string, patterns []string) []Hit {
	var hits []Hit
	for i, raw := range strings.Split(src, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		code := line
		if j := strings.Index(code, " #"); j >= 0 {
			code = code[:j]
		}
		for _, p := range Match(code, patterns) {
			hits = append(hits, Hit{Line: i + 1, Pattern: p, Text: line})
		}
	}
	return hits
}
