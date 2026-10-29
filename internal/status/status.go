// Package status owns .swim/status.yml, the last known state of every lane.
// Every swim process that changes a lane updates it under an exclusive
// flock and replaces it atomically, so parallel lanes never clobber each
// other and `cat` never sees a partial file.
package status

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/failpoint"
	"github.com/gates-brightly/swimlane/internal/fsutil"
	"github.com/gates-brightly/swimlane/internal/proc"
)

// Lane states.
const (
	Idle        = "idle"
	Waiting     = "waiting"
	Queued      = "queued" // waiting for a free slot (max_parallel)
	Locked      = "locked" // waiting for a resource lock (# Locks:)
	Running     = "running"
	Passed      = "passed"
	Failed      = "failed"
	Skipped     = "skipped"
	Interrupted = "interrupted"
)

// Lane is one lane's entry. Fields without omitempty are always written so
// the file reads the same for every lane.
type Lane struct {
	Lane        int      `yaml:"lane" json:"lane"`
	State       string   `yaml:"state" json:"state"`
	Pending     string   `yaml:"pending" json:"pending"`                                     // Round: line of the lane script on disk ("" for stub/none)
	PendingJob  string   `yaml:"pending_job" json:"pending_job"`                             // Job: id of the lane script on disk
	Round       string   `yaml:"round" json:"round"`                                         // round of the last or current run
	Job         string   `yaml:"job" json:"job"`                                             // job id of the last or current run
	Run         string   `yaml:"run,omitempty" json:"run,omitempty"`                         // swim run id of the last or current round
	Commit      string   `yaml:"commit,omitempty" json:"commit,omitempty"`                   // commit the round ran for (swim ci)
	ScriptSHA   string   `yaml:"script_sha256,omitempty" json:"script_sha256,omitempty"`     // SHA-256 of the lane script the round runs
	ManifestSHA string   `yaml:"manifest_sha256,omitempty" json:"manifest_sha256,omitempty"` // its execution manifest (internal/manifest)
	EnvSHA      string   `yaml:"env_sha256,omitempty" json:"env_sha256,omitempty"`           // SHA-256 of its env from env files ("" for none)
	Script      string   `yaml:"script" json:"script"`
	Log         string   `yaml:"log" json:"log"`
	WaitingOn   []int    `yaml:"waiting_on,flow" json:"waiting_on"`
	Reason      string   `yaml:"reason,omitempty" json:"reason,omitempty"`
	Locks       []string `yaml:"locks,flow,omitempty" json:"locks,omitempty"` // locks the running round holds
	PID         int      `yaml:"pid,omitempty" json:"pid,omitempty"`
	Host        string   `yaml:"host,omitempty" json:"host,omitempty"` // the machine PID runs on ("" from an older swim: this one)
	StartedAt   *string  `yaml:"started_at" json:"started_at"`
	FinishedAt  *string  `yaml:"finished_at" json:"finished_at"`
	DurationS   *float64 `yaml:"duration_s" json:"duration_s"`
	ExitCode    *int     `yaml:"exit_code" json:"exit_code"`
	Pass        int      `yaml:"pass" json:"pass"`
	Fail        int      `yaml:"fail" json:"fail"`
	Skip        int      `yaml:"skip" json:"skip"`
	Drift       int      `yaml:"drift" json:"drift"`
	Stage       string   `yaml:"stage,omitempty" json:"stage,omitempty"` // stage the running round is in
	CurrentStep string   `yaml:"current_step" json:"current_step"`
	FailedSteps []string `yaml:"failed_steps" json:"failed_steps"`
	Stages      []string `yaml:"stages,flow,omitempty" json:"stages,omitempty"`     // e.g. [snapshot PASS, check FAIL, change none, verify none]
	Handlers    []string `yaml:"handlers,flow,omitempty" json:"handlers,omitempty"` // outcome handlers that ran, e.g. [on_failure PASS, on_exit PASS]
	// Message is the running round's status message (swim msg), with its
	// progress ("7/25") and when it was posted; cleared when the round ends.
	Message   string `yaml:"message,omitempty" json:"message,omitempty"`
	Progress  string `yaml:"progress,omitempty" json:"progress,omitempty"`
	MessageAt string `yaml:"message_at,omitempty" json:"message_at,omitempty"`
	// Outputs are the last round's outputs (# Outputs:, output KEY value):
	// name -> SHA-256, in .swim/out/<job>/<run>/.
	Outputs map[string]string `yaml:"outputs,omitempty" json:"outputs,omitempty"`
	// The guards the round's run approved without a prompt: AutoApproved by
	// -y (swim run -y, a queue batch's -y, swim watch -y; AutoVia says
	// which), FlagApproved by a guard flag swim run's own environment set
	// (FLAG=1) or the queue batch approved. Cleared when a round starts.
	AutoApproved []string `yaml:"auto_approved,flow,omitempty" json:"auto_approved,omitempty"`
	AutoVia      string   `yaml:"auto_via,omitempty" json:"auto_via,omitempty"`
	FlagApproved []string `yaml:"flag_approved,flow,omitempty" json:"flag_approved,omitempty"`
	// DryRun are the guards the round's run didn't approve (declined, not
	// asked, refused): their steps ran as dry runs. Cleared when a round
	// starts.
	DryRun      []string `yaml:"dry_run,flow,omitempty" json:"dry_run,omitempty"`
	LastArchive string   `yaml:"last_archive,omitempty" json:"last_archive,omitempty"`
}

// File is the whole status.yml document.
type File struct {
	UpdatedAt string `yaml:"updated_at" json:"updated_at"`
	Root      string `yaml:"root" json:"root"`
	Branch    string `yaml:"branch,omitempty" json:"branch,omitempty"`
	LastRun   string `yaml:"last_run,omitempty" json:"last_run,omitempty"` // id of the last swim run
	Lanes     []Lane `yaml:"lanes" json:"lanes"`
}

// Dir is the per-repo state directory.
func Dir(root string) string { return filepath.Join(root, ".swim") }

// Path is the status file location.
func Path(root string) string { return filepath.Join(Dir(root), "status.yml") }

// Now formats a timestamp the way status.yml and logs record it.
func Now() string { return time.Now().UTC().Format(time.RFC3339) }

// Load reads status.yml. A missing file yields an empty document.
func Load(root string) (*File, error) {
	f := &File{Root: root}
	data, err := os.ReadFile(Path(root))
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	return parse(root, data)
}

// parse is status.yml's bytes as a document: JSON, or the YAML an older
// swim wrote (the next update writes it as JSON).
func parse(root string, data []byte) (*File, error) {
	f := &File{Root: root}
	var err error
	if t := bytes.TrimSpace(data); len(t) > 0 && t[0] == '{' {
		err = json.Unmarshal(data, f)
	} else {
		err = yaml.Unmarshal(data, f)
	}
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", Path(root), err)
	}
	return f, nil
}

// Normalize makes sure lanes 1..n are present, in order, with their file
// names filled in. Entries beyond n (from an earlier, larger config) are kept.
func (f *File) Normalize(n int) {
	have := map[int]bool{}
	for _, l := range f.Lanes {
		have[l.Lane] = true
	}
	for i := 1; i <= n; i++ {
		if !have[i] {
			f.Lanes = append(f.Lanes, Lane{Lane: i, State: Idle})
		}
	}
	sort.Slice(f.Lanes, func(a, b int) bool { return f.Lanes[a].Lane < f.Lanes[b].Lane })
	for i := range f.Lanes {
		l := &f.Lanes[i]
		if l.State == "" {
			l.State = Idle
		}
		// The script's name (lane.3.py) is filled in by swim from the file
		// on disk; lane.N.sh until then.
		if base := fmt.Sprintf("lane.%d", l.Lane); l.Script != base && !strings.HasPrefix(l.Script, base+".") {
			l.Script = base + ".sh"
		}
		l.Log = fmt.Sprintf(".swim/logs/agent%d.log", l.Lane)
		if l.WaitingOn == nil {
			l.WaitingOn = []int{}
		}
		if l.FailedSteps == nil {
			l.FailedSteps = []string{}
		}
	}
}

// Get returns lane n's entry, or nil.
func (f *File) Get(n int) *Lane {
	for i := range f.Lanes {
		if f.Lanes[i].Lane == n {
			return &f.Lanes[i]
		}
	}
	return nil
}

// UpdateFile applies fn to the whole document under the lock, after making
// sure lanes 1..n exist, and writes it back atomically.
func UpdateFile(root string, n int, fn func(*File) error) error {
	return updateFile(root, n, true, fn)
}

// Rebuild is UpdateFile for swim status --rebuild, which reconstructs
// every entry from the logs: it may move a lane to any state, so it isn't
// checked against the state machine (Transition).
func Rebuild(root string, n int, fn func(*File) error) error {
	return updateFile(root, n, false, fn)
}

func updateFile(root string, n int, check bool, fn func(*File) error) error {
	if err := os.MkdirAll(Dir(root), 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(Dir(root), "status.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock status: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	old, err := os.ReadFile(Path(root))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := parse(root, old) // read once
	if err != nil {
		return err
	}
	f.Root = root
	f.Normalize(n)
	var before *File
	if check {
		before = f.clone()
	}
	if err := fn(f); err != nil {
		return err
	}
	if check {
		enforce(checkTransitions(before, f))
	}
	// Nothing changed: leave the file (and its updated_at) as it is, so a
	// command that only looks (swim report, swim status) writes nothing.
	if data, err := render(f); err == nil && bytes.Equal(data, old) {
		return nil
	}
	f.UpdatedAt = Now()
	return write(root, f)
}

// Update applies fn to lane n's entry.
func Update(root string, n, lanes int, fn func(*Lane)) error {
	if n > lanes {
		lanes = n
	}
	return UpdateFile(root, lanes, func(f *File) error {
		fn(f.Get(n))
		return nil
	})
}

func write(root string, f *File) error {
	data, err := render(f)
	if err != nil {
		return err
	}
	return fsutil.WriteAtomicAt(failpoint.StatusMidWrite, Path(root), data, 0o644)
}

// render is status.yml's bytes for f: indented JSON, which is YAML too,
// so whatever reads status.yml as YAML still can; swim writes and reads it
// as JSON, about ten times faster, and every status update does both
// under one lock.
func render(f *File) ([]byte, error) {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Alive reports whether the lane's recorded process (PID, on Host) may
// still be running: it's alive here, or it's on another machine, which
// this one can't check, so it counts as alive (swim never ends another
// machine's round for it).
func (l *Lane) Alive() bool {
	if l.PID <= 0 {
		return false
	}
	return !proc.Local(l.Host) || proc.Alive(l.PID)
}

// ResetRun clears the per-run fields before a lane starts or is reset.
func (l *Lane) ResetRun() {
	l.WaitingOn = []int{}
	l.Reason = ""
	l.PID, l.Host = 0, ""
	l.StartedAt, l.FinishedAt, l.DurationS, l.ExitCode = nil, nil, nil, nil
	l.Pass, l.Fail, l.Skip, l.Drift = 0, 0, 0, 0
	l.CurrentStep = ""
	l.Stage = ""
	l.Stages = nil
	l.Handlers = nil
	l.Message, l.Progress, l.MessageAt = "", "", ""
	l.FailedSteps = []string{}
	l.AutoApproved, l.AutoVia, l.FlagApproved = nil, "", nil
	l.DryRun = nil
}

// Dry records that guard flag's step ran as a dry run (each flag once).
func (l *Lane) Dry(flag string) {
	for _, f := range l.DryRun {
		if f == flag {
			return
		}
	}
	l.DryRun = append(l.DryRun, flag)
}

// Unprompted records that guard flag was approved without a prompt: by -y
// when via names it ("swim run -y"), else by a guard flag. Each flag is
// listed once.
func (l *Lane) Unprompted(flag, via string) {
	list := &l.FlagApproved
	if via != "" {
		list, l.AutoVia = &l.AutoApproved, via
	}
	for _, f := range *list {
		if f == flag {
			return
		}
	}
	*list = append(*list, flag)
}

// UnpromptedText sums up the guards approved without a prompt: "DEPLOY,
// MIGRATE (swim run -y); SEED (flag set)" ("" for none).
func (l *Lane) UnpromptedText() string {
	var parts []string
	if len(l.AutoApproved) > 0 {
		parts = append(parts, strings.Join(l.AutoApproved, ", ")+" ("+l.AutoVia+")")
	}
	if len(l.FlagApproved) > 0 {
		parts = append(parts, strings.Join(l.FlagApproved, ", ")+" (flag set)")
	}
	return strings.Join(parts, "; ")
}

// Ptr helpers for the nullable fields.
func Str(s string) *string     { return &s }
func Int(i int) *int           { return &i }
func Float(f float64) *float64 { return &f }
