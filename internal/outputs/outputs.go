// Package outputs keeps the files rounds produce for the lanes that depend
// on them. Each round gets a fresh directory, .swim/out/<job>/<run>/
// ($SWIM_OUT); its header's Outputs: names what it promises. When the round
// ends swim checks and hashes them (a directory as a sorted manifest),
// makes them read-only, and records the hashes in status and the chain. A
// dependent lane asks for one with `input N name`; what it read, and the
// hash it read, is recorded too, so `swim verify` can prove the report was
// built from exactly those stats.
package outputs

import (
	"bufio"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Internal entries in $SWIM_OUT, never outputs themselves.
const (
	valuesDir  = ".swim-values" // output KEY value: one file per KEY
	inputsFile = ".swim-inputs" // what the round read with input
)

// ValuePrefix marks a small value (output KEY value) among a round's
// output hashes: "value:KEY".
const ValuePrefix = "value:"

// Dir is the outputs directory of job's round in run.
func Dir(root, job, run string) string {
	return filepath.Join(root, ".swim", "out", job, run)
}

var keyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)

// ValidKey reports whether key can name a small value.
func ValidKey(key string) bool { return keyRE.MatchString(key) }

// ValidName reports whether an Outputs: entry is a relative path inside
// $SWIM_OUT (no absolute paths, no "..").
func ValidName(name string) bool {
	name = strings.TrimSuffix(name, "/")
	if name == "" || filepath.IsAbs(name) || strings.HasPrefix(name, ".swim-") {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		if part == ".." || part == "." || part == "" {
			return false
		}
	}
	return true
}

// SetValue records a small value (output KEY value) in dir.
func SetValue(dir, key, value string) error {
	if !ValidKey(key) {
		return fmt.Errorf("output %q: a key is letters, digits and . _ -", key)
	}
	vd := filepath.Join(dir, valuesDir)
	if err := os.MkdirAll(vd, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(vd, key), []byte(value), 0o644)
}

// Value reads a small value from dir.
func Value(dir, key string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(dir, valuesDir, key))
	return string(data), err == nil
}

// Hash hashes an output: a file's SHA-256, or for a directory the SHA-256
// of its sorted manifest ("<path> <sha256>" lines). It also returns the
// total size and file count.
func Hash(path string) (sum string, size int64, files int, err error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", 0, 0, err
	}
	if !st.IsDir() {
		sum, err := hashFile(path)
		return sum, st.Size(), 1, err
	}
	var lines []string
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		h, err := hashFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(path, p)
		lines = append(lines, filepath.ToSlash(rel)+" "+h)
		size += info.Size()
		files++
		return nil
	})
	if err != nil {
		return "", 0, 0, err
	}
	sort.Strings(lines)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(lines, "\n")+"\n"))), size, files, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// Output is one checked output.
type Output struct {
	Name  string
	Sum   string
	Size  int64
	Files int
}

// Result is a round's outputs as checked when it ends.
type Result struct {
	Outputs    []Output // declared outputs present, and values, by name
	Missing    []string // declared but not there
	Undeclared []string // top-level entries nobody declared (kept, not offered)
	Size       int64
}

// Sums are the outputs' hashes by name (values as "value:KEY").
func (r Result) Sums() map[string]string {
	if len(r.Outputs) == 0 {
		return nil
	}
	m := map[string]string{}
	for _, o := range r.Outputs {
		m[o.Name] = o.Sum
	}
	return m
}

// Check checks dir against the declared outputs and hashes what's there.
func Check(dir string, declared []string) (Result, error) {
	var r Result
	want := map[string]bool{}
	for _, name := range declared {
		name = strings.TrimSuffix(name, "/")
		want[strings.SplitN(filepath.ToSlash(name), "/", 2)[0]] = true
		sum, size, files, err := Hash(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			r.Missing = append(r.Missing, name)
			continue
		}
		if err != nil {
			return r, err
		}
		r.Outputs = append(r.Outputs, Output{Name: name, Sum: sum, Size: size, Files: files})
		r.Size += size
	}
	entries, _ := os.ReadDir(filepath.Join(dir, valuesDir))
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, valuesDir, e.Name()))
		if err != nil {
			continue
		}
		r.Outputs = append(r.Outputs, Output{Name: ValuePrefix + e.Name(), Sum: fmt.Sprintf("%x", sha256.Sum256(data)), Size: int64(len(data)), Files: 1})
	}
	top, _ := os.ReadDir(dir)
	for _, e := range top {
		if !want[e.Name()] && !strings.HasPrefix(e.Name(), ".swim-") {
			r.Undeclared = append(r.Undeclared, e.Name())
		}
	}
	return r, nil
}

// Freeze makes the files in dir read-only: a round's outputs don't change
// after it ends. (Directories stay writable, so the tree can be removed.)
func Freeze(dir string) {
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			os.Chmod(p, 0o444)
		}
		return nil
	})
}

// Input is one output a round read with input.
type Input struct {
	Key   string // "2:stats.json" (the lane or job as asked, and the name)
	Sum   string // its hash when read
	Round string // the producing round: "<job> <run>"
}

// AddInput records, in the reading round's dir, an output it read.
func AddInput(dir string, in Input) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, inputsFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s\t%s\t%s\n", in.Key, in.Sum, in.Round)
	return err
}

// Inputs are the outputs a round read (the last read of each key wins).
func Inputs(dir string) []Input {
	f, err := os.Open(filepath.Join(dir, inputsFile))
	if err != nil {
		return nil
	}
	defer f.Close()
	byKey := map[string]Input{}
	var order []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.SplitN(sc.Text(), "\t", 3)
		if len(p) != 3 {
			continue
		}
		if _, ok := byKey[p[0]]; !ok {
			order = append(order, p[0])
		}
		byKey[p[0]] = Input{Key: p[0], Sum: p[1], Round: p[2]}
	}
	out := make([]Input, len(order))
	for i, k := range order {
		out[i] = byKey[k]
	}
	return out
}

// Prune removes round output directories under root's .swim/out older than
// keep, except those keepRound says to keep (a lane's latest round). It
// returns what it removed.
func Prune(root string, keep time.Duration, keepRound func(job, run string) bool) []string {
	base := filepath.Join(root, ".swim", "out")
	jobs, _ := os.ReadDir(base)
	var removed []string
	for _, j := range jobs {
		runs, _ := os.ReadDir(filepath.Join(base, j.Name()))
		for _, r := range runs {
			p := filepath.Join(base, j.Name(), r.Name())
			info, err := r.Info()
			if err != nil || time.Since(info.ModTime()) < keep || keepRound(j.Name(), r.Name()) {
				continue
			}
			if os.RemoveAll(p) == nil {
				removed = append(removed, filepath.Join(".swim", "out", j.Name(), r.Name()))
			}
		}
		os.Remove(filepath.Join(base, j.Name())) // only if now empty
	}
	return removed
}

// Stale lists round output directories Prune would remove.
func Stale(root string, keep time.Duration, keepRound func(job, run string) bool) []string {
	base := filepath.Join(root, ".swim", "out")
	jobs, _ := os.ReadDir(base)
	var out []string
	for _, j := range jobs {
		runs, _ := os.ReadDir(filepath.Join(base, j.Name()))
		for _, r := range runs {
			info, err := r.Info()
			if err == nil && time.Since(info.ModTime()) >= keep && !keepRound(j.Name(), r.Name()) {
				out = append(out, filepath.Join(".swim", "out", j.Name(), r.Name()))
			}
		}
	}
	return out
}

// HumanSize is "2.1 KB" style.
func HumanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
