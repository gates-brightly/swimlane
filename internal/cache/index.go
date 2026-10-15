package cache

import (
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/fsutil"
)

// IndexSchema identifies a repo's index.yml.
const IndexSchema = "swim.cache-index/v1"

// index is a repo's index.yml: when each preset was last used, and by
// which lanes. Eviction goes by it, least recently used first.
type index struct {
	Schema  string                `yaml:"schema"`
	Presets map[string]indexEntry `yaml:"presets"`
}

type indexEntry struct {
	LastUsed string `yaml:"last_used"`
	Lanes    []int  `yaml:"lanes,omitempty"`
}

// now is the clock; tests replace it.
var now = time.Now

func (r *Repo) indexPath() string { return filepath.Join(r.Dir, "index.yml") }

func (r *Repo) readIndex() (*index, error) {
	idx := &index{Schema: IndexSchema, Presets: map[string]indexEntry{}}
	data, err := os.ReadFile(r.indexPath())
	if os.IsNotExist(err) {
		return idx, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, idx); err != nil {
		// A cache index is a convenience: a broken one starts over.
		return &index{Schema: IndexSchema, Presets: map[string]indexEntry{}}, nil
	}
	if idx.Presets == nil {
		idx.Presets = map[string]indexEntry{}
	}
	return idx, nil
}

// update changes the index under its lock.
func (r *Repo) update(fn func(*index)) error {
	f, err := openLock(filepath.Join(r.Dir, ".locks", "index"))
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	idx, err := r.readIndex()
	if err != nil {
		return err
	}
	fn(idx)
	data, err := yaml.Marshal(idx)
	if err != nil {
		return err
	}
	return fsutil.WriteAtomic(r.indexPath(), data, 0o600)
}

// Touch records that lane used preset name now.
func (r *Repo) Touch(name string, lane int) error {
	return r.update(func(idx *index) {
		e := idx.Presets[name]
		e.LastUsed = now().UTC().Format(time.RFC3339)
		if lane > 0 && !slices.Contains(e.Lanes, lane) {
			e.Lanes = append(e.Lanes, lane)
			slices.Sort(e.Lanes)
		}
		idx.Presets[name] = e
	})
}

func (r *Repo) forget(name string) error {
	return r.update(func(idx *index) { delete(idx.Presets, name) })
}

// Prune removes the preset caches last used before cutoff (zero: none),
// then evicts down to max bytes (0: no cap). A cache a running lane holds
// is never removed. It returns what it removed.
func (r *Repo) Prune(cutoff time.Time, max int64) ([]string, error) {
	var gone []string
	if !cutoff.IsZero() {
		list, err := r.List()
		if err != nil {
			return nil, err
		}
		for _, c := range list {
			t, err := time.Parse(time.RFC3339, c.LastUsed)
			if c.InUse || (err == nil && !t.Before(cutoff)) {
				continue
			}
			if r.Remove(c.Name) == nil {
				gone = append(gone, c.Name)
			}
		}
	}
	more, err := r.Evict(max)
	return append(gone, more...), err
}
