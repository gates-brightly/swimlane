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
	"time"

	"gopkg.in/yaml.v3"
)

// Schema identifies the record format.
const Schema = "swim.runrec/v1"

// Lane is one lane's part in the run. Times are seconds since the run
// started; a lane that never reached a point leaves it nil.
type Lane struct {
	Lane   int      `yaml:"lane"`
	Job    string   `yaml:"job,omitempty"`
	Round  string   `yaml:"round,omitempty"`
	After  []int    `yaml:"after,flow"` // dependencies inside this run
	Locks  []string `yaml:"locks,flow,omitempty"`
	State  string   `yaml:"state"` // passed | failed | skipped | interrupted
	Exit   *int     `yaml:"exit,omitempty"`
	Reason string   `yaml:"reason,omitempty"`
	Ready  *float64 `yaml:"ready,omitempty"`  // dependencies met
	Locked *float64 `yaml:"locked,omitempty"` // locks taken
	Slot   *float64 `yaml:"slot,omitempty"`   // parallel slot taken
	Start  *float64 `yaml:"start,omitempty"`  // lane process started
	End    *float64 `yaml:"end,omitempty"`    // lane process finished (or skipped)
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

// Write saves r atomically.
func Write(root string, r *Run) error {
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
