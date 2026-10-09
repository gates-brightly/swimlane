// Package migrate brings a repo's lane scripts and lane logs up to the
// current syntax (see internal/syntax). It runs whenever swim loads a repo:
// older files are rewritten in place, with the originals copied to
// .swim/migrations/<time>/ first; files newer than this swim reads stop the
// command with an update prompt. A running lane's files are never touched.
package migrate

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/syntax"
)

// Result lists what a migration did.
type Result struct {
	Files   []string // repo-relative paths rewritten
	Backup  string   // repo-relative backup directory ("" if nothing changed)
	Skipped []string // files left alone because their lane is running
}

var (
	scriptRE = regexp.MustCompile(`^lane\.(\d+)\.sh$`)
	logRE    = regexp.MustCompile(`^agent(\d+)(\.prev-.+)?\.log$`)
)

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
			if m := scriptRE.FindStringSubmatch(e.Name()); m != nil && !e.IsDir() {
				n, _ := strconv.Atoi(m[1])
				out = append(out, file{rel: e.Name(), lane: n})
			}
		}
	}
	logDir := lane.LogDir(root)
	if es, err := os.ReadDir(logDir); err == nil {
		for _, e := range es {
			if m := logRE.FindStringSubmatch(e.Name()); m != nil && !e.IsDir() {
				n, _ := strconv.Atoi(m[1])
				rel, _ := filepath.Rel(root, filepath.Join(logDir, e.Name()))
				out = append(out, file{rel: rel, lane: n, log: true})
			}
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].rel < out[b].rel })
	return out
}

// Check returns syntax.ErrTooNew for the first lane script or log written
// in a newer syntax than this swim reads.
func Check(root string) error {
	for _, f := range files(root) {
		if err := syntax.Check(f.rel, versionOf(root, f)); err != nil {
			return err
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
	return syntax.OfScript(string(data))
}

// Run migrates every lane script and log below the current syntax. It
// refuses (before changing anything) if any file is newer than this swim.
func Run(root string) (Result, error) {
	var res Result
	if err := Check(root); err != nil {
		return res, err
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	backupDir := filepath.Join(root, ".swim", "migrations", stamp)
	for _, f := range files(root) {
		v := versionOf(root, f)
		if v == 0 || v >= syntax.Current {
			continue
		}
		if _, running := lane.Running(root, f.lane); running && (!f.log || !strings.Contains(f.rel, ".prev-")) {
			// Never rewrite a script bash is reading, or the log it's writing.
			res.Skipped = append(res.Skipped, f.rel)
			continue
		}
		path := filepath.Join(root, f.rel)
		src, err := os.ReadFile(path)
		if err != nil {
			return res, err
		}
		bak := filepath.Join(backupDir, f.rel)
		if err := os.MkdirAll(filepath.Dir(bak), 0o755); err != nil {
			return res, err
		}
		if err := os.WriteFile(bak, src, 0o644); err != nil {
			return res, err
		}
		if f.log {
			if err := logparse.ConvertFile(path, f.lane); err != nil {
				return res, fmt.Errorf("migrate %s: %w", f.rel, err)
			}
		} else {
			out, _ := lane.MigrateScript(string(src))
			st, _ := os.Stat(path)
			mode := os.FileMode(0o755)
			if st != nil {
				mode = st.Mode().Perm()
			}
			tmp := path + ".migrating"
			if err := os.WriteFile(tmp, []byte(out), mode); err != nil {
				return res, err
			}
			if err := os.Rename(tmp, path); err != nil {
				return res, err
			}
		}
		res.Files = append(res.Files, f.rel)
	}
	if len(res.Files) > 0 {
		res.Backup, _ = filepath.Rel(root, backupDir)
		history.Log(root, history.Entry{Event: history.Migrate,
			Detail: fmt.Sprintf("%d file(s) to syntax %d (originals in %s): %s", len(res.Files), syntax.Current, res.Backup, strings.Join(res.Files, " "))})
	}
	return res, nil
}
