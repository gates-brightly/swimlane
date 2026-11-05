package doctor

import (
	"github.com/gates-brightly/swimlane/internal/finding"
	"github.com/gates-brightly/swimlane/internal/notify"
)

// checkNotifyFailing: the last notification this machine sent to
// notify_url (or ran notify_command for) failed, so the operator may be
// missing questions and results where they look for them.
func checkNotifyFailing(o *Options) []finding.Finding {
	if o.Cfg == nil || o.Cfg.NotifyURL == "" && o.Cfg.NotifyCommand == "" {
		return nil
	}
	path := notify.StatePath()
	s, err := notify.LastSent(path)
	if err != nil || s == nil || s.Error == "" {
		return nil
	}
	return []finding.Finding{fd(finding.Warn, "notify-failing", path, "fix notify_url or notify_command; swim config, then t, sends a test",
		"the last notification (%s, %s) failed: %s", s.Title, s.At.Local().Format("2006-01-02 15:04"), s.Error)}
}
