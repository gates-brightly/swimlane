package lane

import "testing"

func TestCleanMsg(t *testing.T) {
	for in, want := range map[string]string{
		"migrating billing":                         "migrating billing",
		"a\nb\tc\r\nd":                              "a b c d",
		"\x1b[31mred\x1b[0m done":                   "red done",
		"ok\x1b[2K\r\bfake ‮evil‬":                  "ok fake evil",
		"\x1b]8;;https://x\x1b\\link\x1b]8;;\x1b\\": "link",
		"a\u009bb — 進捗":                             "ab — 進捗",
	} {
		if got := CleanMsg(in); got != want {
			t.Errorf("CleanMsg(%q) = %q, want %q", in, got, want)
		}
	}
}
