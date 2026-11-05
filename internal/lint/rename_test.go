package lint

import (
	"strings"
	"testing"
)

// swim lint --fix rewrites the run calls in a bash lane to step, and
// nothing else named run: not swim run, go run, npm run, nor a run in
// quoted text, a comment or a heredoc body. Remove after the next
// release (T38), with the run alias.
func TestRenameRun(t *testing.T) {
	cases := []struct{ in, want string }{
		{`run "build" go build ./...`, `step "build" go build ./...`},
		{`  run --timeout 5m "wait" make check`, `  step --timeout 5m "wait" make check`},
		{`run build make`, `step build make`},
		{`if guard A "a"; then run "a" true; fi`, `if guard A "a"; then step "a" true; fi`},
		{`last_failed || run "b" true && run 'c' true`, `last_failed || step "b" true && step 'c' true`},
		{`while read x; do run "each" echo "$x"; done < f`, `while read x; do step "each" echo "$x"; done < f`},
		{`case $a in x) run "x" true ;; esac`, `case $a in x) step "x" true ;; esac`},
		{`if ! run "neg" false; then :; fi`, `if ! step "neg" false; then :; fi`},
		{`echo "$(run "quoted" true)"`, `echo "$(step "quoted" true)"`},
		{`x=$(run "sub" true)`, `x=$(step "sub" true)`},
		{`swim run 1 2`, ``},
		{`go run ./cmd/x`, ``},
		{`npm run build`, ``},
		{`step "go" go run ./cmd/x run "x"`, ``},
		{`echo "then; run it"`, ``},
		{`echo 'run "x" y'`, ``},
		{`# run "x" y`, ``},
		{`gate "g" bash -c 'run "x" y'`, ``},
		{`run`, ``},
		{"cat <<EOF\nrun \"doc\" true\nEOF", ``},
		{"echo a \\\n  run \"arg\" true", ``},
		{"run() { echo \"$@\"; }\nrun \"own\" true", ``},
	}
	for _, c := range cases {
		got, lines := renameRun(c.in)
		want := c.want
		if want == "" {
			want = c.in
		}
		if got != want {
			t.Errorf("renameRun(%q)\n got %q\nwant %q", c.in, got, want)
		}
		if (len(lines) > 0) != (c.want != "") {
			t.Errorf("renameRun(%q): lines %v", c.in, lines)
		}
	}
}

// A run call is warned (run-is-step) on its line; step isn't.
func TestRunIsStep(t *testing.T) {
	src := replace(clean(1), "stage verify\n", "stage verify\nrun \"old\" true\n")
	n := 0
	for _, f := range Lint(cfg4(), map[int]string{1: src, 2: clean(2)}, 1) {
		if f.Code == "run-is-step" {
			n++
			if !strings.HasPrefix(f.Message, "run is now step") || strings.Split(src, "\n")[f.Line-1] != `run "old" true` {
				t.Errorf("finding %+v", f)
			}
		}
	}
	if n != 1 {
		t.Errorf("%d run-is-step findings, want 1", n)
	}
}
