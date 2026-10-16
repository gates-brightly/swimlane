package chars

import "testing"

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
