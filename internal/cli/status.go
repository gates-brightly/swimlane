package cli

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/finding"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/lint"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/proc"
	"github.com/gates-brightly/swimlane/internal/queue"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
)

func cmdStatus(args []string) error {
	var raw, rebuild bool
	var run string
	rest, err := flags{bools: map[string]*bool{"yaml": &raw, "rebuild": &rebuild}, strs: map[string]*string{"run": &run}}.parse(args)
	if err != nil {
		return err
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	if run != "" {
		return printRunStatus(root, cfg, run, ui.Painter{On: ui.ColorEnabled(os.Stdout)})
	}
	var only []int // the lanes named (none: every lane in use)
	for _, a := range laneArgs(rest) {
		n, err := laneRef(root, cfg, a, true)
		if err != nil {
			return err
		}
		if !slices.Contains(only, n) {
			only = append(only, n)
		}
	}
	if rebuild {
		if err := rebuildStatus(root, cfg); err != nil {
			return err
		}
	}
	// Pending rounds come from the lane scripts on disk, which planners may
	// have written by hand; refresh them so the file stays truthful.
	if err := refreshStatus(root, cfg); err != nil {
		return err
	}
	f, err := status.Load(root)
	if err != nil {
		return err
	}
	f.Normalize(cfg.Lanes)
	if raw {
		// The file's contents with a schema tag; the file itself stays as
		// it is, for cat. A lane argument narrows it to that lane.
		// status.File's fields, each lane with its ready round's header
		// and dependencies added (not in status.yml itself).
		type laneDoc struct {
			status.Lane   `yaml:",inline"`
			PendingHeader *pendingHeader `yaml:"pending_header,omitempty"`
			Deps          []depDoc       `yaml:"deps,omitempty"`
			DepsError     string         `yaml:"deps_error,omitempty"`
			// LintErrors counts the lint errors its ready round has: any
			// means swim run and swim queue refuse it (swim lint N).
			LintErrors int `yaml:"lint_errors,omitempty"`
		}
		doc := struct {
			Schema    string    `yaml:"schema"`
			UpdatedAt string    `yaml:"updated_at"`
			Root      string    `yaml:"root"`
			Branch    string    `yaml:"branch,omitempty"`
			LastRun   string    `yaml:"last_run,omitempty"`
			Lanes     []laneDoc `yaml:"lanes"`
			// Claims are the lanes' live claims (swim new takes one), by
			// lane: not in status.yml itself.
			Claims map[int]*lane.Claim `yaml:"claims,omitempty"`
		}{Schema: "swim.status/v1", UpdatedAt: f.UpdatedAt, Root: f.Root, Branch: f.Branch, LastRun: f.LastRun}
		lanes := f.Lanes
		if len(only) > 0 {
			lanes = nil
			for _, n := range only {
				lanes = append(lanes, *f.Get(n))
			}
		}
		sv := newStatusView(root, cfg)
		refused := lintRefusals(root, cfg)
		inBatch := queuedLanes(root)
		for i := range lanes {
			if inBatch[lanes[i].Lane] && lanes[i].AtRest() {
				lanes[i].State = status.Queued
			}
			ld := laneDoc{Lane: lanes[i], LintErrors: len(refused[lanes[i].Lane])}
			if pv := sv.pending(&lanes[i]); pv != nil {
				ld.Deps, ld.DepsError = pv.Deps, pv.DepsErr
				if !reflect.ValueOf(pv.Header).IsZero() {
					ld.PendingHeader = &pv.Header
				}
			}
			doc.Lanes = append(doc.Lanes, ld)
		}
		for _, l := range lanes {
			if c := lane.ReadClaim(root, l.Lane); c != nil {
				if doc.Claims == nil {
					doc.Claims = map[int]*lane.Claim{}
				}
				doc.Claims[l.Lane] = c
			}
		}
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		if err := enc.Encode(doc); err != nil {
			return err
		}
		return enc.Close()
	}
	printStatus(root, cfg, f, only, ui.Painter{On: ui.ColorEnabled(os.Stdout)}, time.Now())
	return nil
}

// queuedLanes are the lanes in a batch still queued (swim queue): a lane
// at rest there is queued, not ready.
func queuedLanes(root string) map[int]bool {
	out := map[int]bool{}
	entries, _ := queue.List(root, false)
	for _, e := range entries {
		if e.State == queue.Queued {
			for _, l := range e.Lanes {
				out[l.Lane] = true
			}
		}
	}
	return out
}

// effectiveState flags lanes whose recorded process is gone.
func effectiveState(l *status.Lane) string {
	if (l.State == status.Running || l.State == status.Waiting) && !l.Alive() {
		return l.State + "?"
	}
	return l.State
}

// stateWord is how swim status names a lane's state: "passed", "failed
// exit 1", "waiting on lane 3", "running 42s".
func stateWord(l *status.Lane, state string, now time.Time) string {
	word := state
	switch state {
	case status.Failed:
		if l.ExitCode != nil {
			word = fmt.Sprintf("failed exit %d", *l.ExitCode)
		}
	case status.Waiting:
		word = display.WaitingText(l.WaitingOn)
		if len(l.WaitingOn) == 0 && strings.HasPrefix(l.Reason, "waiting ") {
			word = l.Reason // for a lock or a slot
		}
	case status.Running:
		if l.PausedAt != "" {
			word = "waiting for approval" // its clock stops while it asks
			break
		}
		word = "running"
		if d, ok := l.Ran(now); ok {
			word += " " + display.Duration(d)
		}
		if !proc.Local(l.Host) {
			word += " on " + l.Host // its process is there, not here
		}
	case status.Running + "?", status.Waiting + "?":
		word = "interrupted (its process is gone)"
	}
	return word
}

func printStatus(root string, cfg *config.Config, f *status.File, only []int, p ui.Painter, now time.Time) {
	title := fmt.Sprintf("swim status · %s", filepath.Base(root))
	if f.Branch != "" {
		title += " · " + f.Branch
	}
	if f.UpdatedAt != "" {
		title += " · updated " + f.UpdatedAt
	}
	fmt.Println(p.Paint(ui.Bold, title))
	for _, h := range lane.HeldLocks(root) {
		fmt.Println(p.Paint(ui.Yellow, "lock "+h.Name+": "+h.By))
	}
	sv := newStatusView(root, cfg)
	refused := lintRefusals(root, cfg)
	inBatch := queuedLanes(root)
	for i := range f.Lanes {
		l := &f.Lanes[i]
		// The lanes in use; those named (swim status N M) whatever they are.
		if len(only) > 0 && !slices.Contains(only, l.Lane) || len(only) == 0 && !cfg.IsActive(l.Lane) {
			continue
		}
		state := effectiveState(l)
		if inBatch[l.Lane] && l.AtRest() {
			state = status.Queued
		}
		word := stateWord(l, state, now)
		label := p.Paint(ui.LaneColor(l.Lane)+ui.Bold, fmt.Sprintf(" lane %-2d", l.Lane))
		round := l.Round
		pv := sv.pending(l)
		// A lane that never ran (or was archived) shows the round it holds
		// as its row.
		idlePending := (state == status.Ready || state == status.Queued) && pv != nil && l.Pending != ""
		color := ui.StateColor(state)
		switch {
		case idlePending:
			round = l.Pending
		case state == status.Ready || state == status.Empty:
			round = ""
		}
		line := fmt.Sprintf("%s %s", label, p.Paint(color, fmt.Sprintf("%-22s", word)))
		if round != "" {
			line += " " + round
		}
		if info, _ := lane.ReadScript(root, l.Lane); info.Task != "" {
			line += p.Paint(ui.Dim, "  ["+info.Task+"]")
		}
		fmt.Println(line)

		indent := strings.Repeat(" ", 10)
		detail := func(s string, color string) { fmt.Println(indent + p.Paint(color, s)) }
		if idlePending && l.PendingJob != "" {
			detail("round-id: "+l.PendingJob, ui.Dim)
		} else if round != "" && l.Job != "" {
			detail("round-id: "+l.Job, ui.Dim)
		}
		if idlePending {
			// The row says it.
		} else if l.Pending != "" && (l.Pending != round || (l.PendingJob != "" && l.PendingJob != l.Job)) {
			next := "ready: " + l.Pending
			if l.PendingJob != "" {
				next += "  (round-id " + l.PendingJob + ")"
			}
			detail(next, ui.Cyan)
		} else if l.Pending == "" && state != status.Running {
			if info, _ := lane.ReadScript(root, l.Lane); info.Stub {
				detail("note: "+info.Message, ui.Dim)
			} else if !info.Exists && l.LastNote != "" {
				detail("note: "+l.LastNote, ui.Dim)
			} else if !info.Exists && l.LastArchive == "" {
				detail("no lane script", ui.Dim)
			}
		}
		if !l.AtRest() && state != status.Waiting && state != status.Skipped {
			counts := fmt.Sprintf("pass %d  fail %d  skip %d", l.Pass, l.Fail, l.Skip)
			if l.FinishedAt != nil {
				counts += "  ended " + *l.FinishedAt
				if l.DurationS != nil {
					counts += fmt.Sprintf(" (%s)", display.Seconds(*l.DurationS))
				}
			}
			detail(counts, ui.Dim)
		}
		if (state == status.Running || strings.HasSuffix(state, "?")) && l.CurrentStep != "" {
			step := "step: " + l.CurrentStep
			if l.Stage != "" {
				step = "stage: " + l.Stage + "  " + step
			}
			detail(step, ui.Cyan)
		}
		if len(l.Locks) > 0 && state == status.Running {
			detail("locks: "+strings.Join(l.Locks, ", "), ui.Dim)
		}
		if len(l.Stages) > 0 && state != status.Running {
			detail("stages: "+strings.Join(l.Stages, " | "), ui.Dim)
		}
		if pv != nil {
			pv.print(p, indent)
		}
		// Your own claim (swim new took it) is no news: another session's is.
		if c := lane.ReadClaim(root, l.Lane); c != nil && c.Session != lane.SessionID() && state != status.Running {
			detail(fmt.Sprintf("claimed by %s (%s) until %s", c.Owner, c.Session, c.ExpiresAt().Local().Format("15:04")), ui.Cyan)
		}
		if fs := refused[l.Lane]; len(fs) > 0 && state != status.Running {
			noun := "lint error"
			if len(fs) > 1 {
				noun += "s"
			}
			detail(fmt.Sprintf("won't run: %d %s (swim lint %d): %s:%d %s [%s]", len(fs), noun, l.Lane, fs[0].File, fs[0].Line, fs[0].Message, fs[0].Code), ui.Red)
		}
		if l.Reason != "" && l.Reason != word {
			detail("reason: "+l.Reason, ui.Yellow)
		}
		// Loud on purpose: nobody was asked about these guards.
		if t := l.UnpromptedText(); t != "" {
			detail("approved without a prompt: "+t, ui.Dim)
		}
		if len(l.DryRun) > 0 {
			detail(display.DryMark+" dry run (not approved): "+strings.Join(l.DryRun, ", "), ui.Yellow)
		}
		for _, fs := range l.FailedSteps {
			detail(fs, ui.Red)
		}
		if l.LastArchive != "" && l.AtRest() {
			detail("last archive: "+l.LastArchive, ui.Dim)
		}
	}
	if line := passedLine(root, f, cfg); line != "" && len(only) == 0 {
		fmt.Println(p.Paint(ui.Dim, line))
	}
	if entries, err := queue.List(root, false); err == nil && len(entries) > 0 {
		queued, running, orphaned := 0, 0, 0
		for _, e := range entries {
			switch e.State {
			case queue.Queued:
				queued++
			case queue.Running:
				running++
			case queue.Orphaned:
				orphaned++
			}
		}
		line := fmt.Sprintf("queue: %d queued, %d running", queued, running)
		if orphaned > 0 {
			line += fmt.Sprintf(", %d orphaned", orphaned)
		}
		fmt.Println(p.Paint(ui.Yellow, line+"   (swim queue; the watcher: swim watch)"))
	}
	fmt.Println(p.Paint(ui.Dim, "raw: "+rel(root, status.Path(root))+"   logs: swim log N"))
}

// rebuildStatus reconstructs every lane's entry from its lane script and log.
func rebuildStatus(root string, cfg *config.Config) error {
	return status.Rebuild(root, cfg.Lanes, func(f *status.File) error {
		for i := range f.Lanes {
			l := &f.Lanes[i]
			if l.Lane > cfg.Lanes {
				continue
			}
			r, err := logparse.ParseFile(lane.Log(root, l.Lane))
			if err != nil {
				return err
			}
			archive, note := l.LastArchive, l.LastNote
			l.ResetRun()
			l.LastArchive, l.LastNote = archive, note
			if !r.Found {
				l.State, l.Round, l.Job = status.Empty, "", ""
				l.Rest()
				continue
			}
			l.Round, l.Job = r.Title, r.Job
			l.StartedAt = status.Str(r.StartedAt)
			l.Pass, l.Fail, l.Skip = r.Pass, r.Fail, r.Skip
			for _, fr := range r.Reasons() {
				l.FailedSteps = append(l.FailedSteps, fr.Brief())
			}
			switch {
			case !r.Finished:
				if pid, host, ok := lane.RunningOn(root, l.Lane); ok {
					l.State, l.PID, l.Host = status.Running, pid, host
				} else {
					l.State = status.Interrupted
				}
			case r.ExitCode == 0 && r.DryRun:
				l.State, l.Reason = status.Skipped, r.DryRunReason()
			case r.ExitCode == 0:
				l.State = status.Passed
			case r.ExitCode == 130 || r.ExitCode == 143 || r.Interrupt:
				l.State = status.Interrupted
			default:
				l.State = status.Failed
			}
			if r.Finished {
				l.ExitCode = status.Int(r.ExitCode)
			}
		}
		return nil
	})
}

// printRunStatus shows each lane as it was in swim run id: its round from
// the lane's current or archived logs, or, for a lane skipped before it
// started, the skip recorded in .swim.log.
func printRunStatus(root string, cfg *config.Config, id string, p ui.Painter) error {
	skips := map[int]string{}
	lanesInRun := map[int]bool{}
	if data, err := os.ReadFile(history.Path(root)); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if !strings.Contains(line, "  run="+id) {
				continue
			}
			f := strings.Fields(line)
			if len(f) < 3 {
				continue
			}
			n := 0
			for i := 3; i+1 < len(f); i++ {
				if f[i] == "lane" || f[i] == "swim" { // swim N in an older .swim.log
					fmt.Sscan(f[i+1], &n)
					break
				}
			}
			if f[2] == "run" {
				for _, x := range strings.Split(f[4], ",") {
					var k int
					if _, err := fmt.Sscan(x, &k); err == nil {
						lanesInRun[k] = true
					}
				}
			}
			if f[2] == history.Skip && n > 0 {
				_, after, _ := strings.Cut(line, "run="+id)
				skips[n] = strings.TrimSpace(after)
			}
		}
	}
	fmt.Println(p.Paint(ui.Bold, fmt.Sprintf("swim status · %s · run %s", filepath.Base(root), id)))
	found := 0
	for n := 1; n <= cfg.Lanes; n++ {
		label := p.Paint(ui.LaneColor(n)+ui.Bold, fmt.Sprintf(" lane %-2d", n))
		var round *logparse.Round
		for _, f := range append(archives(root, n), lane.Log(root, n)) {
			rs, err := roundsOf(f, id)
			if err != nil {
				return err
			}
			if len(rs) > 0 {
				if round, err = logparse.ParseRound(rs[len(rs)-1]); err != nil {
					return err
				}
			}
		}
		switch {
		case round != nil:
			found++
			word := status.Passed
			switch {
			case !round.Finished:
				word = "running (or its process is gone)"
			case round.Interrupt:
				word = status.Interrupted
			case round.ExitCode != 0:
				word = fmt.Sprintf("failed exit %d", round.ExitCode)
			case round.DryRun:
				word = "skipped (dry run)"
			}
			fmt.Printf("%s %s %s\n", label, p.Paint(ui.StateColor(word), fmt.Sprintf("%-22s", word)), round.Title)
			fmt.Println(strings.Repeat(" ", 10) + p.Paint(ui.Dim, fmt.Sprintf("round-id: %s  pass %d  fail %d  skip %d  stages: %s",
				round.Job, round.Pass, round.Fail, round.Skip, logparse.StagesText(round.StageResults()))))
			for _, fr := range round.Failed() {
				fmt.Println(strings.Repeat(" ", 10) + p.Paint(ui.Red, fr.Brief()))
			}
		case skips[n] != "":
			found++
			fmt.Printf("%s %s %s\n", label, p.Paint(ui.Yellow, fmt.Sprintf("%-22s", status.Skipped)), skips[n])
		case lanesInRun[n]:
			found++
			fmt.Printf("%s %s\n", label, p.Paint(ui.Dim, "in the run, but no record of its round"))
		}
	}
	if found == 0 {
		return fmt.Errorf("no lane has a record of run %s", id)
	}
	return nil
}

// lintRefusals are the lint errors swim run and swim queue would refuse
// each pending lane for, by lane (one lint run for them all).
func lintRefusals(root string, cfg *config.Config) map[int][]finding.Finding {
	var pending []int
	for n := 1; n <= cfg.Lanes; n++ {
		if info, err := lane.ReadScript(root, n); err == nil && info.Pending() {
			pending = append(pending, n)
		}
	}
	out := map[int][]finding.Finding{}
	if len(pending) == 0 {
		return out
	}
	fs, err := lint.Errors(root, cfg, pending)
	if err != nil {
		return out
	}
	for _, f := range fs {
		for _, n := range pending {
			if f.File == lane.ScriptName(root, n) {
				out[n] = append(out[n], f)
			}
		}
	}
	return out
}
