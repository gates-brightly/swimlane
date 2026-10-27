// Package earmark keeps a reader's place in a repo's record: how far into
// .swim.log, and into each lane's log, a reader (swim report --earmark)
// has read. An earmark is a cursor, not an edit: nothing is ever cut from a
// log to make a report smaller. It knows nothing of the logs themselves.
package earmark

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/chars"
	"github.com/gates-brightly/swimlane/internal/fsutil"
)

// Schema names the earmark file's format.
const Schema = "swim.earmark/v1"

// Default is the name of a reader that gives none.
const Default = "default"

// SessionEnv names the reader when --name doesn't (as lane claims do).
const SessionEnv = "SWIM_SESSION"

// Cursor is one reader's place.
type Cursor struct {
	Schema  string         `yaml:"schema"`
	Name    string         `yaml:"name"`
	At      string         `yaml:"at"` // UTC, RFC 3339: when it was written
	History HistoryPos     `yaml:"history"`
	Lanes   map[string]Pos `yaml:"lanes,omitempty"` // "1", or "1[job]" for a job's log
}

// HistoryPos is how much of .swim.log was read: Offset bytes, the last
// line read hashing to SHA.
type HistoryPos struct {
	Offset int64  `yaml:"offset"`
	SHA    string `yaml:"sha,omitempty"`
}

// Pos is a place in one lane log, by round rather than by file (an
// archive renames the file, never a round): Round is the round's first
// line, Lines how many of its lines were read, the last hashing to SHA.
// State and Job are the lane's, as status.yml had them (lane entries only):
// what the next report's status compares against.
type Pos struct {
	Round string `yaml:"round,omitempty"`
	Lines int    `yaml:"lines,omitempty"`
	SHA   string `yaml:"sha,omitempty"`
	State string `yaml:"state,omitempty"`
	Job   string `yaml:"job,omitempty"`
}

// Dir holds root's earmarks.
func Dir(root string) string { return filepath.Join(root, ".swim", "earmarks") }

// Name is the reader's name: flag, else $SWIM_SESSION, else Default.
func Name(flag string) (string, error) {
	n := flag
	if n == "" {
		n = os.Getenv(SessionEnv)
	}
	if n == "" {
		return Default, nil
	}
	if !chars.Name(n, 64) {
		return "", fmt.Errorf("earmark name %q: use letters, digits, '.', '_' or '-' (up to 64)", n)
	}
	return n, nil
}

func path(root, name string) string { return filepath.Join(Dir(root), name+".yml") }

// Load reads name's earmark; nil when it has none.
func Load(root, name string) (*Cursor, error) {
	data, err := os.ReadFile(path(root, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c Cursor
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("%s: %w (swim report --earmark --reset starts it over)", path(root, name), err)
	}
	if c.Lanes == nil {
		c.Lanes = map[string]Pos{}
	}
	return &c, nil
}

// Save writes c, durably and atomically.
func Save(root string, c *Cursor) error {
	c.Schema = Schema
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(Dir(root), 0o755); err != nil {
		return err
	}
	return fsutil.WriteAtomic(path(root, c.Name), data, 0o644)
}

// Remove deletes name's earmark; ok is false when it had none.
func Remove(root, name string) (ok bool, err error) {
	err = os.Remove(path(root, name))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// List returns every earmark, by name.
func List(root string) ([]*Cursor, error) {
	files, _ := filepath.Glob(filepath.Join(Dir(root), "*.yml"))
	var out []*Cursor
	for _, f := range files {
		c, err := Load(root, strings.TrimSuffix(filepath.Base(f), ".yml"))
		if err != nil {
			return nil, err
		}
		if c != nil {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
