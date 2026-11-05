package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/launcher"
	"github.com/gates-brightly/swimlane/internal/queue"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// inSwimRepo reports whether the working directory is in a repo swim init
// set up (it has .swim/ or .swim.lock): there, bare swim answers "what
// needs me?"; anywhere else it prints the help.
func inSwimRepo() bool {
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	root := config.RepoRoot(cwd)
	for _, p := range []string{".swim", ".swim.lock"} {
		if _, err := os.Stat(filepath.Join(root, p)); err == nil {
			return true
		}
	}
	return false
}

// attentionItem is one line of bare swim's view: who, what, and the
// command that acts on it.
type attentionItem struct{ who, what, next string }

// cmdAttention is bare swim in a swim repo: what needs the operator now
// (a failed, dry-run or interrupted round nobody has rewritten, a lane
// asking for an answer, a lane lint refuses, a batch no swimmer will
// take), what's running, and what's ready to queue. Everything else is in
// swim status, swim queue and swim report.
func cmdAttention() error {
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	if err := refreshStatus(root, cfg); err != nil {
		return err
	}
	f, err := status.Load(root)
	if err != nil {
		return err
	}
	f.Normalize(cfg.Lanes)
	entries, err := queue.List(root, false)
	if err != nil {
		return err
	}
	now := time.Now()
	refused := lintRefusals(root, cfg)

	queued := map[int]bool{} // lanes in a batch that hasn't ended
	var needs, running []attentionItem
	swimmerUp := workerRunning(root)
	for _, e := range entries {
		var ns []string
		for _, l := range e.Lanes {
			ns = append(ns, strconv.Itoa(l.Lane))
			queued[l.Lane] = true
		}
		who := fmt.Sprintf("#%d", e.Seq)
		lanes := "swim " + strings.Join(ns, ",")
		switch e.State {
		case queue.Orphaned:
			needs = append(needs, attentionItem{who, lanes + ": its swimmer is gone", "swim queue requeue " + strconv.Itoa(e.Seq)})
		case queue.Queued:
			if !swimmerUp {
				needs = append(needs, attentionItem{who, lanes + ": queued, no swimmer up", "swim watch"})
			} else {
				running = append(running, attentionItem{who, lanes + ": queued", ""})
			}
		case queue.Running:
			running = append(running, attentionItem{who, lanes + ": running", ""})
		}
	}

	var ready []string
	for i := range f.Lanes {
		l := &f.Lanes[i]
		if !cfg.IsActive(l.Lane) {
			continue
		}
		who := "swim " + strconv.Itoa(l.Lane)
		state := effectiveState(l)
		// The round that ended is still the script on disk: nobody has
		// edited, rewritten or archived it since.
		current := false
		if data, err := lane.ScriptBytes(root, l.Lane); err == nil && l.ScriptSHA != "" {
			current = chain.Hash(data) == l.ScriptSHA
		}
		switch {
		case state == status.Running && l.PausedAt != "":
			needs = append(needs, attentionItem{who, "asking for your approval where it runs", ""})
			continue
		case state == status.Running+"?" || state == status.Waiting+"?":
			needs = append(needs, attentionItem{who, "interrupted: its process is gone", "swim log " + strconv.Itoa(l.Lane)})
			continue
		case state == status.Running || state == status.Waiting || state == status.Locked || state == status.Queued:
			running = append(running, attentionItem{who, stateWord(l, state, now), ""})
			continue
		case current && state == status.Failed:
			needs = append(needs, attentionItem{who, stateWord(l, state, now) + " · " + l.Round, "swim log " + strconv.Itoa(l.Lane)})
			continue
		case current && state == status.Interrupted:
			needs = append(needs, attentionItem{who, "INTERRUPTED · " + l.Round, "swim log " + strconv.Itoa(l.Lane)})
			continue
		case current && state == status.Skipped && len(l.DryRun) > 0:
			needs = append(needs, attentionItem{who, "dry run: " + strings.Join(l.DryRun, ", ") + " not approved · " + l.Round, "swim status " + strconv.Itoa(l.Lane)})
			continue
		}
		if fs := refused[l.Lane]; len(fs) > 0 {
			needs = append(needs, attentionItem{who, fmt.Sprintf("won't run: %s · %s", plural(len(fs), "lint error"), l.Pending), "swim lint " + strconv.Itoa(l.Lane)})
			continue
		}
		info, err := lane.ReadScript(root, l.Lane)
		if err != nil || !info.Pending() || queued[l.Lane] || launcher.AlreadyPassed(info, l) {
			continue
		}
		ready = append(ready, strconv.Itoa(l.Lane))
	}

	p := ui.Painter{On: ui.ColorEnabled(os.Stdout)}
	title := "swim · " + filepath.Base(root)
	if f.Branch != "" && f.Branch != "-" {
		title += " · " + f.Branch
	}
	fmt.Println(p.Paint(ui.Bold, title))
	section := func(name, color string, items []attentionItem) {
		if len(items) == 0 {
			return
		}
		fmt.Println(p.Paint(color, name))
		for _, it := range items {
			line := fmt.Sprintf("  %-8s %s", it.who, it.what)
			if it.next != "" {
				line += p.Paint(ui.Dim, "  → "+it.next)
			}
			fmt.Println(line)
		}
	}
	section("needs you", ui.Red, needs)
	section("running", ui.Cyan, running)
	if len(ready) > 0 {
		fmt.Println(p.Paint(ui.Green, "ready"))
		fmt.Printf("  swim %s%s\n", strings.Join(ready, ","), p.Paint(ui.Dim, "  → swim queue "+strings.Join(ready, " ")))
	}
	if len(needs)+len(running)+len(ready) == 0 {
		fmt.Println("nothing needs you, and nothing is running.")
	}
	fmt.Println(p.Paint(ui.Dim, "every lane: swim status · since you last looked: swim report · commands: swim --help"))
	return nil
}
