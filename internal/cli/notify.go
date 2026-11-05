package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/gates-brightly/swimlane/internal/ci"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/notify"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// terminalNotifier is the operator's terminal for notifications and the
// progress bar (package notify): nil (nothing sent) unless the notify
// setting is on, stdout is a terminal and this isn't CI.
func terminalNotifier(cfg *config.Config) *notify.Terminal {
	if cfg == nil || !cfg.NotifyOn() || !ui.IsTTY(os.Stdout) || ci.InCI(os.Getenv) {
		return nil
	}
	return notify.New(os.Stdout)
}

// commandHook sends each notification to notify_url, or runs
// notify_command for it (package notify): nil unless one is set. Each send
// is recorded (notify.StatePath: swim config and swim doctor show the
// last); one that fails is also a warn line in .swim.log, not on the
// screen it would draw over.
func commandHook(cfg *config.Config, root string) *notify.Hook {
	if cfg == nil {
		return nil
	}
	return notify.NewHook(notify.HookConfig{Command: cfg.NotifyCommand, URL: cfg.NotifyURL, Root: root, State: notify.StatePath(),
		Warn: func(why string) {
			history.Log(root, history.Entry{Event: history.Warn, Detail: why})
		}})
}

// notifySummary says how the last notification sent from this machine
// went: "last sent 3m ago: swim run passed", "last send failed 2h ago:
// notify_url: exit status 6: ...", "nothing sent yet" ("" with neither
// notify_url nor notify_command set).
func notifySummary(cfg *config.Config, now time.Time) string {
	if cfg == nil || cfg.NotifyURL == "" && cfg.NotifyCommand == "" {
		return ""
	}
	s, err := notify.LastSent(notify.StatePath())
	switch {
	case err != nil:
		return err.Error()
	case s == nil:
		return "nothing sent yet"
	case s.Error != "":
		return fmt.Sprintf("last send failed %s ago: %s", display.Duration(now.Sub(s.At)), s.Error)
	}
	return fmt.Sprintf("last sent %s ago: %s", display.Duration(now.Sub(s.At)), s.Title)
}

// testNotify sends a test notification the way swim run would here.
func testNotify(cfg *config.Config, root string) string {
	h := commandHook(cfg, root)
	if h == nil {
		return "nothing to test: set notify_url (or notify_command)"
	}
	host, _ := os.Hostname()
	err := h.SendNow(notify.Note{Event: notify.EventTest, Title: "swim: test", Body: "from swim config on " + host})
	if err != nil {
		return "test failed: " + err.Error()
	}
	return "test sent: check where it goes"
}
