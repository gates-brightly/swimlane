// Package migrate checks a repo's lane scripts and lane logs against the
// syntaxes this swim reads (see internal/syntax), whenever swim loads a
// repo: a file newer than it reads stops the command with an update
// prompt, and a syntax 1 file (from before swim versioned its files) with
// how to replace it. Nothing is rewritten.
package migrate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/gates-brightly/swimlane/internal/chars"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/syntax"
)

// Result lists what a migration did.
type Result struct {
	Files   []string // repo-relative paths rewritten
	Backup  string   // repo-relative backup directory ("" if nothing changed)
	Skipped []string // files left alone because their lane is running
}

type file struct {
	rel  string // relative to root
	lane int
	log  bool
}

// files lists the repo's lane scripts and lane logs (current and archived).
func files(root string) []file {
	var out []file
	if es, err := os.ReadDir(root); err == nil {
		for _, e := range es {
			if ln, ok := chars.Between(e.Name(), "lane.", ".sh"); ok && chars.Digits(ln) && !e.IsDir() {
				n, _ := strconv.Atoi(ln)
				out = append(out, file{rel: e.Name(), lane: n})
			}
		}
	}
	logDir := lane.LogDir(root)
	if es, err := os.ReadDir(logDir); err == nil {
		for _, e := range es {
			if ln := logLane(e.Name()); ln != "" && !e.IsDir() {
				n, _ := strconv.Atoi(ln)
				rel, _ := filepath.Rel(root, filepath.Join(logDir, e.Name()))
				out = append(out, file{rel: rel, lane: n, log: true})
			}
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].rel < out[b].rel })
	return out
}

// Check returns syntax.ErrTooNew for the first lane script or log written
// in a newer syntax than this swim reads, and an error for one in syntax 1.
func Check(root string) error {
	for _, f := range files(root) {
		check := syntax.CheckScript
		if f.log {
			check = syntax.Check
		}
		v := versionOf(root, f)
		if err := check(f.rel, v); err != nil {
			return err
		}
		if v == 1 {
			fix := "write it again from `swim new N`"
			if f.log {
				fix = "archive it out of the way (swim archive " + strconv.Itoa(f.lane) + ") or delete it"
			}
			return fmt.Errorf("%s is swim syntax 1, from before swim versioned its files, which this swim doesn't read: %s", f.rel, fix)
		}
	}
	return nil
}

func versionOf(root string, f file) int {
	path := filepath.Join(root, f.rel)
	if f.log {
		return syntax.OfLogFile(path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	src := string(data)
	if !lane.UsesLib(src) && !strings.Contains(src, lane.StubMarker) {
		// A plain program (it runs inside swim's wrapper): swim's syntax
		// versions are the bash library's, so there is nothing to migrate.
		return syntax.Current
	}
	return syntax.OfScript(src)
}

// agentN.prev-<name>.log ("" if it isn't one).
func logLane(name string) string {
	body, ok := chars.Between(name, "agent", ".log")
	if !ok {
		return ""
	}
	n, prev, archived := strings.Cut(body, ".")
	if !chars.Digits(n) || archived && (!strings.HasPrefix(prev, "prev-") || len(prev) == len("prev-")) {
		return ""
	}
	return n
}
