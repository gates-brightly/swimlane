package cli

import "testing"

func TestDidYouMean(t *testing.T) {
	cmds := []string{"history", "changelog", "run", "verify", "status"}
	for word, want := range map[string]string{
		"histroy":  " (did you mean history?)",
		"changlog": " (did you mean changelog?)",
		"deploy":   "",
		"x":        "",
	} {
		if got := didYouMean(word, cmds); got != want {
			t.Errorf("didYouMean(%q) = %q, want %q", word, got, want)
		}
	}
}

// An unknown flag names the closest one.
func TestUnknownFlagSuggests(t *testing.T) {
	var plain bool
	_, err := flags{bools: map[string]*bool{"plain": &plain}}.parse([]string{"--pain"})
	if err == nil || err.Error() != "unknown flag --pain (did you mean --plain?)" {
		t.Errorf("err = %v", err)
	}
}
