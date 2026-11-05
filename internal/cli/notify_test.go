package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/config"
)

// The last send, as swim config shows it: nothing with no endpoint set.
func TestNotifySummary(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfg := &config.Config{}
	if s := notifySummary(cfg, time.Now()); s != "" {
		t.Errorf("nothing set: %q", s)
	}
	cfg.NotifyCommand = "exit 3"
	if s := notifySummary(cfg, time.Now()); s != "nothing sent yet" {
		t.Errorf("none sent: %q", s)
	}
	if got := testNotify(cfg, t.TempDir()); got != "test failed: notify_command: exit status 3" {
		t.Errorf("testNotify = %q", got)
	}
	if s := notifySummary(cfg, time.Now()); !strings.HasPrefix(s, "last send failed 0.0s ago: notify_command: exit status 3") {
		t.Errorf("failed: %q", s)
	}
	cfg.NotifyCommand = "true"
	if got := testNotify(cfg, t.TempDir()); !strings.HasPrefix(got, "test sent") {
		t.Errorf("testNotify = %q", got)
	}
	if s := notifySummary(cfg, time.Now().Add(3*time.Minute)); s != "last sent 3m 0s ago: swim: test" {
		t.Errorf("sent: %q", s)
	}
}
