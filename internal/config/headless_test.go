package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// headless: is swim.yml's alone, with defaults when it's absent; bad
// values and the block in config.yml are refused.
func TestHeadlessBlock(t *testing.T) {
	xdg, root := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	os.MkdirAll(filepath.Join(xdg, "swim"), 0o755)
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.HeadlessAllow(), HeadlessDefaultAllow) || c.HeadlessAllows(HubRun) || !c.HeadlessLogs() ||
		c.CommandTTL(HubRun) != 10*time.Minute || c.CommandTTL(HubInterrupt) != 2*time.Minute ||
		c.HeadlessApprovals() != HeadlessSigned || c.AnswerTimeout() != 30*time.Minute {
		t.Errorf("defaults: %v", c.Headless)
	}
	os.WriteFile(filepath.Join(root, "swim.yml"), []byte("headless:\n  allow: [run, log]\n  logs: off\n  command_ttl: 30s\n  approvals: hub\n"), 0o644)
	if c, err = Load(root); err != nil {
		t.Fatal(err)
	}
	if !c.HeadlessAllows(HubRun) || c.HeadlessAllows(HubInterrupt) || c.HeadlessLogs() || c.CommandTTL(HubInterrupt) != 30*time.Second || c.HeadlessApprovals() != HeadlessHub {
		t.Errorf("swim.yml: %+v", c.Headless)
	}
	for body, want := range map[string]string{
		"headless:\n  allow: [deploy]\n":     "unknown command",
		"headless:\n  logs: maybe\n":         "on or off",
		"headless:\n  command_ttl: soon\n":   "duration",
		"headless:\n  approvals: anyone\n":   "signed or hub",
		"headless:\n  allwo: [run]\n":        "allwo",
		"headless:\n  answer_timeout: -1m\n": "duration",
	} {
		os.WriteFile(filepath.Join(root, "swim.yml"), []byte(body), 0o644)
		if _, err := Load(root); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", body, err)
		}
	}
	os.WriteFile(filepath.Join(root, "swim.yml"), nil, 0o644)
	for _, cfg := range []string{"defaults:\n  headless: {allow: [run]}\n", "repos:\n  " + root + ":\n    headless: {allow: [run]}\n"} {
		os.WriteFile(filepath.Join(xdg, "swim", "config.yml"), []byte(cfg), 0o644)
		if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "headless: belongs in") {
			t.Errorf("headless in config.yml: %v", err)
		}
	}
}
