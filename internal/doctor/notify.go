package doctor

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gates-brightly/swimlane/internal/finding"
	"github.com/gates-brightly/swimlane/internal/notify"
)

// checkNotifyTarget: where notifications go is reachable. notify_url is
// POSTed to with curl; notify_command runs with sh -c.
func checkNotifyTarget(o *Options) []finding.Finding {
	const test = "; then swim config, t sends a test"
	var out []finding.Finding
	if u := o.Cfg.NotifyURL; u != "" && o.Cfg.NotifyCommand == "" {
		if _, err := exec.LookPath("curl"); err != nil {
			out = append(out, fd(finding.Warn, "notify", "", "install curl", "curl is not on PATH; swim sends notify_url's notifications with it"))
		}
		ctx, cancel := context.WithTimeout(context.Background(), o.NotifyTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
		if err == nil {
			var resp *http.Response
			if resp, err = http.DefaultClient.Do(req); err == nil {
				resp.Body.Close()
			}
		}
		if err != nil {
			out = append(out, fd(finding.Warn, "notify", "", "check notify_url and the network here"+test,
				"notify_url %s doesn't answer: %v", u, err))
		}
	}
	if c := strings.TrimSpace(o.Cfg.NotifyCommand); c != "" {
		if prog := commandWord(c); prog != "" && !runnable(prog) {
			out = append(out, fd(finding.Warn, "notify", "", "fix notify_command (swim config --path)"+test,
				"notify_command runs %s, which isn't on PATH or isn't executable", prog))
		}
	}
	return out
}

// commandWord is the program a shell command line runs first: its first
// word after any VAR=value assignments ("" when it can't tell).
func commandWord(line string) string {
	for _, w := range strings.Fields(line) {
		if strings.ContainsAny(w, `"'$\`+"`") {
			return "" // quoted or expanded: sh would read it differently
		}
		if i := strings.IndexByte(w, '='); i > 0 && !strings.Contains(w[:i], "/") {
			continue
		}
		return w
	}
	return ""
}

// runnable reports whether sh -c could run prog: an executable file at a
// path, or a command or builtin it finds.
func runnable(prog string) bool {
	if strings.Contains(prog, "/") {
		if rest, ok := strings.CutPrefix(prog, "~/"); ok {
			home, _ := os.UserHomeDir()
			prog = filepath.Join(home, rest)
		}
		st, err := os.Stat(prog)
		return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
	}
	if _, err := exec.LookPath(prog); err == nil {
		return true
	}
	_, err := probe("sh", "-c", `command -v "$1" >/dev/null`, "sh", prog)
	return err == nil
}

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
