// Package store keeps, outside the repo, the bytes behind the chain of
// custody: every file a round ran and the log section it wrote, so what a
// chain entry's hashes name can still be read after the repo loses them
// (swim stub, swim clean, git clean, a deleted checkout).
//
// Objects are content-addressed (objects/<aa>/<sha256>, read-only): a
// script run a hundred times is kept once, and an object proves itself by
// its name. Each round has a record (repos/<slug>/<run>/<lane>.yml) naming
// its objects with the hashes its chain entry holds. The launcher writes
// the record as it stages a lane, before anything runs; the round's
// summary adds its log and how it ended. A round killed before its summary
// still has what it ran.
//
// The store lives in the operator's profile ($XDG_DATA_HOME/swim/store,
// ~/.local/share/swim/store; store_dir in config.yml), 0700: it holds
// prod output from every repo. Nothing is ever removed.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/fsutil"
)

// Schema is a round record's schema.
const Schema = "swim.store/v1"

// File is one file a round ran: its repo-relative path and its object.
type File struct {
	Path   string `yaml:"path"`
	SHA256 string `yaml:"sha256"`
}

// Round is one round's record. Script, Manifest and Log are the hashes
// its chain entry holds (script_sha256 is lane.ScriptBytes, the script
// with its includes; log the round's section of the lane log).
type Round struct {
	Schema   string `yaml:"schema"`
	Repo     string `yaml:"repo"` // the repo root it ran in
	Run      string `yaml:"run"`
	Lane     int    `yaml:"lane"`
	Job      string `yaml:"job,omitempty"`
	Round    string `yaml:"round,omitempty"`
	Script   string `yaml:"script_sha256,omitempty"`
	Manifest string `yaml:"manifest_sha256,omitempty"`
	// Staged is false for a lane run directly with bash: its files were
	// read from the working tree when the round ended, as its chain entry's
	// hashes were.
	Staged bool `yaml:"staged"`
	// ManifestFile is the staged manifest.yml's own bytes.
	ManifestFile string `yaml:"manifest_file_sha256,omitempty"`
	Files        []File `yaml:"files,omitempty"`
	StoredAt     string `yaml:"stored_at"`
	FinishedAt   string `yaml:"finished_at,omitempty"`
	Result       string `yaml:"result,omitempty"`
	Exit         *int   `yaml:"exit,omitempty"`
	Log          string `yaml:"log_sha256,omitempty"`
}

// Object is a hash the record names, and what it is.
type Object struct {
	What   string // "script", "manifest", "log", or a file's path
	SHA256 string
}

// Objects are every object r names.
func (r *Round) Objects() []Object {
	var out []Object
	add := func(what, sum string) {
		if sum != "" {
			out = append(out, Object{what, sum})
		}
	}
	add("script", r.Script)
	add("manifest", r.ManifestFile)
	for _, f := range r.Files {
		add(f.Path, f.SHA256)
	}
	add("log", r.Log)
	return out
}

// Store is one store directory.
type Store struct{ Dir string }

// Open is the store for cfg's repo (config.StoreBase), and the repo's
// name in it (config.RepoSlug: worktrees and clones of one repo share it).
// nil when store_dir is off.
func Open(cfg *config.Config) (*Store, string, error) {
	dir, ok, err := cfg.StoreBase()
	if err != nil || !ok {
		return nil, "", err
	}
	slug, err := cfg.RepoSlug()
	if err != nil {
		return nil, "", err
	}
	return &Store{Dir: dir}, slug, nil
}

// Sum is the SHA-256 (hex) of data, as the chain hashes.
func Sum(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

func (s *Store) objectPath(sum string) string {
	return filepath.Join(s.Dir, "objects", sum[:2], sum)
}

// Put keeps data and returns its hash. An object already kept is left as
// it is.
func (s *Store) Put(data []byte) (string, error) {
	sum := Sum(data)
	path := s.objectPath(sum)
	if _, err := os.Stat(path); err == nil {
		return sum, nil
	}
	if err := writeAtomic(path, data, 0o400); err != nil {
		return "", err
	}
	return sum, nil
}

// Get reads the object sum, and fails if its bytes no longer hash to it.
func (s *Store) Get(sum string) ([]byte, error) {
	if !validSum(sum) {
		return nil, fmt.Errorf("%q isn't a SHA-256", sum)
	}
	data, err := os.ReadFile(s.objectPath(sum))
	if err != nil {
		return nil, err
	}
	if got := Sum(data); got != sum {
		return data, fmt.Errorf("object %s was changed: its bytes hash to %s", sum[:12], got[:12])
	}
	return data, nil
}

func validSum(sum string) bool {
	if len(sum) != 64 {
		return false
	}
	_, err := hex.DecodeString(sum)
	return err == nil
}

func (s *Store) recordPath(slug, run string, n int) string {
	return filepath.Join(s.Dir, "repos", slug, run, strconv.Itoa(n)+".yml")
}

// Update changes round n of run's record (a new one if there is none)
// with fn, and writes it.
func (s *Store) Update(slug, run string, n int, fn func(*Round)) error {
	if slug == "" || run == "" || strings.ContainsAny(slug+run, `/\`) || strings.HasPrefix(run, ".") {
		return fmt.Errorf("store: can't record run %q of %q", run, slug)
	}
	r, err := s.Load(slug, run, n)
	if errors.Is(err, fs.ErrNotExist) {
		r, err = &Round{Run: run, Lane: n}, nil
	}
	if err != nil {
		return err
	}
	fn(r)
	r.Schema, r.Run, r.Lane = Schema, run, n
	data, err := yaml.Marshal(r)
	if err != nil {
		return err
	}
	return writeAtomic(s.recordPath(slug, run, n), data, 0o600)
}

// Load reads round n of run's record.
func (s *Store) Load(slug, run string, n int) (*Round, error) {
	data, err := os.ReadFile(s.recordPath(slug, run, n))
	if err != nil {
		return nil, err
	}
	var r Round
	if err := yaml.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", s.recordPath(slug, run, n), err)
	}
	if r.Schema != Schema {
		return nil, fmt.Errorf("%s: not a swim store record (schema %q)", s.recordPath(slug, run, n), r.Schema)
	}
	return &r, nil
}

// Rounds are every record kept for slug's repo, oldest first. A record
// that doesn't read is an error at the end; the rest are returned.
func (s *Store) Rounds(slug string) ([]*Round, error) {
	dir := filepath.Join(s.Dir, "repos", slug)
	runs, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Round
	var bad []error
	for _, run := range runs {
		lanes, err := os.ReadDir(filepath.Join(dir, run.Name()))
		if err != nil {
			continue
		}
		for _, l := range lanes {
			n, err := strconv.Atoi(strings.TrimSuffix(l.Name(), ".yml"))
			if err != nil || !strings.HasSuffix(l.Name(), ".yml") {
				continue
			}
			r, err := s.Load(slug, run.Name(), n)
			if err != nil {
				bad = append(bad, err)
				continue
			}
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StoredAt != out[j].StoredAt {
			return out[i].StoredAt < out[j].StoredAt
		}
		return out[i].Lane < out[j].Lane
	})
	return out, errors.Join(bad...)
}

// writeAtomic writes data to path (fsutil.WriteAtomic), making its
// directories 0700.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return fsutil.WriteAtomic(path, data, mode)
}
