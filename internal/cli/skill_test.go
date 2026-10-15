package cli

import (
	"errors"
	"strings"
	"testing"
)

func TestSkillText(t *testing.T) {
	url := skillURL()
	if !strings.HasPrefix(url, "https://github.com/gates-brightly/swimlane/tree/") || !strings.HasSuffix(url, "/skills/swim") {
		t.Errorf("skillURL = %q", url)
	}
	text := skillText(url)
	if !strings.Contains(text, "Install the Claude skill at "+url) {
		t.Errorf("no line to paste:\n%s", text)
	}
}

func TestLatestTextUnreachable(t *testing.T) {
	got := latestText("", errors.New("https://example/x.git: could not resolve host"))
	if got != "latest release: unknown (https://example/x.git: could not resolve host)\n" {
		t.Errorf("latestText = %q", got)
	}
}
