// Package status owns .swim/status.yml, the last known state of every lane.
// Every swim process that changes a lane updates it under an exclusive
// flock and replaces it atomically, so parallel lanes never clobber each
// other and `cat` never sees a partial file.
package status

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// Lane states.
const (
	Idle        = "idle"
	Waiting     = "waiting"
	Queued      = "queued" // waiting for a free slot (max_parallel)
	Running     = "running"
	Passed      = "passed"
	Failed      = "failed"
	Skipped     = "skipped"
	Interrupted = "interrupted"
)

// Lane is one lane's entry. Fields without omitempty are always written so
// the file reads the same for every lane.
type Lane struct {
	Lane        int      `yaml:"lane"`
	State       string   `yaml:"state"`
	Pending     string   `yaml:"pending"`       // Round: line of the lane script on disk ("" for stub/none)
	PendingJob  string   `yaml:"pending_job"`   // Job: id of the lane script on disk
	Round       string   `yaml:"round"`         // round of the last or current run
	Job         string   `yaml:"job"`           // job id of the last or current run
	Run         string   `yaml:"run,omitempty"` // swim run id of the last or current round
	Script      string   `yaml:"script"`
	Log         string   `yaml:"log"`
	WaitingOn   []int    `yaml:"waiting_on,flow"`
	Reason      string   `yaml:"reason,omitempty"`
	PID         int      `yaml:"pid,omitempty"`
	StartedAt   *string  `yaml:"started_at"`
	FinishedAt  *string  `yaml:"finished_at"`
	DurationS   *float64 `yaml:"duration_s"`
	ExitCode    *int     `yaml:"exit_code"`
	Pass        int      `yaml:"pass"`
	Fail        int      `yaml:"fail"`
	Skip        int      `yaml:"skip"`
	Drift       int      `yaml:"drift"`
	Stage       string   `yaml:"stage,omitempty"` // stage the running round is in
	CurrentStep string   `yaml:"current_step"`
	FailedSteps []string `yaml:"failed_steps"`
	Stages      []string `yaml:"stages,flow,omitempty"` // e.g. [snapshot PASS, check FAIL, change none, verify none]
	LastArchive string   `yaml:"last_archive,omitempty"`
}

// File is the whole status.yml document.
type File struct {
	UpdatedAt string `yaml:"updated_at"`
	Root      string `yaml:"root"`
	Branch    string `yaml:"branch,omitempty"`
	LastRun   string `yaml:"last_run,omitempty"` // id of the last swim run
	Lanes     []Lane `yaml:"lanes"`
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
	if err := yaml.Unmarshal(data, f); err != nil {
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
		l.Script = fmt.Sprintf("lane.%d.sh", l.Lane)
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

	f, err := Load(root)
	if err != nil {
		return err
	}
	f.Root = root
	f.Normalize(n)
	if err := fn(f); err != nil {
		return err
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
	var buf bytes.Buffer
	buf.WriteString("# swim status — last known state of every lane. Written by swim; do not edit.\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(Dir(root), "status-*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), Path(root))
}

// ResetRun clears the per-run fields before a lane starts or is reset.
func (l *Lane) ResetRun() {
	l.WaitingOn = []int{}
	l.Reason = ""
	l.PID = 0
	l.StartedAt, l.FinishedAt, l.DurationS, l.ExitCode = nil, nil, nil, nil
	l.Pass, l.Fail, l.Skip, l.Drift = 0, 0, 0, 0
	l.CurrentStep = ""
	l.Stage = ""
	l.Stages = nil
	l.FailedSteps = []string{}
}

// Ptr helpers for the nullable fields.
func Str(s string) *string     { return &s }
func Int(i int) *int           { return &i }
func Float(f float64) *float64 { return &f }
