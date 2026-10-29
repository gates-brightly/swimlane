package lint

import "testing"

// decide is refused in a subshell (lint: decision-subshell), and only there.
func TestDecideSubshellRE(t *testing.T) {
	for line, want := range map[string]bool{
		`x=$(decide R "q" a b)`:        true,
		"x=`decide R q a b`":           true,
		`decide R "q" a b | tee x`:     true,
		`echo y | decide R q a b`:      true,
		`decide R "q" "a" "b"`:         false,
		`decide R "q" a b || stop "x"`: false,
		`decide R "q" "a | b" c`:       false,
	} {
		v := scan(line)
		got := decideSubshellRE.MatchString(v.bare[0])
		if got != want {
			t.Errorf("%q (bare %q): %v, want %v", line, v.bare[0], got, want)
		}
	}
}
