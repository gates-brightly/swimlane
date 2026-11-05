// Package why picks the one line of a failed step's output that best says
// why it failed, for the one-line views (.swim.log, swim status, bare swim,
// swim watch, notifications, the run summary): "--- FAIL: TestX:
// x_test.go:41: got 1, want 2" instead of only "(exit 1)".
package why

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/gates-brightly/swimlane/internal/ui"
)

// Max is how many characters a why keeps; longer ones end in "…".
const Max = 160

// markers are what a telling line contains, most telling first: the first
// line with an earlier marker wins over any line with a later one.
var markers = [][]string{
	{"panic:"},
	{"FAIL:"},
	{"fatal:", "Fatal"},
	{"error:", "Error:", "ERROR"},
}

// location is a source position, "file.go:12:" or "lane.1.sh:8:".
var location = regexp.MustCompile(`[\w./-]+\.\w+:\d+:`)

// lintLine is a swim lint finding: "lane.2.sh:4  error  ..." (or warn).
var lintLine = regexp.MustCompile(`^[\w./-]+:\d+  (error|warn) `)

// testLoc starts a go test failure's message: "x_test.go:41: ...".
var testLoc = regexp.MustCompile(`^[\w./-]+\.go:\d+:`)

// goSummary is e2etool gotest-report's "=== N failed tests": the failures
// it lists after it each come with their own output (go test -json prints a
// test's output before its --- FAIL line, so above it a location may be
// another test's).
var goSummary = regexp.MustCompile(`^=== \d+ failed tests?$`)

// Of is the why of a failed step: the most telling line of its output (the
// last attempt's, ANSI already stripped and secrets masked), else lastErr
// (its last non-empty stderr line), else its last non-empty output line;
// "" when it printed nothing. One line, made safe, at most Max characters.
func Of(output []string, lastErr string) string {
	return clip(pick(output, lastErr))
}

func pick(output []string, lastErr string) string {
	if s := goFail(output); s != "" {
		return s
	}
	for _, set := range markers {
		for _, l := range output {
			for _, m := range set {
				if strings.Contains(l, m) {
					return l
				}
			}
		}
	}
	for _, l := range output {
		if location.MatchString(l) {
			return l
		}
	}
	if s := lint(output); s != "" {
		return s
	}
	if s := gofmt(output); s != "" {
		return s
	}
	if strings.TrimSpace(lastErr) != "" {
		return lastErr
	}
	for i := len(output) - 1; i >= 0; i-- {
		if strings.TrimSpace(output[i]) != "" {
			return output[i]
		}
	}
	return ""
}

// goFail is a go test failure: its first "--- FAIL: TestX" that carries a
// location in the lines below it, joined with that line ("--- FAIL: TestX:
// x_test.go:41: message"), else the first "--- FAIL:" line alone.
func goFail(output []string) string {
	from := 0
	for i, l := range output {
		if goSummary.MatchString(strings.TrimSpace(l)) {
			from = i + 1
		}
	}
	first := ""
	for i := from; i < len(output); i++ {
		head := strings.TrimSpace(output[i])
		if !strings.HasPrefix(head, "--- FAIL:") {
			continue
		}
		head = testName(head)
		if first == "" {
			first = head
		}
		if loc := testLocation(output[i+1:]); loc != "" {
			return head + ": " + loc
		}
	}
	return first
}

// lint is swim lint's first error, else its first warning.
func lint(output []string) string {
	warn := ""
	for _, l := range output {
		if m := lintLine.FindStringSubmatch(l); m != nil {
			if m[1] == "error" {
				return l
			}
			if warn == "" {
				warn = l
			}
		}
	}
	return warn
}

// gofmt is a "needs gofmt -s:" header (as a lane prints gofmt -l's list)
// with the first file under it: "needs gofmt -s: internal/x.go".
func gofmt(output []string) string {
	for i, l := range output {
		head := strings.TrimSpace(l)
		if !strings.HasPrefix(head, "needs gofmt") || !strings.HasSuffix(head, ":") {
			continue
		}
		for _, f := range output[i+1:] {
			if f = strings.TrimSpace(f); f != "" {
				return head + " " + f
			}
		}
		return head
	}
	return ""
}

// testName is "--- FAIL: TestX" without its " (0.01s)" or e2etool's
// " (pkg, 1.2s)".
func testName(line string) string {
	if i := strings.LastIndex(line, " ("); i > 0 && strings.HasSuffix(line, ")") {
		return line[:i]
	}
	return line
}

// testLocation is the first "x_test.go:41: message" in a --- FAIL line's
// indented block (its === RUN/PAUSE/CONT lines skipped), "" if it has none
// before its block ends or a nested --- line starts.
func testLocation(rest []string) string {
	for _, l := range rest {
		t := strings.TrimSpace(l)
		if t == "" || l == t { // a blank or unindented line ends the block
			return ""
		}
		switch {
		case strings.HasPrefix(t, "=== "):
			continue
		case strings.HasPrefix(t, "--- "):
			return ""
		case testLoc.MatchString(t):
			return t
		default:
			return ""
		}
	}
	return ""
}

// clip makes s one safe line of at most Max characters.
func clip(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(ui.Safe(s), "\t", " "))
	if utf8.RuneCountInString(s) <= Max {
		return s
	}
	return strings.TrimSpace(string([]rune(s)[:Max-1])) + "…"
}
