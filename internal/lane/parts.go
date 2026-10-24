package lane

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/gates-brightly/swimlane/internal/cache"
)

// ScriptBytes is what a lane's approvals, queue pins and chain entries
// hash: its script, followed by every file ScriptParts adds.
func ScriptBytes(root string, n int) ([]byte, error) {
	path, files, err := ScriptParts(root, n)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil || IsBinary(data) {
		return data, err
	}
	return JoinScript(data, files, func(f string) ([]byte, error) { return os.ReadFile(filepath.Join(root, f)) }), nil
}

// ScriptParts are the files ScriptBytes hashes: the script's path and,
// sorted, the repo-relative files it adds: # Cache: lockfiles and
// # Includes: matches. A compiled lane adds none.
func ScriptParts(root string, n int) (string, []string, error) {
	path := Script(root, n)
	data, err := os.ReadFile(path)
	if err != nil || IsBinary(data) {
		return path, nil, err
	}
	info := ParseScript(string(data))
	// # Cache: lockfiles: what the tool checks the cache against, so the
	// approval covers what it may take from the cache.
	files := cache.Files(info.Cache)
	if sc := Sidecar(root, n); fileExists(sc) {
		var side Info
		if applySidecar(sc, &side) == nil {
			files = append(files, cache.Files(side.Cache)...)
		}
	}
	// # Includes: (globs): local modules and other files that are part of
	// the round.
	for _, g := range append(info.Includes, sideIncludes(root, n)...) {
		matches, _ := filepath.Glob(filepath.Join(root, g))
		for _, m := range matches {
			if st, err := os.Stat(m); err == nil && !st.IsDir() {
				if rel, err := filepath.Rel(root, m); err == nil {
					files = append(files, filepath.ToSlash(rel))
				}
			}
		}
	}
	sort.Strings(files)
	return path, dedupe(files), nil
}

// JoinScript is ScriptBytes from the script's data and its files, read
// with read (from the repo, or from a run's staged copy).
func JoinScript(data []byte, files []string, read func(string) ([]byte, error)) []byte {
	var b bytes.Buffer
	b.Write(data)
	for _, f := range files {
		fd, err := read(f)
		if err != nil {
			fd = []byte("(missing)\n")
		}
		fmt.Fprintf(&b, "\n# swim: included file %s\n", f)
		b.Write(fd)
	}
	return b.Bytes()
}

func sideIncludes(root string, n int) []string {
	sc := Sidecar(root, n)
	if !fileExists(sc) {
		return nil
	}
	var side Info
	if applySidecar(sc, &side) != nil {
		return nil
	}
	return side.Includes
}

func dedupe(s []string) []string {
	var out []string
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
