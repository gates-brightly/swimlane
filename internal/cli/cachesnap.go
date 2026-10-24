package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/cache"
	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/step"
)

// swim cache restore TREE / swim cache save TREE, inside a lane: an
// installed tree's snapshot (# Cache: snap:TREE @files), keyed by what it
// was made from. Each is a step in the round's log. A miss is never a
// failure: it passes, and returns 1 so the script installs instead.

// snapsPath is where a round's restores collect for its chain entry.
func snapsPath(logPath string) string { return logPath + ".snaps" }

// cacheSnap is swim cache restore|save TREE [--with S]...
func cacheSnap(root string, cfg *config.Config, sub string, args []string) error {
	var with []string
	var rest []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--with" && i+1 < len(args):
			with = append(with, args[i+1])
			i++
		case strings.HasPrefix(args[i], "--with="):
			with = append(with, strings.TrimPrefix(args[i], "--with="))
		default:
			rest = append(rest, args[i])
		}
	}
	if len(rest) != 1 {
		return usagef("usage: swim cache %s TREE [--with STRING]...   (inside a lane: # Cache: snap:TREE @files)", sub)
	}
	n, err := strconv.Atoi(os.Getenv("SWIM_LANE"))
	if err != nil || n <= 0 {
		return fmt.Errorf("swim cache %s runs inside a lane (its round sets SWIM_LANE)", sub)
	}
	tree := filepath.ToSlash(filepath.Clean(rest[0]))
	info, err := lane.ReadScript(root, n)
	if err != nil {
		return err
	}
	var decl *cache.Decl
	for i := range info.Cache {
		if d := info.Cache[i]; d.Preset == cache.SnapPreset && d.Tree == tree {
			decl = &info.Cache[i]
		}
	}
	if decl == nil {
		return fmt.Errorf("swim %d declares no snapshot of %s: add # Cache: snap:%s @<the files it's installed from>", n, tree, tree)
	}
	r, err := cfg.OpenCache()
	if err != nil {
		return err
	}
	logPath := os.Getenv("STEP_LOG")
	if logPath == "" {
		logPath = lane.Log(root, n)
	}
	// The tree and its key files are the lane's: in its staged copy (which
	// holds the lockfiles) unless it runs live.
	ws := or(os.Getenv("SWIM_WORKSPACE"), root)
	key := cache.SnapKey(ws, tree, decl.Lockfiles, with)
	short := key[:12]
	label := "cache " + sub + " " + tree
	start := time.Now()
	var out []string
	record := func(kind, detail string) {
		cmdline := "swim cache " + sub + " " + tree
		for _, w := range with {
			cmdline += " --with " + shellQuote(w)
		}
		step := strings.Join(out, "\n")
		if step != "" {
			step += "\n"
		}
		lineOut, _ := recordCacheStep(logPath, kind, label, detail, cmdline, start, step)
		fmt.Fprintln(os.Stderr, lineOut)
		status.Update(root, n, cfg.Lanes, func(l *status.Lane) {
			switch kind {
			case logparse.Pass:
				l.Pass++
			case logparse.Skip:
				l.Skip++
			default:
				l.Fail++
				l.FailedSteps = append(l.FailedSteps, lineOut)
			}
		})
	}
	name := cache.SnapName(tree)
	r.Touch(name, n)

	if sub == "restore" {
		lock, err := r.Use(name)
		if err == nil {
			defer lock.Close()
		}
		s := r.FindSnap(tree, key)
		if s == nil {
			out = append(out, "no snapshot of "+tree+" for key "+short+" (cache save "+tree+" makes one)")
			record(logparse.Pass, "miss")
			return exitError{1}
		}
		switch err := r.RestoreSnap(ws, s); {
		case errors.Is(err, cache.ErrTreeExists):
			out = append(out, tree+" is there already: left as it is (restore never deletes)")
			record(logparse.Pass, "kept: "+tree+" exists")
			return exitError{1}
		case err != nil:
			out = append(out, err.Error())
			record(logparse.Fail, "restore failed")
			return exitError{2}
		}
		out = append(out, fmt.Sprintf("restored %s (%s) from snapshot %s", plural(s.Files, "file"), humanBytes(s.Bytes), short),
			fmt.Sprintf("saved by %s (job %s, run %s) at %s; tree sha256 %s", s.SavedBy, or(s.SavedByJob, "-"), or(s.SavedRun, "-"), s.SavedAt, s.TreeSHA256[:12]))
		if s.Failed {
			out = append(out, "-- saved by a round that had failed steps: check what it holds")
		}
		ref := chain.SnapRef{Key: key, Tree: tree, TreeSHA256: s.TreeSHA256, SavedByJob: s.SavedByJob}
		if data, err := yaml.Marshal([]chain.SnapRef{ref}); err == nil {
			appendLog(snapsPath(logPath), string(data))
		}
		detail := "hit " + short
		if s.Failed {
			detail += ", saved by a failed round"
		}
		record(logparse.Pass, detail)
		return nil
	}

	// save
	if s := r.FindSnap(tree, key); s != nil {
		out = append(out, fmt.Sprintf("snapshot %s is saved already (by job %s at %s); keys are written once", short, or(s.SavedByJob, "-"), s.SavedAt))
		record(logparse.Pass, "already saved "+short)
		return nil
	}
	unlock, err := r.Write(name, nil)
	if err == nil {
		defer unlock()
	}
	failed := false
	if st, _ := status.Load(root); st != nil {
		if l := st.Get(n); l != nil {
			failed = l.Fail > 0
		}
	}
	s, err := r.SaveSnap(ws, cache.Snapshot{Key: key, Tree: tree, With: with, SavedBy: operator(),
		SavedByJob: os.Getenv("SWIM_JOB"), SavedRun: os.Getenv("SWIM_RUN"), Failed: failed})
	switch {
	case errors.Is(err, cache.ErrSnapExists):
		out = append(out, "another round saved snapshot "+short+" first; keys are written once")
		record(logparse.Pass, "already saved "+short)
		return nil
	case err != nil:
		out = append(out, err.Error())
		record(logparse.Fail, "save failed")
		return exitError{2}
	}
	out = append(out, fmt.Sprintf("saved %s (%s) as snapshot %s; tree sha256 %s", plural(s.Files, "file"), humanBytes(s.Bytes), short, s.TreeSHA256[:12]))
	detail := "saved " + short
	if failed {
		out = append(out, "-- this round has failed steps: a restore of this snapshot will say so")
		detail += ", from a failed round"
	}
	record(logparse.Pass, detail)
	go r.Evict(cfg.CacheMaxBytes())
	return nil
}

// recordCacheStep writes a snapshot step's block and returns its result line.
func recordCacheStep(logPath, kind, label, detail, cmdline string, start time.Time, out string) (string, error) {
	return step.RecordStep(logPath, kind, label, detail, cmdline, start, time.Since(start), strings.NewReader(out))
}

// roundSnaps are the snapshots a round restored (its log's .snaps), read
// once for its chain entry: the file is spent.
func roundSnaps(logPath string) []chain.SnapRef {
	p := snapsPath(logPath)
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	os.Remove(p)
	var out []chain.SnapRef // each restore appended a one-item list: together, one list
	yaml.Unmarshal(data, &out)
	return out
}

// plural is "1 file", "3 files".
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
