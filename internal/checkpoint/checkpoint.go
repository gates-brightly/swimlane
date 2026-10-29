// Package checkpoint keeps, per lane, the steps marked as checkpoints
// (run --checkpoint) that passed in its current round: a later run of the
// same round skips each one (KEPT) instead of running it again, as long
// as its command is the same.
//
// The file is a cache of what the logs and the chain prove, not the proof:
// each kept step names the run it passed in, and swim verify checks that
// run's log for the pass. A checkpoint belongs to one round (its # Job:,
// or its Round: line for a script without one); a new round starts with
// none.
package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Schema names the file's format.
const Schema = "swim.checkpoints/v1"

// Step is one checkpointed step that passed.
type Step struct {
	Label string `json:"label"`
	// Cmd is the step's command hash (CmdHash): its arguments and the
	// files they name, as they were when it passed.
	Cmd string `json:"cmd_sha256"`
	Run string `json:"run"` // the swim run it passed in
	At  string `json:"at"`  // when it passed (RFC 3339, UTC)
}

// File is one lane's checkpoints.
type File struct {
	Schema string `json:"schema"`
	Lane   int    `json:"lane"`
	// Round is the round they belong to: "job <id>", or "round <title>"
	// for a script without a Job: line.
	Round string          `json:"round"`
	Steps map[string]Step `json:"steps"` // by key (--key, else the label)
}

// Dir holds the checkpoint files.
func Dir(root string) string { return filepath.Join(root, ".swim", "checkpoints") }

// Path is lane n's checkpoint file.
func Path(root string, n int) string { return filepath.Join(Dir(root), fmt.Sprintf("lane%d.json", n)) }

// Round is the identity of a lane's round, for checkpoints: its job id,
// or its Round: line when the script has no Job: line (each run then gets
// a new job id, as swim run matches such rounds by title).
func Round(job, title string) string {
	if job != "" {
		return "job " + job
	}
	return "round " + title
}

// Load reads lane n's checkpoints; a missing file is an empty one.
func Load(root string, n int) (*File, error) {
	f := &File{Schema: Schema, Lane: n, Steps: map[string]Step{}}
	data, err := os.ReadFile(Path(root, n))
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(data, f); err != nil {
		return &File{Schema: Schema, Lane: n, Steps: map[string]Step{}}, fmt.Errorf("%s: %v", Path(root, n), err)
	}
	if f.Steps == nil {
		f.Steps = map[string]Step{}
	}
	return f, nil
}

func save(root string, f *File) error {
	if err := os.MkdirAll(Dir(root), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := Path(root, f.Lane) + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, Path(root, f.Lane))
}

// Find is round's checkpoint for key in lane n, if it passed with the
// command hash cmd and (with maxAge) not longer ago than maxAge.
func Find(root string, n int, round, key, cmd string, maxAge time.Duration, now time.Time) (Step, bool) {
	f, err := Load(root, n)
	if err != nil || f.Round != round {
		return Step{}, false
	}
	s, ok := f.Steps[key]
	if !ok || s.Cmd != cmd {
		return Step{}, false
	}
	if maxAge > 0 {
		at, err := time.Parse(time.RFC3339, s.At)
		if err != nil || now.Sub(at) > maxAge {
			return Step{}, false
		}
	}
	return s, true
}

// Record notes that key passed in round, in lane n: a checkpoint of
// another round is dropped first.
func Record(root string, n int, round, key string, s Step) error {
	f, _ := Load(root, n)
	if f.Round != round {
		f.Round, f.Steps = round, map[string]Step{}
	}
	f.Steps[key] = s
	return save(root, f)
}

// Clear forgets all of lane n's checkpoints.
func Clear(root string, n int) error {
	err := os.Remove(Path(root, n))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Forget drops keys from lane n's checkpoints and returns those it had.
func Forget(root string, n int, keys []string) ([]string, error) {
	f, err := Load(root, n)
	if err != nil {
		return nil, err
	}
	var gone []string
	for _, k := range keys {
		if _, ok := f.Steps[k]; ok {
			delete(f.Steps, k)
			gone = append(gone, k)
		}
	}
	if len(gone) == 0 {
		return nil, nil
	}
	return gone, save(root, f)
}

// Keys lists f's keys in the order their steps passed.
func (f *File) Keys() []string {
	keys := make([]string, 0, len(f.Steps))
	for k := range f.Steps {
		keys = append(keys, k)
	}
	sort.SliceStable(keys, func(a, b int) bool {
		if f.Steps[keys[a]].At != f.Steps[keys[b]].At {
			return f.Steps[keys[a]].At < f.Steps[keys[b]].At
		}
		return keys[a] < keys[b]
	})
	return keys
}

// CmdHash is a step's command hash: its arguments, and the content of
// each argument that names a file in dir (the step's working directory,
// a lane's staged copy), so a script it runs that changed runs again.
func CmdHash(dir string, args []string) string {
	h := sha256.New()
	io.WriteString(h, "swim checkpoint v1\n")
	for _, a := range args {
		fmt.Fprintf(h, "arg %d %s\n", len(a), a)
	}
	for _, a := range args {
		if a == "" || filepath.IsAbs(a) {
			continue
		}
		p := filepath.Join(dir, a)
		st, err := os.Stat(p)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(h, "file %s %s\n", a, hex.EncodeToString(sum[:]))
	}
	return hex.EncodeToString(h.Sum(nil))
}
