package chars

import (
	"strings"
	"testing"
)

func TestChars(t *testing.T) {
	for s, want := range map[string]bool{"ALLOW_X": true, "_x1": true, "1x": false, "": false, "a-b": false} {
		if Ident(s) != want {
			t.Errorf("Ident(%q) = %v", s, !want)
		}
	}
	if !Digits("0123") || Digits("") || Digits("1a") {
		t.Error("Digits")
	}
	if !Word("aws-session", Lower, Or(Lower, Digit, In("-"))) || Word("Aws", Lower, Lower) {
		t.Error("Word")
	}
	for in, want := range map[string]string{"Orders API v2": "Orders-API-v2", "  a..b  ": "a-b", "---": "", "x": "x"} {
		if got := Squash(in, Alnum, "-"); got != want {
			t.Errorf("Squash(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBetweenAndName(t *testing.T) {
	for _, c := range []struct {
		s, pre, suf, want string
		ok                bool
	}{
		{"lane12.pid", "lane", ".pid", "12", true},
		{"lane.pid", "lane", ".pid", "", false},
		{".lane.3.rc", ".lane.", ".rc", "3", true},
		{"agent1.log", "lane", ".log", "", false},
	} {
		if got, ok := Between(c.s, c.pre, c.suf); got != c.want || ok != c.ok {
			t.Errorf("Between(%q) = %q %v", c.s, got, ok)
		}
	}
	for s, ok := range map[string]bool{"zach": true, "a.b-c_d": true, ".x": false, "": false, "a b": false, strings.Repeat("a", 65): false, strings.Repeat("a", 64): true} {
		if Name(s, 64) != ok {
			t.Errorf("Name(%q) != %v", s, ok)
		}
	}
}
