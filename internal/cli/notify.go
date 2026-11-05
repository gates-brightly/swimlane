package cli

import (
	"os"

	"github.com/gates-brightly/swimlane/internal/ci"
	"github.com/gates-brightly/swimlane/internal/config"
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

// commandHook runs notify_command for each notification (package notify):
// nil unless it's set. A command that fails is a warn line in .swim.log,
// not on the screen it would draw over.
func commandHook(cfg *config.Config, root string) *notify.Hook {
	if cfg == nil {
		return nil
	}
	return notify.NewHook(cfg.NotifyCommand, root, func(why string) {
		history.Log(root, history.Entry{Event: history.Warn, Detail: why})
	})
}
