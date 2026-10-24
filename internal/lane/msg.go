package lane

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gates-brightly/swimlane/internal/ui"
)

// Msg is a running round's status message (swim msg): one per lane, the
// latest wins. It lives in .swim/msg/laneN so posting it never contends
// with status.yml; the launcher's panel, swim status and the hub read it.
type Msg struct {
	Text     string `json:"text"`
	Progress string `json:"progress,omitempty"` // "7/25"
	At       string `json:"at"`                 // RFC 3339
	Run      string `json:"run,omitempty"`      // the round's run, so an old message never shows on a new round

	// The log's view (throttling): what was last written to the lane log,
	// and when; and messages posted during a step, which the step writes
	// into its own block when it ends.
	Logged   string   `json:"logged,omitempty"`
	LoggedAt string   `json:"logged_at,omitempty"`
	Pending  []string `json:"pending,omitempty"`
}

// MsgMax is the longest message kept (runes).
const MsgMax = 120

// MsgPath is lane n's message file.
func MsgPath(root string, n int) string {
	return filepath.Join(root, ".swim", "msg", fmt.Sprintf("lane%d", n))
}

// ReadMsg returns lane n's message, if it has one.
func ReadMsg(root string, n int) (Msg, bool) {
	data, err := os.ReadFile(MsgPath(root, n))
	if err != nil {
		return Msg{}, false
	}
	var m Msg
	if json.Unmarshal(data, &m) != nil {
		return Msg{}, false
	}
	return m, m.Text != ""
}

// WriteMsg replaces lane n's message file atomically.
func WriteMsg(root string, n int, m Msg) error {
	path := MsgPath(root, n)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, _ := json.Marshal(m)
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ClearMsg removes lane n's message (the round is over).
func ClearMsg(root string, n int) { os.Remove(MsgPath(root, n)) }

// ValidProgress reports whether p is N/M.
func ValidProgress(p string) bool {
	n, m, ok := strings.Cut(p, "/")
	return ok && isDigits(n) && isDigits(m)
}

// CleanMsg makes text one clean line: escape sequences and control
// characters removed (ui.Safe; whitespace controls such as newlines become
// spaces first, so "a\nb" reads "a b"), whitespace collapsed, at most
// MsgMax runes.
func CleanMsg(text string) string {
	text = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\v' || r == '\f' {
			return ' '
		}
		return r
	}, text)
	text = ui.Safe(text)
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) > MsgMax {
		text = string([]rune(text)[:MsgMax-1]) + "…"
	}
	return text
}

// Display is the message as shown: "migrating billing (7/25)".
func (m Msg) Display() string {
	if m.Progress != "" {
		return m.Text + " (" + m.Progress + ")"
	}
	return m.Text
}

// Age is how long ago the message was posted.
func (m Msg) Age(now time.Time) time.Duration {
	t, err := time.Parse(time.RFC3339, m.At)
	if err != nil {
		return 0
	}
	return now.Sub(t)
}
