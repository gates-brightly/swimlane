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
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/fsutil"
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
	Lane        int      `yaml:"lane"`
	State       string   `yaml:"state"`
	Pending     string   `yaml:"pending"`                   // Round: line of the lane script on disk ("" for stub/none)
	PendingJob  string   `yaml:"pending_job"`               // Job: id of the lane script on disk
	Round       string   `yaml:"round"`                     // round of the last or current run
	Job         string   `yaml:"job"`                       // job id of the last or current run
	Run         string   `yaml:"run,omitempty"`             // swim run id of the last or current round
	Commit      string   `yaml:"commit,omitempty"`          // commit the round ran for (swim ci)
	ScriptSHA   string   `yaml:"script_sha256,omitempty"`   // SHA-256 of the lane script the round runs
	ManifestSHA string   `yaml:"manifest_sha256,omitempty"` // its execution manifest (internal/manifest)
	EnvSHA      string   `yaml:"env_sha256,omitempty"`      // SHA-256 of its env from env files ("" for none)
	Script      string   `yaml:"script"`
	Log         string   `yaml:"log"`
	WaitingOn   []int    `yaml:"waiting_on,flow"`
	Reason      string   `yaml:"reason,omitempty"`
	Locks       []string `yaml:"locks,flow,omitempty"` // locks the running round holds
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
	Stages      []string `yaml:"stages,flow,omitempty"`   // e.g. [snapshot PASS, check FAIL, change none, verify none]
	Handlers    []string `yaml:"handlers,flow,omitempty"` // outcome handlers that ran, e.g. [on_failure PASS, on_exit PASS]
	// Message is the running round's status message (swim msg), with its
	// progress ("7/25") and when it was posted; cleared when the round ends.
	Message   string `yaml:"message,omitempty"`
	Progress  string `yaml:"progress,omitempty"`
	MessageAt string `yaml:"message_at,omitempty"`
	// Outputs are the last round's outputs (# Outputs:, output KEY value):
	// name -> SHA-256, in .swim/out/<job>/<run>/.
	Outputs map[string]string `yaml:"outputs,omitempty"`
	// The last read-only check (swim plan --check): checked (it reached
	// change cleanly), stopped (a gate stopped it), failed, or refused; when;
	// the script it checked (stale once the script changes); the gate that
	// stopped it; and the guards it reached.
	CheckResult string   `yaml:"check_result,omitempty"`
	CheckAt     string   `yaml:"check_at,omitempty"`
	CheckSHA    string   `yaml:"check_sha256,omitempty"`
	CheckStop   string   `yaml:"check_stop,omitempty"`
	CheckGuards []string `yaml:"check_guards,flow,omitempty"`
	// The guards the round's run approved without a prompt: AutoApproved by
	// -y (swim run -y, a queue batch's -y, swim watch -y; AutoVia says
	// which), FlagApproved by a guard flag swim run's own environment set
	// (FLAG=1) or the queue batch approved. Cleared when a round starts.
	AutoApproved []string `yaml:"auto_approved,flow,omitempty"`
	AutoVia      string   `yaml:"auto_via,omitempty"`
	FlagApproved []string `yaml:"flag_approved,flow,omitempty"`
	// A matrix round's cells, each with its own state (Cell names it).
	Cell        string `yaml:"cell,omitempty"`
	Cells       []Lane `yaml:"cells,omitempty"`
	LastArchive string `yaml:"last_archive,omitempty"`
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
// CellsText sums up a matrix lane's cells: "2/3 passed · failed: billing ·
// running: users" ("" for a plain lane).
func (l *Lane) CellsText() string { return l.CellsTextAs(func(c string) string { return c }) }

// CellsTextAs is CellsText with each cell named by label (a job's id, 1.4).
func (l *Lane) CellsTextAs(label func(cell string) string) string {
	if len(l.Cells) == 0 {
		return ""
	}
	by := map[string][]string{}
	for _, c := range l.Cells {
		by[c.State] = append(by[c.State], label(c.Cell))
	}
	parts := []string{fmt.Sprintf("%d/%d passed", len(by[Passed]), len(l.Cells))}
	for _, st := range []string{Failed, Interrupted, Skipped, Running, Waiting, Locked} {
		if len(by[st]) > 0 {
			parts = append(parts, st+": "+strings.Join(by[st], ", "))
		}
	}
	return strings.Join(parts, " · ")
}

// CellEntry is the entry of the lane's cell name (created if new).
func (l *Lane) CellEntry(name string) *Lane {
	for i := range l.Cells {
		if l.Cells[i].Cell == name {
			return &l.Cells[i]
		}
	}
	l.Cells = append(l.Cells, Lane{Lane: l.Lane, Cell: name, State: Idle, WaitingOn: []int{}, FailedSteps: []string{}})
	return &l.Cells[len(l.Cells)-1]
}

// FindCell is the entry of the lane's cell name, or nil.
func (l *Lane) FindCell(name string) *Lane {
	for i := range l.Cells {
		if l.Cells[i].Cell == name {
			return &l.Cells[i]
		}
	}
	return nil
}

// Mine is lane n's entry as this process sees its round: in a matrix
// cell's process (SWIM_CELL set), that cell's entry.
func (f *File) Mine(n int) *Lane {
	l := f.Get(n)
	if c := os.Getenv("SWIM_CELL"); c != "" && l != nil {
		return l.CellEntry(c)
	}
	return l
}

func (f *File) Get(n int) *Lane {
	for i := range f.Lanes {
		if f.Lanes[i].Lane == n {
			return &f.Lanes[i]
		}
	}
	return nil
}

// UpdateCheck applies fn to lane n's entry even in a read-only check: for
// the check's own fields.
func UpdateCheck(root string, n, lanes int, fn func(*Lane)) error {
	return updateFile(root, max(n, lanes), func(f *File) error {
		fn(f.Mine(n))
		return nil
	})
}

// UpdateFile applies fn to the whole document under the lock, after making
// sure lanes 1..n exist, and writes it back atomically. Like Update, it
// does nothing in a read-only check.
func UpdateFile(root string, n int, fn func(*File) error) error {
	if os.Getenv("SWIM_CHECK_ONLY") == "1" {
		return nil
	}
	return updateFile(root, n, fn)
}

func updateFile(root string, n int, fn func(*File) error) error {
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

// Update applies fn to lane n's entry. In a read-only check (swim plan
// --check) it does nothing: a check never changes a lane's state; its
// result is written with UpdateCheck.
func Update(root string, n, lanes int, fn func(*Lane)) error {
	if os.Getenv("SWIM_CHECK_ONLY") == "1" {
		return nil
	}
	if n > lanes {
		lanes = n
	}
	return UpdateFile(root, lanes, func(f *File) error {
		fn(f.Mine(n))
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
	return fsutil.WriteAtomic(Path(root), buf.Bytes(), 0o644)
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
	l.Handlers = nil
	l.Message, l.Progress, l.MessageAt = "", "", ""
	l.FailedSteps = []string{}
	l.AutoApproved, l.AutoVia, l.FlagApproved = nil, "", nil
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
