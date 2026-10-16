// Package runrec keeps a record of each swim run: when every lane became
// ready, waited for locks and slots, started and finished (millisecond
// precision), what it waited on, and how it ended. The launcher writes it to
// .swim/runs/<run id>.yml when the run ends; swim timeline reads it.
package runrec

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// Schema identifies the record format.
const Schema = "swim.runrec/v1"

// Lane is one lane's part in the run. Times are seconds since the run
// started; a lane that never reached a point leaves it nil.
type Lane struct {
	Lane  int    `yaml:"lane"`
	Job   string `yaml:"job,omitempty"`
	Round string `yaml:"round,omitempty"`
	After []int  `yaml:"after,flow"` // dependencies inside this run
	// Finally marks a finally lane (# Finally:): its After lists the lanes
	// it waited for to end, however they ended.
	Finally bool     `yaml:"finally,omitempty"`
	Locks   []string `yaml:"locks,flow,omitempty"`
	State   string   `yaml:"state"` // passed | failed | skipped | interrupted
	Exit    *int     `yaml:"exit,omitempty"`
	Reason  string   `yaml:"reason,omitempty"`
	Ready   *float64 `yaml:"ready,omitempty"`    // dependencies met
	Locked  *float64 `yaml:"locked,omitempty"`   // locks taken
	Slot    *float64 `yaml:"slot,omitempty"`     // parallel slot taken
	Start   *float64 `yaml:"start,omitempty"`    // lane process started
	End     *float64 `yaml:"end,omitempty"`      // lane process finished (or skipped)
	Paused  float64  `yaml:"paused_s,omitempty"` // time its round was held by swim pause
	Pauses  []Span   `yaml:"pauses,omitempty"`   // each pause: start, seconds since the run started, and length
}

// Span is a stretch of a lane's time: its start (seconds since the run
// started) and length.
type Span struct {
	Start float64 `yaml:"start"`
	Dur   float64 `yaml:"duration_s"`
}

// Run is a whole run's record.
type Run struct {
	Schema      string    `yaml:"schema"`
	Run         string    `yaml:"run"`
	Started     time.Time `yaml:"started"`
	Finished    time.Time `yaml:"finished"`
	MaxParallel int       `yaml:"max_parallel,omitempty"`
	Lanes       []Lane    `yaml:"lanes"`
}

// Dir holds run records.
func Dir(root string) string { return filepath.Join(root, ".swim", "runs") }

// Path is a run's record file.
func Path(root, run string) string { return filepath.Join(Dir(root), run+".yml") }

// The run record's rules (docs/state.md): it's written once a run has
// ended, so every lane in it has ended (passed, failed, skipped or
// interrupted); rewritten for the same run (the same start), a lane's
// result never changes in place. A write that breaks them panics in tests
// and wherever SWIM_CHECK_STATES=1 is set; elsewhere it goes through.
const checkEnv = "SWIM_CHECK_STATES"

func ended(s string) bool {
	return s == "passed" || s == "failed" || s == "skipped" || s == "interrupted"
}

// Transition checks writing record to over record from (nil when there's
// none yet).
func Transition(from, to *Run) error {
	var bad []string
	for _, l := range to.Lanes {
		if !ended(l.State) {
			bad = append(bad, fmt.Sprintf("swim %d is %q in a record of a run that has ended", l.Lane, l.State))
		}
	}
	if from != nil && from.Started.Equal(to.Started) {
		was := map[int]string{}
		for _, l := range from.Lanes {
			was[l.Lane] = l.State
		}
		for _, l := range to.Lanes {
			if w, ok := was[l.Lane]; ok && w != l.State {
				bad = append(bad, fmt.Sprintf("swim %d's result can't change from %s to %s in the same run", l.Lane, w, l.State))
			}
		}
	}
	if len(bad) > 0 {
		return errors.New(strings.Join(bad, "; "))
	}
	return nil
}

// Write saves r atomically.
func Write(root string, r *Run) error {
	old, _, _ := Load(root, r.Run)
	if err := Transition(old, r); err != nil && (testing.Testing() || os.Getenv(checkEnv) == "1") {
		panic("run record " + r.Run + ": illegal state transition (docs/state.md): " + err.Error())
	}
	r.Schema = Schema
	sort.Slice(r.Lanes, func(a, b int) bool { return r.Lanes[a].Lane < r.Lanes[b].Lane })
	if err := os.MkdirAll(Dir(root), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(r)
	if err != nil {
		return err
	}
	tmp := Path(root, r.Run) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, Path(root, r.Run))
}

// Load reads a run's record; ok is false if there is none.
func Load(root, run string) (*Run, bool, error) {
	data, err := os.ReadFile(Path(root, run))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var r Run
	if err := yaml.Unmarshal(data, &r); err != nil {
		return nil, false, fmt.Errorf("%s: %w", Path(root, run), err)
	}
	return &r, true, nil
}

// List returns every recorded run, oldest first.
func List(root string) ([]*Run, error) {
	files, _ := filepath.Glob(filepath.Join(Dir(root), "*.yml"))
	var out []*Run
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var r Run
		if yaml.Unmarshal(data, &r) == nil && r.Run != "" {
			out = append(out, &r)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Started.Before(out[b].Started) })
	return out, nil
}

// Since converts an absolute time to seconds since start (nil if t is zero).
func Since(start, t time.Time) *float64 {
	if t.IsZero() {
		return nil
	}
	s := float64(t.Sub(start).Microseconds()) / 1e6
	return &s
}
