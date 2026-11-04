package store

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/manifest"
)

// Keep stores what m pins, as lane m.Lane of run in root's repo (slug):
// every pinned file, the script as the chain hashes it (with its
// includes), and the staged manifest.yml. m is the launcher's staged
// manifest, or one built from the working tree for a lane run directly
// with bash (Staged false). The record's other fields are left as they
// are.
func (s *Store) Keep(slug, root, run string, m *manifest.Manifest) error {
	var files []File
	for _, f := range m.Files() {
		data, err := m.Bytes(f.Path)
		if err != nil {
			return err
		}
		sum, err := s.Put(data)
		if err != nil {
			return err
		}
		files = append(files, File{Path: f.Path, SHA256: sum})
	}
	staged := m.Dir != ""
	var script []byte
	var err error
	if staged {
		script, err = m.ScriptBytes()
	} else {
		script, err = lane.ScriptBytes(root, m.Lane)
	}
	if err != nil {
		return err
	}
	scriptSum, err := s.Put(script)
	if err != nil {
		return err
	}
	mfSum := ""
	if staged {
		data, err := os.ReadFile(filepath.Join(m.Dir, manifest.FileName))
		if err != nil {
			return err
		}
		if mfSum, err = s.Put(data); err != nil {
			return err
		}
	}
	return s.Update(slug, run, m.Lane, func(r *Round) {
		if r.Staged && !staged {
			return // what the launcher staged is what ran, not the working tree now
		}
		r.Repo = root
		if m.Job != "" {
			r.Job = m.Job
		}
		r.Script, r.Manifest, r.ManifestFile = scriptSum, m.SHA256, mfSum
		r.Staged, r.Files = staged, files
		if r.StoredAt == "" {
			r.StoredAt = time.Now().UTC().Format(time.RFC3339)
		}
	})
}

// Finish stores a round's log section and how it ended, in its record.
func (s *Store) Finish(slug, run string, n int, job, title, result string, exit int, logSection string) error {
	sum, err := s.Put([]byte(logSection))
	if err != nil {
		return err
	}
	return s.Update(slug, run, n, func(r *Round) {
		now := time.Now().UTC().Format(time.RFC3339)
		if r.StoredAt == "" {
			r.StoredAt = now
		}
		if job != "" {
			r.Job = job
		}
		r.Round, r.Result, r.Exit, r.Log, r.FinishedAt = title, result, &exit, sum, now
	})
}

// Problem is something wrong with what's kept.
type Problem struct {
	Run  string
	Lane int
	What string
}

func (p Problem) String() string { return fmt.Sprintf("%s lane %d: %s", p.Run, p.Lane, p.What) }

// Check reads every object r names and reports any missing or changed.
func (s *Store) Check(r *Round) []Problem {
	var out []Problem
	for _, o := range r.Objects() {
		if _, err := s.Get(o.SHA256); err != nil {
			out = append(out, Problem{r.Run, r.Lane, fmt.Sprintf("%s (%s): %v", o.What, short(o.SHA256), err)})
		}
	}
	return out
}

func short(sum string) string { return sum[:min(12, len(sum))] }
