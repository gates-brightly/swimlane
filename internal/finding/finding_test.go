package finding

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderAndExit(t *testing.T) {
	fs := []Finding{
		{Level: Warn, File: "lane.10.sh", Line: 3, Code: "bash4", Message: "mapfile"},
		{Level: Error, File: "lane.2.sh", Line: 5, Code: "set-e", Message: "uses set -e", Fix: "remove it"},
		{Level: Info, Code: "lock", Message: "no lock"},
		{Level: Warn, File: "lane.2.sh", Code: "summary", Message: "no summary"},
	}
	Sort(fs)
	if fs[0].File != "lane.2.sh" || fs[0].Line != 0 || fs[2].File != "lane.10.sh" || fs[3].File != "" {
		t.Errorf("order: %+v", fs)
	}
	var b bytes.Buffer
	WriteText(&b, fs)
	out := b.String()
	for _, want := range []string{"lane.2.sh:5   error  uses set -e  [set-e]\n", "fix: remove it", "-             info   no lock", "1 error, 2 warnings, 1 info\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("text missing %q:\n%s", want, out)
		}
	}
	if ExitCode(fs, false) != 1 || ExitCode(fs[2:3], false) != 0 || ExitCode(fs[2:3], true) != 1 || ExitCode(fs[3:], true) != 0 {
		t.Error("exit codes")
	}
	b.Reset()
	if err := WriteYAML(&b, "swim.lint/v1", nil); err != nil || !strings.Contains(b.String(), "schema: swim.lint/v1") || !strings.Contains(b.String(), "findings: []") {
		t.Errorf("yaml: %v %s", err, b.String())
	}
	if Summary(nil) != "no problems found" {
		t.Error(Summary(nil))
	}
}
