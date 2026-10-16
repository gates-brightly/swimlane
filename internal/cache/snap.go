package cache

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Snapshots: an installed tree (node_modules, a virtualenv) a lane saves
// whole and restores in a later round, when the files it was installed from
// haven't changed. Unlike a preset's download cache, which the tool checks
// on use, a restored tree runs as it is: nothing checks it. So a snapshot is
// keyed by everything it was made from (its tree, the hashes of its @
// files, OS, architecture and --with strings), written once and never
// overwritten, never saved from an outcome handler, kept per repo only,
// and every restore goes in the round's chain entry.
//
//	# Cache: snap:web/node_modules @web/package-lock.json

// SnapPreset is the # Cache: kind for snapshots (snap:<tree>).
const SnapPreset = "snap"

// SnapSchema names a snapshot's record (snapshot.yml).
const SnapSchema = "swim.cache-snapshot/v1"

// SnapName is the cache name (a directory in the repo's caches) holding
// tree's snapshots, one per key.
func SnapName(tree string) string {
	return SnapPreset + "." + strings.Trim(slugRE.ReplaceAllString(strings.ReplaceAll(filepath.ToSlash(tree), "/", "_"), "-"), "-")
}

// Known reports whether name is a cache a repo keeps: a preset, or a
// snapshot set (snap.<tree>).
func Known(name string) bool { return known(name) }

// known reports whether name is a cache this package keeps: a preset, or
// a snapshot set (snap.<tree>).
func known(name string) bool {
	if _, ok := Lookup(name); ok {
		return true
	}
	rest, ok := strings.CutPrefix(name, SnapPreset+".")
	return ok && rest != "" && !strings.ContainsAny(rest, "/\\") && rest != "." && rest != ".."
}

// Snapshot is a saved tree's record.
type Snapshot struct {
	Schema     string   `yaml:"schema"`
	Key        string   `yaml:"key"`
	Tree       string   `yaml:"tree"`
	TreeSHA256 string   `yaml:"tree_sha256"`
	Files      int      `yaml:"files"`
	Bytes      int64    `yaml:"bytes"`
	OS         string   `yaml:"os"`
	Arch       string   `yaml:"arch"`
	With       []string `yaml:"with,omitempty"`
	SavedBy    string   `yaml:"saved_by"`
	SavedByJob string   `yaml:"saved_by_job,omitempty"`
	SavedRun   string   `yaml:"saved_run,omitempty"`
	SavedAt    string   `yaml:"saved_at"`
	// Failed: the round that saved it had failed steps when it did.
	Failed bool `yaml:"failed,omitempty"`
}

// SnapKey is tree's snapshot key: its name, each key file's path and
// SHA-256 (a missing one counts as missing), the OS, the architecture and
// the --with strings, hashed.
func SnapKey(root, tree string, files, with []string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00tree %s\x00%s/%s\x00", SnapSchema, tree, runtime.GOOS, runtime.GOARCH)
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(root, f))
		sum := "(missing)"
		if err == nil {
			s := sha256.Sum256(data)
			sum = hex.EncodeToString(s[:])
		}
		fmt.Fprintf(h, "file %s %s\x00", f, sum)
	}
	for _, w := range with {
		fmt.Fprintf(h, "with %s\x00", w)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ErrSnapExists: a snapshot with this key is saved already (keys are
// written once).
var ErrSnapExists = errors.New("a snapshot with this key is saved already")

// SaveSnap saves root/tree as the snapshot with key s.Key: copied into a
// temporary directory, hashed, then renamed into place, so a reader never
// sees half a snapshot and a key is never overwritten (ErrSnapExists).
func (r *Repo) SaveSnap(root string, s Snapshot) (*Snapshot, error) {
	dir, err := r.Path(SnapName(s.Tree))
	if err != nil {
		return nil, err
	}
	dst := filepath.Join(dir, s.Key)
	if _, err := os.Stat(dst); err == nil {
		return readSnap(dst)
	}
	src := filepath.Join(root, s.Tree)
	if st, err := os.Lstat(src); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("%s isn't a directory in the repo", s.Tree)
	}
	tmp := filepath.Join(dir, ".tmp-"+randHex())
	if err := copyTree(src, filepath.Join(tmp, "tree")); err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	s.Schema, s.OS, s.Arch = SnapSchema, runtime.GOOS, runtime.GOARCH
	s.SavedAt = time.Now().UTC().Format(time.RFC3339)
	if s.TreeSHA256, s.Files, s.Bytes, err = treeHash(filepath.Join(tmp, "tree")); err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	data, _ := yaml.Marshal(s)
	if err := os.WriteFile(filepath.Join(tmp, "snapshot.yml"), data, 0o600); err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		makeWritable(tmp)
		os.RemoveAll(tmp)
		if _, serr := os.Stat(dst); serr == nil {
			return nil, ErrSnapExists // another round saved it first
		}
		return nil, err
	}
	return &s, nil
}

// ErrTreeExists: restore found the tree already in place and left it.
var ErrTreeExists = errors.New("the tree is there already")

// FindSnap is the snapshot saved under key for tree, or nil (a miss).
func (r *Repo) FindSnap(tree, key string) *Snapshot {
	if !known(SnapName(tree)) {
		return nil
	}
	s, err := readSnap(filepath.Join(r.Dir, SnapName(tree), key))
	if err != nil {
		return nil
	}
	return s
}

// RestoreSnap copies snapshot s into root/s.Tree: into a temporary sibling
// first, then renamed into place. A tree already there is left alone
// (ErrTreeExists): restoring never deletes the repo's files.
func (r *Repo) RestoreSnap(root string, s *Snapshot) error {
	dst := filepath.Join(root, s.Tree)
	if ents, err := os.ReadDir(dst); err == nil && len(ents) > 0 {
		return ErrTreeExists
	}
	src := filepath.Join(r.Dir, SnapName(s.Tree), s.Key, "tree")
	tmp := filepath.Join(filepath.Dir(dst), "."+filepath.Base(dst)+".restore-"+randHex())
	if err := copyTree(src, tmp); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	os.Remove(dst) // an empty directory, if any
	if err := os.Rename(tmp, dst); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	return nil
}

func readSnap(dir string) (*Snapshot, error) {
	data, err := os.ReadFile(filepath.Join(dir, "snapshot.yml"))
	if err != nil {
		return nil, err
	}
	var s Snapshot
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func randHex() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// copyTree copies the directory src to dst (which must not exist): files
// with their modes, directories, and symlinks as links (never followed).
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		to := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(to, info.Mode().Perm()|0o700)
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(target, to)
		case info.Mode().IsRegular():
			return copyFile(p, to, info.Mode().Perm())
		}
		return nil // sockets, devices: not part of an installed tree
	})
}

func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// treeHash is a tree's SHA-256 over every entry's path, kind, mode and
// content (a link's target), in path order; with its file count and size.
func treeHash(dir string) (string, int, int64, error) {
	type ent struct{ rel, line string }
	var ents []ent
	files, size := 0, int64(0)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			ents = append(ents, ent{rel, fmt.Sprintf("d %s %o", filepath.ToSlash(rel), info.Mode().Perm())})
		case info.Mode()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			ents = append(ents, ent{rel, fmt.Sprintf("l %s %s", filepath.ToSlash(rel), target)})
		case info.Mode().IsRegular():
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			h := sha256.New()
			n, err := io.Copy(h, f)
			f.Close()
			if err != nil {
				return err
			}
			files++
			size += n
			ents = append(ents, ent{rel, fmt.Sprintf("f %s %o %x", filepath.ToSlash(rel), info.Mode().Perm(), h.Sum(nil))})
		}
		return nil
	})
	if err != nil {
		return "", 0, 0, err
	}
	sort.Slice(ents, func(a, b int) bool { return ents[a].rel < ents[b].rel })
	h := sha256.New()
	for _, e := range ents {
		fmt.Fprintln(h, e.line)
	}
	return hex.EncodeToString(h.Sum(nil)), files, size, nil
}
