package logparse

import (
	"strings"
	"testing"
)

func out(lines ...string) []string {
	r := make([]string, len(lines))
	for i, l := range lines {
		r[i] = OutPrefix + l
	}
	return r
}

func TestFoldRepeats(t *testing.T) {
	for _, tc := range []struct {
		name     string
		in, want []string
	}{
		{"the same line folds to one and a count",
			append([]string{"  PASS  wait  3.0s  12:00:01", CmdPrefix + "poll"}, out("waiting…", "waiting…", "waiting…", "waiting…", "ready")...),
			append([]string{"  PASS  wait  3.0s  12:00:01", CmdPrefix + "poll"}, OutPrefix+"waiting…", FoldNote+"3 more times", OutPrefix+"ready")},
		{"twice stays twice", out("again", "again"), out("again", "again")},
		{"lines that differ in their numbers keep the first and last",
			out("copied 1 of 50", "copied 2 of 50", "copied 3 of 50", "copied 50 of 50"),
			[]string{OutPrefix + "copied 1 of 50", FoldNote + "2 more like these", OutPrefix + "copied 50 of 50"}},
		{"three alike stay", out("node-1 ready", "node-2 ready", "node-3 ready"), out("node-1 ready", "node-2 ready", "node-3 ready")},
		{"a progress bar redrawn on one line shows where it ended",
			out("  10%\r  55%\r 100% done"), out(" 100% done")},
		{"a spinner frame alone goes; its text stays",
			out("⠋", "⠙ Waiting for pods", "⠹ Waiting for pods", "⠸ Waiting for pods"),
			[]string{OutPrefix + "Waiting for pods", FoldNote + "2 more times"}},
		{"escape sequences go", out("\x1b[32mok\x1b[0m"), out("ok")},
		{"a blank output line stays", out("a", "", "b"), out("a", "", "b")},
		{"a fold never crosses a step", append(out("x", "x"), append([]string{"  PASS  next  0.1s  12:00:02"}, out("x", "x")...)...),
			append(out("x", "x"), append([]string{"  PASS  next  0.1s  12:00:02"}, out("x", "x")...)...)},
	} {
		got := FoldRepeats(tc.in)
		if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}
