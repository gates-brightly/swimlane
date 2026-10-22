package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hub: is swim.yml's alone: request by default, headless.logs: off wins,
// and hub.logs: off turns off headless log uploads too.
func TestHubBlock(t *testing.T) {
	xdg, root := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	os.MkdirAll(filepath.Join(xdg, "swim"), 0o755)
	for body, want := range map[string]string{
		"":                       HubLogsRequest,
		"hub:\n  logs: stream\n": HubLogsStream,
		"hub:\n  logs: Stream\n": HubLogsStream,
		"hub:\n  logs: off\n":    HubLogsOff,
		"hub:\n  logs: stream\nheadless:\n  logs: off\n": HubLogsOff,
	} {
		os.WriteFile(filepath.Join(root, "swim.yml"), []byte(body), 0o644)
		c, err := Load(root)
		if err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		if got := c.HubLogs(); got != want {
			t.Errorf("%q: hub logs %s, want %s", body, got, want)
		}
		if c.HeadlessLogs() != (want != HubLogsOff) {
			t.Errorf("%q: headless logs %v", body, c.HeadlessLogs())
		}
	}
	for body, want := range map[string]string{
		"hub:\n  logs: all\n": "request, stream or off",
		"hub:\n  lgos: off\n": "lgos",
	} {
		os.WriteFile(filepath.Join(root, "swim.yml"), []byte(body), 0o644)
		if _, err := Load(root); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", body, err)
		}
	}
	os.WriteFile(filepath.Join(root, "swim.yml"), nil, 0o644)
	for _, cfg := range []string{"defaults:\n  hub: {logs: stream}\n", "repos:\n  " + root + ":\n    hub: {logs: stream}\n"} {
		os.WriteFile(filepath.Join(xdg, "swim", "config.yml"), []byte(cfg), 0o644)
		if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "hub: belongs in") {
			t.Errorf("hub in config.yml: %v", err)
		}
	}
}
