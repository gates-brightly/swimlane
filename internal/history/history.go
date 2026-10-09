// Package history appends top-level actions to .swim.log at the repo root:
// one line per event (rounds written, started, finished, skipped, archived,
// stubbed, notes), so a new agent can read the project's history quickly.
// Step output never goes here; that is what .swim/logs/agentN.log is for.
package history

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileName is the project log's name at the repo root.
const FileName = ".swim.log"

// Events.
const (
	Init        = "init"
	New         = "new"
	Run         = "run"
	RunDone     = "run-done"
	Start       = "start"
	Pass        = "pass"
	Fail        = "fail"
	Interrupted = "interrupted"
	Skip        = "skip"
	Archive     = "archive"
	Stub        = "stub"
	Note        = "note"
	Lanes       = "lanes"
	Lock        = "lock"
	Migrate     = "migrate"
)

const header = `# swim project log: top-level actions, oldest first. Append-only; written by swim.
# Read this to catch up, then: swim status (current state), swim log N (step detail).
# Format: <utc time>  <user>  <event>  [swim N]  [job=<id>]  <detail>
`

// Path returns the project log location.
func Path(root string) string { return filepath.Join(root, FileName) }

// Entry is one event.
type Entry struct {
	Event  string
	Lane   int    // 0 when not about one lane
	Job    string // "" when not about one job
	Detail string
}

// Format renders an entry as its log line (without the newline).
func Format(t time.Time, user string, e Entry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %-8s  %-11s", t.UTC().Format(time.RFC3339), user, e.Event)
	if e.Lane > 0 {
		fmt.Fprintf(&b, "  swim %d", e.Lane)
	}
	if e.Job != "" {
		fmt.Fprintf(&b, "  job=%s", e.Job)
	}
	if d := oneLine(e.Detail); d != "" {
		b.WriteString("  " + d)
	}
	return strings.TrimRight(b.String(), " ")
}

// Append writes one event. Each line is a single O_APPEND write, so lanes
// finishing at the same time never interleave mid-line. Errors are returned
// but callers treat the project log as best-effort.
func Append(root string, e Entry) error {
	path := Path(root)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		// Publish the file with its header already in it: link() is atomic
		// and fails if another writer got there first, so the header is
		// always the first thing in the file and written exactly once.
		tmp, err := os.CreateTemp(root, ".swim.log.*")
		if err != nil {
			return err
		}
		_, werr := tmp.WriteString(header)
		tmp.Close()
		os.Chmod(tmp.Name(), 0o644)
		if werr == nil {
			if err := os.Link(tmp.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
				werr = err
			}
		}
		os.Remove(tmp.Name())
		if werr != nil {
			return werr
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(Format(time.Now(), user(), e) + "\n")
	return err
}

// Log is Append with the error dropped, for call sites where the project
// log must never block the operator's work.
func Log(root string, e Entry) {
	if err := Append(root, e); err != nil {
		fmt.Fprintf(os.Stderr, "swim: warning: could not write %s: %v\n", FileName, err)
	}
}

func user() string {
	for _, k := range []string{"SWIM_USER", "USER", "LOGNAME"} {
		if u := os.Getenv(k); u != "" {
			return u
		}
	}
	return "-"
}

// oneLine keeps an event on one line; runs of spaces (used as separators
// inside details) are kept.
func oneLine(s string) string {
	return strings.TrimSpace(strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ").Replace(s))
}
