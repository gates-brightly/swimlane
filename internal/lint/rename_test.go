package lint

import "testing"

// swim lint --fix rewrites a header's # Job: (or // Job:) to # Round-ID:,
// and only there: not a Job: in the body or a script without one.
func TestRenameJob(t *testing.T) {
	cases := []struct {
		in, want string
		line     int
	}{
		{"#!/usr/bin/env swim\n# Round: x\n# Job:     abc\n\nstep \"a\" true", "#!/usr/bin/env swim\n# Round: x\n# Round-ID:     abc\n\nstep \"a\" true", 3},
		{"// Round: x\n// Job: abc\npackage main", "// Round: x\n// Round-ID: abc\npackage main", 2},
		{"# Round: x\n# Round-ID: abc", "", 0},
		{"# Round: x\n\necho\n# Job: abc", "", 0},
	}
	for _, c := range cases {
		got, line := renameJob(c.in)
		want := c.want
		if want == "" {
			want = c.in
		}
		if got != want || line != c.line {
			t.Errorf("renameJob(%q)\n got %q (line %d)\nwant %q (line %d)", c.in, got, line, want, c.line)
		}
	}
}
