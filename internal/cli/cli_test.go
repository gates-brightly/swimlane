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

// --force acknowledges gaps and deletions, never changed content.
func TestAmendable(t *testing.T) {
	for p, want := range map[string]bool{
		"entry #3 (line 2): entry #2 is missing before it (removed)":                                                          true,
		"entry #3 (line 2): prev hash doesn't match the entry before it (an earlier entry was changed, removed or reordered)": true,
		"the logs of 2 rounds are gone (deleted, not archived), so their contents can't be rechecked: #1, 3":                  true,
		"entry #4: the script copy 1a2b3c4d5e6f.sh is missing":                                                                true,
		"entry #4 (line 4): signature doesn't match the entry (it was changed after signing): bad":                            false,
		"entry #5: swim 1's log for run r-1 was changed after the round ended":                                                false,
		"entry #4: the kept script 1a2b3c4d5e6f.sh was changed":                                                               false,
		"entry #6: swim 1's staged copy in run r-1: manifest changed since staged (manifest.yml)":                             false,
	} {
		if amendable(p) != want {
			t.Errorf("amendable(%q) = %v", p, !want)
		}
	}
}
