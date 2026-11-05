package why

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// Real failing steps' output, copied from lane logs (testdata/*.txt; a
// hostname replaced): what each one's why is.
func TestOfRealFailures(t *testing.T) {
	for _, tc := range []struct{ file, want string }{
		// go test: the --- FAIL line joined with its location below it.
		{"go-test-unit.txt", "--- FAIL: TestIsDefault: config_test.go:341: IsDefault(cache_dir, ~/.cache/swim) = false, <nil>; want true"},
		{"go-test-unit-mise.txt", "--- FAIL: TestTemplatesRenderAndParse: assets_test.go:43: lane.3.js doesn't parse: exit status 1"},
		// go test -json through e2etool: its own failure list, not the
		// stream (where a test's output comes before its --- FAIL line).
		{"go-test-e2e-json.txt", "--- FAIL: TestCIHeartbeat: ci_test.go:231: a lane writing output needs no heartbeat:"},
		{"go-test-e2e-json-one.txt", "--- FAIL: TestJobIDsAndPinning: e2e_test.go:865: new printed no job id:"},
		{"go-test-timeout-panic.txt", "panic: test timed out after 10m0s"},
		// No marker: the last line it printed.
		{"curl-http-404.txt", `{"message":"The target couldn't be found.","url":"https://forgejo.example.ts.net/api/swagger","errors":[]}`},
		{"curl-refused.txt", "curl: (7) Failed to connect to 127.0.0.1 port 9 after 0 ms: Couldn't connect to server"},
		{"missing-command-127.txt", "bash: line 1: kubectl-nope: command not found"},
		{"missing-exec-127.txt", `swim: cannot start kubectl-nope: exec: "kubectl-nope": executable file not found in $PATH`},
		{"go-vet-no-packages.txt", "no packages to vet"},
		// gofmt -l's list under its header: the first file.
		{"gofmt.txt", "needs gofmt -s: internal/cli/schedule.go"},
		// swim lint: its first error, else its first warning.
		{"swim-lint.txt", `lane.1.sh:8  warn   disable comment names unknown check "workspace-live"  [lint-ignore]`},
		{"swim-lint-error.txt", "lane.2.sh:4  error  `After: 9`: no swim 9: it has no lane script, log or past run; swim run refuses this lane  [after]"},
		// A gate that printed nothing has no why.
		{"gate-test-f.txt", ""},
	} {
		data, err := os.ReadFile(filepath.Join("testdata", tc.file))
		if err != nil {
			t.Fatal(err)
		}
		var lines []string
		if s := strings.TrimRight(string(data), "\n"); s != "" {
			lines = strings.Split(s, "\n")
		}
		if got := Of(lines, ""); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.file, got, tc.want)
		}
	}
}

func TestOf(t *testing.T) {
	long := strings.Repeat("x", 300)
	for _, tc := range []struct {
		name    string
		out     []string
		lastErr string
		want    string
	}{
		{"nothing printed", nil, "", ""},
		{"blank lines only", []string{"", "  "}, "", ""},
		{"last output line", []string{"one", "two", ""}, "", "two"},
		{"stderr before the last output line", []string{"building", "could not reach db", "done"}, "could not reach db", "could not reach db"},
		{"a marker before stderr", []string{"x.go:3: bad thing", "oops"}, "oops", "x.go:3: bad thing"},
		{"priority, not order", []string{"error: later", "panic: boom"}, "", "panic: boom"},
		{"FAIL: before fatal", []string{"fatal: not a git repository", "FAIL: check"}, "", "FAIL: check"},
		{"fatal before error", []string{"Error: x", "log.Fatal hit"}, "", "log.Fatal hit"},
		{"ERROR", []string{"starting", "2026 ERROR db down", "exit"}, "", "2026 ERROR db down"},
		{"location", []string{"compiling", "main.go:12:3: undefined: x", "done"}, "", "main.go:12:3: undefined: x"},
		{"trimmed", []string{"   spaced out \t "}, "", "spaced out"},
		{"controls stripped", []string{"\x1b[31mred\x1b[0m\x07 text"}, "", "red text"},
		{"cut to Max", []string{long}, "", strings.Repeat("x", Max-1) + "…"},
		{"go test nested subtest", []string{
			"--- FAIL: TestA (0.01s)",
			"    --- FAIL: TestA/sub (0.00s)",
			"        a_test.go:9: got 1",
			"FAIL",
		}, "", "--- FAIL: TestA/sub: a_test.go:9: got 1"},
		{"gofmt header with a note", []string{"needs gofmt -s (just fmt):", "a.go", "b.go"}, "", "needs gofmt -s (just fmt): a.go"},
		{"go test without a location", []string{"--- FAIL: TestB (0.00s)", "FAIL"}, "", "--- FAIL: TestB"},
	} {
		if got := Of(tc.out, tc.lastErr); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
		if n := utf8.RuneCountInString(Of(tc.out, tc.lastErr)); n > Max {
			t.Errorf("%s: %d characters", tc.name, n)
		}
	}
}
