package cli

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/launcher"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/manifest"
	"github.com/gates-brightly/swimlane/internal/queue"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// swim prune is deliberate and permanent: it deletes leftovers older than
// a cutoff it's given (scripts swim clean moved aside, consumed queue
// entries, old run stages, and with --logs archived logs), records what it
// deleted in the chain, and never deletes the record itself (the chain,
// .swim.log, approvals, kept script copies).

// pruneItem is one thing swim prune would delete.
type pruneItem struct {
	cat   string // cleaned | queue | stages | logs
	path  string // absolute
	bytes int64
}

var pruneCats = []struct{ key, what, note string }{
	{"cleaned", "moved-aside scripts", ".swim/cleaned/<stamp>/ (swim clean)"},
	{"queue", "consumed queue entries", ".swim/queue/done/"},
	{"stages", "run stages", ".swim/runs/<run>/ (staged copies; run records and logs kept)"},
	{"logs", "archived logs", "agentN.prev-*.log (with --logs)"},
}

func cmdPrune(args []string) error {
	var older, stamp string
	var logs, confirm, dryRun bool
	for i, a := range args {
		if a == "-n" {
			args[i] = "--dry-run"
		}
	}
	rest, err := flags{bools: map[string]*bool{"logs": &logs, "confirm": &confirm, "dry-run": &dryRun},
		strs: map[string]*string{"older": &older, "stamp": &stamp}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 0 || older != "" && stamp != "" {
		return usagef("usage: swim prune --older D [--logs] [--confirm] [-n] | --stamp STAMP")
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	all := pruneCandidates(root, cfg, 0, "", true)
	if older == "" && stamp == "" {
		// Bare: what each category holds; never "delete everything".
		fmt.Println("swim prune · deletes nothing without a cutoff: --older D (30d, 12h) or --stamp STAMP")
		for _, c := range pruneCats {
			n, b := countBytes(all, c.key)
			fmt.Printf("  %-22s %5d  %8s  %s\n", c.what, n, humanBytes(b), c.note)
		}
		fmt.Println("  kept always: the chain, .swim.log, approvals/, lookup.yml, kept script copies (.swim/scripts/)")
		return nil
	}
	var cutoff time.Duration
	if older != "" {
		if cutoff, err = lookup.ParseLifetime(older); err != nil || cutoff <= 0 {
			return usagef("--older %q: a duration like 30d or 12h", older)
		}
	}
	if why := launcher.Busy(root, cfg); why != "" && !dryRun {
		return fmt.Errorf("swim prune: %s; prune when it's done", why)
	}
	items := pruneCandidates(root, cfg, cutoff, stamp, logs)
	if stamp != "" && len(items) == 0 {
		return fmt.Errorf("no clean %s under .swim/cleaned/", stamp)
	}
	fmt.Println("swim prune · permanent: deleted files can't be restored")
	fmt.Println()
	var total int64
	for _, c := range pruneCats {
		n, b := countBytes(items, c.key)
		total += b
		switch {
		case n > 0:
			fmt.Printf("delete  %-22s %5d  %8s\n", c.what, n, humanBytes(b))
		case c.key == "logs" && !logs:
			fmt.Printf("keep    %-22s  (pass --logs to include them)\n", c.what)
		}
	}
	if len(items) == 0 {
		fmt.Println("nothing to prune past the cutoff")
		return nil
	}
	if dryRun {
		fmt.Println("\ndry run: nothing deleted")
		return nil
	}
	if !confirm {
		if !ui.IsTTY(os.Stdin) || !ui.IsTTY(os.Stdout) {
			return fmt.Errorf("swim prune deletes for good: pass --confirm to run it without a terminal")
		}
		fmt.Printf("\ndelete these %d items (%s)? type \"prune\" to confirm: ", len(items), humanBytes(total))
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if strings.TrimSpace(line) != "prune" {
			fmt.Println("nothing deleted")
			return nil
		}
	}
	name := "prune-" + time.Now().Format("20060102-1504")
	// Recorded before anything goes: each deleted archived log's rounds, so
	// swim verify reports them as pruned, not missing.
	var pruned []string
	sums := map[string]string{}
	for _, it := range items {
		if it.cat != "logs" {
			continue
		}
		sum, _ := chain.HashFile(it.path)
		sums[rel(root, it.path)] = sum
		rounds, _ := splitRounds(it.path)
		for _, r := range rounds {
			pruned = append(pruned, chain.Hash([]byte(roundSection(r))))
		}
	}
	detail := fmt.Sprintf("%s: %d items, %s", name, len(items), humanBytes(total))
	if err := record(root, cfg, chain.Entry{Type: "prune", Action: detail, Outputs: sums, Pruned: pruned}); err != nil {
		return err
	}
	deleted := 0
	for _, it := range items {
		var err error
		switch it.cat {
		case "stages":
			err = manifest.Remove(it.path)
		default:
			err = os.RemoveAll(it.path)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "swim prune: %s: %v\n", rel(root, it.path), err)
			continue
		}
		deleted++
	}
	history.Log(root, history.Entry{Event: history.Prune, Detail: fmt.Sprintf("%s: deleted %d of %d items, %s", name, deleted, len(items), humanBytes(total))})
	fmt.Printf("pruned %d items; recorded in the chain as %s\n", deleted, name)
	return nil
}

// pruneCandidates are what swim prune would delete: everything older than
// cutoff (0: everything, for the bare summary), or one clean's moved
// scripts (stamp). A run's stage stays while its round is a lane's
// latest (swim verify checks it), as swim doctor --fix keeps it.
func pruneCandidates(root string, cfg *config.Config, cutoff time.Duration, stamp string, logs bool) []pruneItem {
	var out []pruneItem
	old := func(path string) bool {
		st, err := os.Stat(path)
		return err == nil && time.Since(st.ModTime()) >= cutoff
	}
	add := func(cat, path string) {
		out = append(out, pruneItem{cat: cat, path: path, bytes: dirBytes(path)})
	}
	if stamp != "" {
		if dir := filepath.Join(cleanedDir(root), stamp); isDir(dir) {
			add("cleaned", dir)
		}
		return out
	}
	dirs, _ := os.ReadDir(cleanedDir(root))
	for _, d := range dirs {
		if p := filepath.Join(cleanedDir(root), d.Name()); d.IsDir() && old(p) {
			add("cleaned", p)
		}
	}
	done, _ := filepath.Glob(filepath.Join(queue.DoneDir(root), "*.yml"))
	for _, p := range done {
		if old(p) {
			add("queue", p)
		}
	}
	st, _ := status.Load(root)
	keep := func(run string) bool {
		if st == nil {
			return false
		}
		for _, l := range st.Lanes {
			if l.Run == run {
				return true
			}
			for _, c := range l.Cells {
				if c.Run == run {
					return true
				}
			}
		}
		return false
	}
	for _, run := range manifest.OldStages(root, cutoff, keep) {
		add("stages", filepath.Join(manifest.RunsDir(root), run))
	}
	if logs {
		files, _ := filepath.Glob(filepath.Join(lane.LogDir(root), "agent*.prev-*.log"))
		for _, p := range files {
			if old(p) {
				add("logs", p)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// dirBytes is a file's size, or a directory's in all.
func dirBytes(path string) int64 {
	var n int64
	filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

func countBytes(items []pruneItem, cat string) (n int, b int64) {
	for _, it := range items {
		if it.cat == cat {
			n++
			b += it.bytes
		}
	}
	return n, b
}

// humanBytes is "12 KB", "3.1 MB".
func humanBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%d KB", b>>10)
	}
	return fmt.Sprintf("%d B", b)
}

// pruneReclaim is what swim prune --older 30d would free, for swim doctor.
func pruneReclaim(root string, cfg *config.Config) (items int, bytes int64) {
	for _, it := range pruneCandidates(root, cfg, 30*24*time.Hour, "", false) {
		items++
		bytes += it.bytes
	}
	return items, bytes
}
