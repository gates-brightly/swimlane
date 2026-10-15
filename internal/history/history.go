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
	Init          = "init"
	New           = "new"
	Run           = "run"
	RunDone       = "run-done"
	Start         = "start"
	Pass          = "pass"
	Fail          = "fail"
	Interrupted   = "interrupted"
	Skip          = "skip"
	Archive       = "archive"
	Stub          = "stub"
	Note          = "note"
	Lanes         = "lanes"
	Lock          = "lock"
	Migrate       = "migrate"
	Queued        = "queued"
	Locked        = "locked"         // a lane waits for a resource lock
	StopReq       = "stop"           // swim interrupt N: a lane asked to stop at its next step boundary
	Approve       = "approve"        // the operator approved a guarded step at the prompt
	Decline       = "decline"        // ... declined it, or no answer came (the step ran as a dry run)
	QueueAdd      = "queue"          // swim queue add: a batch was queued (detail: id, lanes, approvals)
	QueueRun      = "queue-run"      // swim watch started a queued batch
	QueueDone     = "queue-done"     // ... and consumed it (detail: id, result)
	QueueRm       = "queue-rm"       // a waiting batch removed (swim clean)
	Clean         = "clean"          // swim clean: lanes archived, queue and scripts cleared (detail: name, counts)
	Prune         = "prune"          // swim prune: leftovers deleted (detail: name, counts, bytes)
	Trash         = "trash"          // swim trash: one lane's files moved aside (detail: stamp, files, from)
	Untrash       = "untrash"        // swim trash --restore: a trashed lane's files put back
	Schedule      = "schedule"       // swim schedule: a batch to start no earlier than a time (detail: id, lanes, not_before, zone, approvals)
	ScheduleStart = "schedule-start" // a worker claimed a scheduled batch (detail: how late)
	ScheduleRm    = "schedule-rm"    // swim schedule rm: a scheduled batch removed before it ran
	// Recurring schedules (swim schedule --daily).
	ScheduleSkipped = "schedule-skipped" // an occurrence not made: superseded (no worker then) or overlap
	SchedulePaused  = "schedule-paused"  // a recurring schedule paused (by hand, or a pinned lane changed)
	ScheduleResumed = "schedule-resumed"
	ScheduleExpired = "schedule-expired"
	SecretSet       = "secret-set" // swim secret set: a repo-scoped secret was stored (detail: its name, never its value)
	SecretRm        = "secret-rm"  // swim secret rm: ... removed
	SecretUse       = "secret"     // a lane got a secret (detail: name, scope, how)
	// A guarded secret (swim.yml secrets:) released to a lane once its
	// guard was approved, or refused (detail: name, flags, how or why).
	SecretReleased = "secret-released"
	SecretRefused  = "secret-refused"
	Hub            = "hub" // enrolled with a hub, left it, or a command from it (detail: hub <cmd> by <user> via <url>: <outcome>)
)

const header = `# swim project log: top-level actions, oldest first. Append-only; written by swim.
# Read this to catch up, then: swim status (current state), swim log N (step detail).
# Format: <utc time>  <user>  <event>  [swim N]  [job=<id>]  [run=<id>]  <detail>
`

// Path returns the project log location.
func Path(root string) string { return filepath.Join(root, FileName) }

// Entry is one event.
type Entry struct {
	Event  string
	Lane   int    // 0 when not about one lane
	Job    string // "" when not about one job
	Run    string // the swim run it belongs to ("" when not about a run)
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
	if e.Run != "" {
		fmt.Fprintf(&b, "  run=%s", e.Run)
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
	// swim ci tags results with the commit it ran for (SWIM_COMMIT).
	if c := os.Getenv("SWIM_COMMIT"); c != "" {
		switch e.Event {
		case Run, RunDone, Start, Pass, Fail, Interrupted:
			e.Detail = strings.TrimSpace(e.Detail + "  commit=" + c)
		}
	}
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
