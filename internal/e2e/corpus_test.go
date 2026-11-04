package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The lint/runtime corpus: the ways an agent writes a step, each run
// through both swim lint and swim run, so the two can't drift apart. What
// lint passes runs as it reads (its output in the log, the round PASS);
// what lint refuses, swim run refuses too, before any step runs. A new
// step form, or a new mistake seen in the wild, gets a line here.
func TestLintAndRuntimeAgree(t *testing.T) {
	r := newSerialRepo(t, "")
	write := func(shebang, body string) {
		t.Helper()
		s := shebang + "\n# swim: syntax 4\n# Round: corpus\n\nstage check\n" + body + "\n"
		if err := os.WriteFile(filepath.Join(r.root, "lane.1.sh"), []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const short = "#!/usr/bin/env swim"

	// Lint passes these, and they run as written.
	for _, tc := range []struct{ body, want string }{
		{`run "plain" echo corpus-ok`, "| corpus-ok"},
		{`run "dash dash" -- echo corpus-ok`, "| corpus-ok"},
		{`run --timeout=1m "opt equals" echo corpus-ok`, "| corpus-ok"},
		{`run --timeout 1m --retry 1 "opt value" echo corpus-ok`, "| corpus-ok"},
		{`run "pipe inside" bash -c 'echo corpus-ax | tr x k'`, "| corpus-ak"},
		{`run "quiet" echo corpus-ok >/dev/null`, "| corpus-ok"},
		{`run "merged" echo corpus-ok 2>&1`, "| corpus-ok"},
		{`run "or true" echo corpus-ok || true`, "| corpus-ok"},
		{`gate "and then" true && echo corpus-after`, "PASS  and then"},
		{`run "expands" echo "corpus-$SWIM_LANE"`, "| corpus-1"},
		{`snapshot "snap" echo corpus-ok`, "| corpus-ok"},
		{`run --expect-fail "fails" false`, "PASS  fails"},
	} {
		write(short, tc.body)
		if out, code := r.swim("lint", "1"); code != 0 {
			t.Errorf("lint refuses %q, which runs:\n%s", tc.body, out)
			continue
		}
		out, code := r.swim("run", "1", "--plain")
		log := r.log(1)
		if code != 0 || !strings.Contains(log, tc.want) {
			t.Errorf("%q lints clean but doesn't run as written (exit %d, want %q in the log):\n%s\n%s", tc.body, code, tc.want, out, log)
		}
		r.mustSwim("archive", "1")
	}

	// Lint refuses these (each with its check), and so does swim run.
	for _, tc := range []struct{ shebang, body, code string }{
		{short, `run echo hi`, "step"},
		{short, `run "words" "echo hi there"`, "step"},
		{short, `run "dir" cd /tmp`, "step"},
		{short, `run "env" export X=1`, "step"},
		{short, `gate --retries 2 "typo" false`, "step"},
		{short, `gate --timeout= "empty" false`, "step"},
		{short, `gate "no command"`, "step"},
		{short, `run "a step"; echo corpus-later | cat`, "step"}, // no command: the pipe after ; is the lane's own
		{short, `run "count" ls | wc -l`, "step-pipe"},
		{short, `run "file" echo hi > out.txt`, "step-pipe"},
		{"#!/bin/bash", `run "x" true`, "short-form"},
	} {
		write(tc.shebang, tc.body)
		out, code := r.swim("lint", "1")
		if code == 0 || !strings.Contains(out, fmt.Sprintf("[%s]", tc.code)) {
			t.Errorf("lint passes %q (want [%s]):\n%s", tc.body, tc.code, out)
			continue
		}
		before := r.log(1)
		if out, code := r.swim("run", "1", "--plain"); code == 0 || r.log(1) != before && strings.Contains(r.log(1), "  PASS  ") {
			t.Errorf("swim run ran %q, which lint refuses (exit %d):\n%s", tc.body, code, out)
		}
	}
}
