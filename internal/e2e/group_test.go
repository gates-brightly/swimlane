package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/status"
)

// A group file holds several lanes; each runs like a lane.N.sh.
func TestGroupFile(t *testing.T) {
	r := newRepo(t, "")
	write := func(src string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(r.root, "lanes.ci.sh"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`#!/usr/bin/env bash
# swim: syntax 2
# The checks.
export GREETING=hello

# swim lane 1 -----
# Round:  fmt
# Always: yes
run "greet" bash -c 'echo "$GREETING from swim $SWIM_LANE"' 
run "mark" touch fmt.done

# swim lane 2 -----
# Round:  lint
# Always: yes
# After:  1
gate "fmt first" test -e fmt.done
`)
	r.script(3, "a round", `run "x" test -e fmt.done`)

	out := r.mustSwim("all")
	for n := 1; n <= 3; n++ {
		if st := r.status().Get(n).State; st != status.Passed {
			t.Errorf("swim %d = %s\n%s", n, st, out)
		}
	}
	contains(t, "lane 1 log", r.log(1), "hello from swim 1")

	// Findings point at the group file's lines.
	write(strings.Replace(string(mustRead(t, filepath.Join(r.root, "lanes.ci.sh"))), `gate "fmt first"`, "set -e\ngate \"fmt first\"", 1))
	lint, _ := r.swim("lint")
	contains(t, "lint", lint, "lanes.ci.sh:16", "swim 2: ")

	// A grouped lane is edited in its group file.
	if out, code := r.swim("new", "2", "other", "--force"); code == 0 || !strings.Contains(out, "lanes.ci.sh") {
		t.Errorf("new over a grouped lane: %d %s", code, out)
	}
	// Defined twice: refused before anything runs.
	r.script(1, "also fmt", `run "x" true`)
	if out, code := r.swim("all"); code == 0 || !strings.Contains(out, "lanes.ci.sh:6") {
		t.Errorf("lane in a group and a lane.N.sh: %d %s", code, out)
	}
}
