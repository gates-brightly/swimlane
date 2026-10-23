package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/fsutil"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/launcher"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/queue"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// swim clean is everyday and recoverable (features/clean.md shipped): it
// archives every lane's finished rounds under one batch name, then asks,
// each defaulting to no, whether to remove the waiting queue batches and
// whether to move the lane scripts aside (.swim/cleaned/<stamp>/, restored
// by swim clean --restore). It never deletes anything; swim prune does.

type cleanOpts struct {
	queue, noQueue, scripts, noScripts, all, includeAlways, noInput, dryRun, asYAML bool
	restore                                                                         string
}

// cleanedDir holds the scripts swim clean moved aside.
func cleanedDir(root string) string { return filepath.Join(root, ".swim", "cleaned") }

// cleanedManifest lists one clean's moved scripts.
type cleanedManifest struct {
	Schema  string        `yaml:"schema"`
	Stamp   string        `yaml:"stamp"`
	By      string        `yaml:"by"`
	At      string        `yaml:"at"`
	Scripts []cleanedFile `yaml:"scripts"`
	// One lane trashed (swim trash), not a clean: which, its job and
	// round, and where it was trashed from (cli, watch, hub).
	Lane  int    `yaml:"lane,omitempty"`
	Job   string `yaml:"job,omitempty"`
	Round string `yaml:"round,omitempty"`
	From  string `yaml:"from,omitempty"`
}

type cleanedFile struct {
	Path   string `yaml:"path"` // repo-relative, as it was
	SHA256 string `yaml:"sha256"`
}

type cleanLane struct {
	N     int    `yaml:"lane"`
	State string `yaml:"state"`
	Job   string `yaml:"job,omitempty"`
	To    string `yaml:"to,omitempty"`   // the archived log
	Skip  string `yaml:"skip,omitempty"` // why it's left alone
}

func cmdClean(args []string) error {
	var o cleanOpts
	var out string
	for i, a := range args {
		if a == "-n" {
			args[i] = "--dry-run"
		}
	}
	rest, err := flags{bools: map[string]*bool{"queue": &o.queue, "no-queue": &o.noQueue, "scripts": &o.scripts, "no-scripts": &o.noScripts,
		"all": &o.all, "include-always": &o.includeAlways, "no-input": &o.noInput, "dry-run": &o.dryRun, "yaml": &o.asYAML},
		strs: map[string]*string{"restore": &o.restore, "o": &out}}.parse(args)
	if err != nil {
		return err
	}
	if out == "yaml" {
		o.asYAML = true
	} else if out != "" {
		return usagef("-o takes yaml")
	}
	if len(rest) > 0 {
		return usagef("usage: swim clean [--queue|--no-queue] [--scripts|--no-scripts] [--all] [--include-always] [--no-input] [-n] [-o yaml] | --restore STAMP")
	}
	if o.all {
		o.queue, o.scripts = true, true
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	if o.restore != "" {
		return cleanRestore(root, cfg, o.restore)
	}
	if why := launcher.Busy(root, cfg); why != "" && !o.dryRun {
		return fmt.Errorf("swim clean: %s; clean when it's done", why)
	}
	if !o.dryRun {
		// A dry run changes nothing under .swim/, status.yml included.
		if err := refreshStatus(root, cfg); err != nil {
			return err
		}
	}
	interactive := !o.noInput && !o.asYAML && !o.dryRun && ui.IsTTY(os.Stdin) && ui.IsTTY(os.Stdout)
	in := bufio.NewReader(os.Stdin)
	ask := func(q string, def bool) bool {
		if !interactive {
			return def
		}
		hint := "[y/N]"
		if def {
			hint = "[Y/n]"
		}
		fmt.Printf("%s %s ", q, hint)
		line, _ := in.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return true
		case "n", "no":
			return false
		}
		return def
	}
	now := time.Now()
	stamp := now.Format("20060102-1504")

	// What to archive: every lane with a finished round and a log.
	lanes, name := cleanPlan(root, cfg, o, stamp)
	var toArchive []cleanLane
	for _, l := range lanes {
		if l.Skip == "" {
			toArchive = append(toArchive, l)
		}
	}
	entries, _ := queue.List(root, false)
	var waiting, theirs, orphans []*queue.Entry
	for _, e := range entries {
		switch {
		case e.State == queue.Orphaned:
			orphans = append(orphans, e)
		case e.State != queue.Queued:
		case e.QueueSig != "" && e.By != operator():
			theirs = append(theirs, e)
		default:
			waiting = append(waiting, e)
		}
	}
	scripts := cleanScripts(root, o)

	if !o.asYAML {
		fmt.Printf("swim clean · nothing is deleted · %d lanes with finished rounds, %d queued batches, %d lane scripts\n\n",
			len(toArchive), len(waiting)+len(theirs), len(scripts))
		for _, l := range lanes {
			if l.Skip != "" {
				fmt.Printf("skip     swim %-2d %s\n", l.N, l.Skip)
				continue
			}
			fmt.Printf("archive  swim %-2d %-8s %-18s -> %s\n", l.N, l.State, lane.ShortJob(l.Job), l.To)
		}
		for _, e := range orphans {
			fmt.Printf("orphan   #%d %s: its swimmer is gone; swim queue requeue %d or swim queue rm %d\n", e.Seq, e.ID, e.Seq, e.Seq)
		}
		fmt.Println()
	}
	res := struct {
		Schema   string      `yaml:"schema"`
		Name     string      `yaml:"name"`
		DryRun   bool        `yaml:"dry_run"`
		Lanes    []cleanLane `yaml:"lanes"`
		Queue    []string    `yaml:"queue_removed"`
		Scripts  []string    `yaml:"scripts_moved"`
		Restore  string      `yaml:"restore,omitempty"`
		Orphaned []string    `yaml:"orphaned,omitempty"`
	}{Schema: "swim.clean/v1", Name: name, DryRun: o.dryRun, Lanes: lanes, Queue: []string{}, Scripts: []string{}}
	for _, e := range orphans {
		res.Orphaned = append(res.Orphaned, e.ID)
	}

	// 1. Archive (asked, default yes).
	archived := 0
	if len(toArchive) > 0 && ask(fmt.Sprintf("archive %d lanes' logs?", len(toArchive)), true) && !o.dryRun {
		for _, l := range toArchive {
			if _, err := archiveLane(root, cfg, l.N, name); err != nil {
				return err
			}
			archived++
		}
		if !o.asYAML {
			fmt.Printf("archived %d lanes as %s\n", archived, name)
		}
	}

	// 2. The queue (asked, default no).
	var removed []string
	if len(waiting)+len(theirs) > 0 && !o.noQueue {
		var seqs []string
		for _, e := range waiting {
			seqs = append(seqs, fmt.Sprintf("#%d", e.Seq))
		}
		take := o.queue
		if !take && len(waiting) > 0 {
			take = ask(fmt.Sprintf("\nremove %d waiting queue batches (%s)?", len(waiting), strings.Join(seqs, " ")), false)
		}
		if take {
			for _, e := range waiting {
				if !o.dryRun {
					if err := queue.Remove(e); err != nil {
						return err
					}
					history.Log(root, history.Entry{Event: history.QueueRm, Detail: fmt.Sprintf("#%d %s by clean %s", e.Seq, e.ID, name)})
				}
				removed = append(removed, e.ID)
			}
			// Someone else's signed batch: their queued, signed work.
			for _, e := range theirs {
				if !interactive || !ask(fmt.Sprintf("remove #%d (%s), queued and signed by %s?", e.Seq, e.ID, e.By), false) {
					if !o.asYAML {
						fmt.Printf("kept #%d: queued and signed by %s (swim queue rm %d)\n", e.Seq, e.By, e.Seq)
					}
					continue
				}
				if !o.dryRun {
					if err := queue.Remove(e); err != nil {
						return err
					}
					history.Log(root, history.Entry{Event: history.QueueRm, Detail: fmt.Sprintf("#%d %s by clean %s", e.Seq, e.ID, name)})
				}
				removed = append(removed, e.ID)
			}
		} else if !o.asYAML {
			fmt.Println("kept the queue")
		}
	}
	res.Queue = append(res.Queue, removed...)

	// 3. The scripts (asked, default no): moved aside, restorable.
	var moved []cleanedFile
	if len(scripts) > 0 && !o.noScripts {
		var names []string
		for _, s := range scripts {
			names = append(names, s)
		}
		shown := names
		if len(shown) > 4 {
			shown = append(append([]string(nil), shown[:3]...), "...")
		}
		take := o.scripts || ask(fmt.Sprintf("\nmove %d lane scripts aside (%s)?\n  they go to .swim/cleaned/%s/ and can be restored", len(scripts), strings.Join(shown, " "), stamp), false)
		if take {
			if !o.dryRun {
				if moved, err = moveScripts(root, stamp, scripts, nil); err != nil {
					return err
				}
			} else {
				for _, s := range scripts {
					moved = append(moved, cleanedFile{Path: s})
				}
			}
			if !o.asYAML {
				fmt.Printf("moved %d scripts to .swim/cleaned/%s/ (swim clean --restore %s puts them back)\n", len(moved), stamp, stamp)
			}
			res.Restore = stamp
		} else if !o.asYAML {
			fmt.Println("kept the lane scripts")
		}
	}
	for _, m := range moved {
		res.Scripts = append(res.Scripts, m.Path)
	}

	if !o.dryRun && (archived > 0 || len(removed) > 0 || len(moved) > 0) {
		detail := fmt.Sprintf("%s: %d archived, %d queued removed, %d scripts moved", name, archived, len(removed), len(moved))
		history.Log(root, history.Entry{Event: history.Clean, Detail: detail})
		sums := map[string]string{}
		for _, m := range moved {
			sums[m.Path] = m.SHA256
		}
		var ns []string
		for _, l := range toArchive {
			ns = append(ns, fmt.Sprint(l.N))
		}
		record(root, cfg, chain.Entry{Type: "clean", Action: detail + "  (lanes " + strings.Join(ns, ",") + ")", Outputs: sums, Removed: removed})
	}
	if o.asYAML {
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		if err := enc.Encode(res); err != nil {
			return err
		}
		return enc.Close()
	}
	if o.dryRun {
		fmt.Println("dry run: nothing changed")
	}
	return nil
}

// cleanPlan is what swim clean does with each lane in use, and the batch
// name: clean-<stamp>, made unique (-2, -3, ...) if an archive has it.
func cleanPlan(root string, cfg *config.Config, o cleanOpts, stamp string) ([]cleanLane, string) {
	st, _ := status.Load(root)
	var out []cleanLane
	for _, n := range lane.Associated(root) {
		info, _ := lane.ReadScript(root, n)
		var l *status.Lane
		if st != nil {
			l = st.Get(n)
		}
		cl := cleanLane{N: n}
		if l != nil {
			cl.State, cl.Job = l.State, l.Job
		}
		switch {
		case !hasLog(root, n):
			continue // nothing to archive (a pending script is the scripts question's)
		case cl.State == status.Running || cl.State == status.Waiting || cl.State == status.Locked || cl.State == status.Queued:
			if pid, ok := lane.Running(root, n); ok {
				cl.Skip = fmt.Sprintf("running (pid %d)", pid)
			} else if l != nil && l.Alive() {
				cl.Skip = fmt.Sprintf("%s in a swim run (pid %d)", cl.State, l.PID)
			} else {
				cl.State = "stale"
			}
		case info.Always && !o.includeAlways:
			cl.Skip = "Always lane (standing setup; --include-always to clean it)"
		}
		if cl.State == "" || cl.State == status.Idle {
			cl.State = "finished"
		}
		out = append(out, cl)
	}
	name := "clean-" + stamp
	for k := 2; k < 100; k++ {
		taken := false
		for i := range out {
			if out[i].Skip != "" {
				continue
			}
			to, err := lane.ArchiveCheck(root, out[i].N, name)
			if err != nil {
				if strings.Contains(err.Error(), "exists") {
					taken = true
					break
				}
				out[i].Skip = err.Error()
				continue
			}
			out[i].To = filepath.Base(to)
		}
		if !taken {
			break
		}
		name = fmt.Sprintf("clean-%s-%d", stamp, k)
	}
	return out, name
}

// hasLog reports whether lane n has a current log (its own, or a cell's or
// job's).
func hasLog(root string, n int) bool {
	if st, err := os.Stat(lane.Log(root, n)); err == nil && st.Size() > 0 {
		return true
	}
	files, _ := filepath.Glob(filepath.Join(lane.LogDir(root), fmt.Sprintf("agent%d.*.log", n)))
	for _, f := range files {
		if !strings.Contains(filepath.Base(f), ".prev-") && !strings.HasSuffix(f, ".check.log") {
			return true
		}
	}
	return false
}

// archiveLane archives lane n's logs as name, as swim archive does, and
// resets its status.
func archiveLane(root string, cfg *config.Config, n int, name string) (string, error) {
	dst, err := lane.Archive(root, n, name)
	if err != nil {
		return "", err
	}
	status.Update(root, n, cfg.Lanes, func(l *status.Lane) {
		l.ResetRun()
		l.State, l.Round, l.Job = status.Idle, "", ""
		l.LastArchive = filepath.Base(dst)
	})
	archived, _ := logparse.ParseFile(dst)
	history.Log(root, history.Entry{Event: history.Archive, Lane: n, Job: archived.Job, Detail: filepath.Base(dst)})
	return dst, nil
}

// cleanScripts are the lane scripts the scripts question covers: lane.N.*
// in every runtime, group files and lane.N.yml sidecars, not of a running
// lane, nor (unless --include-always) an Always lane's.
func cleanScripts(root string, o cleanOpts) []string {
	st, _ := status.Load(root)
	var out []string
	for _, n := range lane.Associated(root) {
		if _, ok := lane.Running(root, n); ok {
			continue
		}
		if st != nil {
			if l := st.Get(n); l != nil && l.Alive() && (l.State == status.Running || l.State == status.Waiting) {
				continue
			}
		}
		info, _ := lane.ReadScript(root, n)
		if info.Always && !o.includeAlways {
			continue
		}
		if s := lane.ScriptName(root, n); s != "" {
			if _, err := os.Stat(filepath.Join(root, s)); err == nil {
				out = append(out, s)
			}
		}
		if sc := lane.Sidecar(root, n); sc != "" {
			if _, err := os.Stat(sc); err == nil {
				out = append(out, rel(root, sc))
			}
		}
	}
	sort.Strings(out)
	var uniq []string
	for i, s := range out {
		if i == 0 || s != out[i-1] {
			uniq = append(uniq, s)
		}
	}
	return uniq
}

// moveScripts moves paths (repo-relative) to .swim/cleaned/<stamp>/, the
// same layout, with cleaned.yml listing each and its SHA-256; fill, if
// set, adds to that manifest (swim trash: the lane).
func moveScripts(root, stamp string, paths []string, fill func(*cleanedManifest)) ([]cleanedFile, error) {
	dir := filepath.Join(cleanedDir(root), stamp)
	if _, err := os.Stat(dir); err == nil {
		return nil, fmt.Errorf("%s exists already; wait a minute and clean again", rel(root, dir))
	}
	var moved []cleanedFile
	for _, p := range paths {
		sum, err := chain.HashFile(filepath.Join(root, p))
		if err != nil {
			return moved, err
		}
		dst := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return moved, err
		}
		if err := os.Rename(filepath.Join(root, p), dst); err != nil {
			return moved, err
		}
		moved = append(moved, cleanedFile{Path: p, SHA256: sum})
	}
	m := cleanedManifest{Schema: "swim.cleaned/v1", Stamp: stamp, By: operator(), At: time.Now().UTC().Format(time.RFC3339), Scripts: moved}
	if fill != nil {
		fill(&m)
	}
	data, _ := yaml.Marshal(m)
	return moved, fsutil.WriteAtomic(filepath.Join(dir, "cleaned.yml"), data, 0o644)
}

// cleanRestore puts a clean's moved scripts back, all or none: a path
// reused since is refused.
func cleanRestore(root string, cfg *config.Config, stamp string) error {
	dir := filepath.Join(cleanedDir(root), stamp)
	data, err := os.ReadFile(filepath.Join(dir, "cleaned.yml"))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no clean %s under .swim/cleaned/ (swim prune may have removed it)", stamp)
	}
	if err != nil {
		return err
	}
	var m cleanedManifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return err
	}
	var problems []string
	for _, f := range m.Scripts {
		if _, err := os.Stat(filepath.Join(root, f.Path)); err == nil {
			problems = append(problems, f.Path+" exists again (reused since the clean)")
		}
	}
	if len(problems) > 0 {
		return errors.New("nothing restored:\n  " + strings.Join(problems, "\n  "))
	}
	for _, f := range m.Scripts {
		if err := os.Rename(filepath.Join(dir, f.Path), filepath.Join(root, f.Path)); err != nil {
			return err
		}
	}
	os.RemoveAll(dir)
	refreshStatus(root, cfg)
	if m.Lane > 0 {
		// A trashed lane: recorded as untrash, in the chain too.
		detail := fmt.Sprintf("%s: swim %d back (%s)", stamp, m.Lane, strings.Join(paths(m.Scripts), " "))
		history.Log(root, history.Entry{Event: history.Untrash, Lane: m.Lane, Job: m.Job, Detail: detail})
		record(root, cfg, chain.Entry{Type: "untrash", Lane: m.Lane, Job: m.Job, Action: detail, Outputs: sums(m.Scripts)})
		fmt.Printf("restored swim %d from .swim/cleaned/%s/ (%s); its round is pending again\n", m.Lane, stamp, strings.Join(paths(m.Scripts), " "))
		return nil
	}
	history.Log(root, history.Entry{Event: history.Clean, Detail: fmt.Sprintf("restored %d scripts from clean %s", len(m.Scripts), stamp)})
	fmt.Printf("restored %d scripts from .swim/cleaned/%s/\n", len(m.Scripts), stamp)
	return nil
}

// paths are the moved files' repo-relative paths.
func paths(fs []cleanedFile) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Path)
	}
	return out
}

// sums are the moved files' SHA-256s, for a chain entry.
func sums(fs []cleanedFile) map[string]string {
	out := map[string]string{}
	for _, f := range fs {
		out[f.Path] = f.SHA256
	}
	return out
}
