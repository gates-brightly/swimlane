package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/launcher"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/queue"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// swim clean idles every lane whose round is finished: it archives its
// logs under one batch name and stubs its script, as swim archive and swim
// stub do, then asks (default no) whether to remove the waiting queue
// batches. A script edited since its round ran holds new work: its logs
// are archived, and the script is kept.

type cleanOpts struct {
	queue, noQueue, noInput, dryRun, asYAML bool
}

type cleanLane struct {
	N     int    `yaml:"lane"`
	State string `yaml:"state"`
	Job   string `yaml:"job,omitempty"`
	To    string `yaml:"to,omitempty"`   // the archived log
	Stub  bool   `yaml:"stub"`           // its script is stubbed too
	Kept  string `yaml:"kept,omitempty"` // why its script is kept
	Skip  string `yaml:"skip,omitempty"` // why it's left alone
}

func cmdClean(args []string) error {
	var o cleanOpts
	var all bool
	var out string
	for i, a := range args {
		if a == "-n" {
			args[i] = "--dry-run"
		}
	}
	rest, err := flags{bools: map[string]*bool{"queue": &o.queue, "no-queue": &o.noQueue, "all": &all,
		"no-input": &o.noInput, "dry-run": &o.dryRun, "yaml": &o.asYAML},
		strs: map[string]*string{"o": &out}}.parse(args)
	if err != nil {
		return err
	}
	if out == "yaml" {
		o.asYAML = true
	} else if out != "" {
		return usagef("-o takes yaml")
	}
	if len(rest) > 0 {
		return usagef("usage: swim clean [--queue|--no-queue] [--all] [--no-input] [-n] [-o yaml]")
	}
	if all {
		o.queue = true
	}
	root, cfg, err := repo()
	if err != nil {
		return err
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
	stamp := time.Now().Format("20060102-1504")

	// What to idle: every lane with a finished round and a log.
	lanes, name := cleanPlan(root, cfg, stamp)
	var toClean []cleanLane
	for _, l := range lanes {
		if l.Skip == "" {
			toClean = append(toClean, l)
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

	if !o.asYAML {
		fmt.Printf("swim clean · %d lanes with finished rounds, %d queued batches\n\n", len(toClean), len(waiting)+len(theirs))
		for _, l := range lanes {
			switch {
			case l.Skip != "":
				fmt.Printf("skip     swim %-2d %s\n", l.N, l.Skip)
			case l.Stub:
				fmt.Printf("idle     swim %-2d %-8s %-18s -> %s, stub\n", l.N, l.State, lane.ShortJob(l.Job), l.To)
			default:
				fmt.Printf("archive  swim %-2d %-8s %-18s -> %s (script kept: %s)\n", l.N, l.State, lane.ShortJob(l.Job), l.To, l.Kept)
			}
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
		Orphaned []string    `yaml:"orphaned,omitempty"`
	}{Schema: "swim.clean/v1", Name: name, DryRun: o.dryRun, Lanes: lanes, Queue: []string{}}
	for _, e := range orphans {
		res.Orphaned = append(res.Orphaned, e.ID)
	}

	// 1. Archive and stub (asked, default yes).
	archived, stubbed := 0, 0
	sums := map[string]string{}
	if len(toClean) > 0 && ask(fmt.Sprintf("archive %d lanes' logs and stub their scripts?", len(toClean)), true) && !o.dryRun {
		for _, l := range toClean {
			if _, err := archiveLane(root, cfg, l.N, name); err != nil {
				return err
			}
			archived++
			if !l.Stub {
				continue
			}
			if data, err := lane.ScriptBytes(root, l.N); err == nil {
				sums[lane.ScriptName(root, l.N)] = chain.Hash(data)
			}
			if err := stubLane(root, cfg, l.N, "cleaned: archived as "+name); err != nil {
				return err
			}
			stubbed++
		}
		if !o.asYAML {
			fmt.Printf("archived %d lanes as %s; stubbed %d scripts\n", archived, name, stubbed)
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

	if !o.dryRun && (archived > 0 || len(removed) > 0) {
		detail := fmt.Sprintf("%s: %d archived, %d stubbed, %d queued removed", name, archived, stubbed, len(removed))
		history.Log(root, history.Entry{Event: history.Clean, Detail: detail})
		var ns []string
		for _, l := range toClean {
			ns = append(ns, fmt.Sprint(l.N))
		}
		// Outputs: the stubbed scripts as they were (their SHA-256s).
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
// name: clean-<stamp>, made unique (-2, -3, ...) if an archive has it. A
// finished lane's script is stubbed only if it is the one its last round
// ran (an edit since is new work) and isn't a stub already.
func cleanPlan(root string, cfg *config.Config, stamp string) ([]cleanLane, string) {
	st, _ := status.Load(root)
	var out []cleanLane
	for _, n := range lane.Associated(root) {
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
			continue // nothing to archive: a script nothing has run yet is new work
		case cl.State == status.Running || cl.State == status.Waiting || cl.State == status.Locked || cl.State == status.Queued:
			if pid, ok := lane.Running(root, n); ok {
				cl.Skip = fmt.Sprintf("running (pid %d)", pid)
			} else if l != nil && l.Alive() {
				cl.Skip = fmt.Sprintf("%s in a swim run (pid %d)", cl.State, l.PID)
			} else {
				cl.State = "stale"
			}
		}
		if cl.State == "" || cl.State == status.Idle {
			cl.State = "finished"
		}
		if cl.Skip == "" {
			cl.Stub, cl.Kept = stubbable(root, n, l)
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

// hasLog reports whether lane n has a current log (its own, or a
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

// stubbable says whether swim clean stubs lane n's script, or why it
// keeps it: no script, already a stub, a claim another session holds, or
// edited since its last round ran (l: its status entry).
func stubbable(root string, n int, l *status.Lane) (bool, string) {
	info, err := lane.ReadScript(root, n)
	switch {
	case err != nil || lane.ScriptName(root, n) == "":
		return false, "no script"
	case info.Stub:
		return false, "a stub already"
	case lane.CheckClaim(root, n, lane.SessionID()) != nil:
		return false, "claimed by another session"
	}
	data, err := lane.ScriptBytes(root, n)
	if err != nil || l == nil || l.ScriptSHA == "" || chain.Hash(data) != l.ScriptSHA {
		return false, "changed since its last round"
	}
	return true, ""
}
