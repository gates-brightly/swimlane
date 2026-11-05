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
	warnVersion(root)
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
		// status.File's fields, each lane with its pending round's header
		// and dependencies added (not in status.yml itself).
		type laneDoc struct {
			status.Lane   `yaml:",inline"`
			PendingHeader *pendingHeader `yaml:"pending_header,omitempty"`
			Deps          []depDoc       `yaml:"deps,omitempty"`
			DepsError     string         `yaml:"deps_error,omitempty"`
			// LintErrors counts the lint errors its pending round has: any
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
		for i := range lanes {
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

// effectiveState flags lanes whose recorded process is gone.
func effectiveState(l *status.Lane) string {
	if (l.State == status.Running || l.State == status.Waiting) && !l.Alive() {
		return l.State + "?"
	}
	return l.State
}

// stateWord is how swim status names a lane's state: "PASS", "FAIL exit
// 1", "waiting on swim 3", "running 0:42".
func stateWord(l *status.Lane, state string, now time.Time) string {
	word := state
	switch state {
	case status.Passed:
		word = "PASS"
	case status.Failed:
		word = "FAIL"
		if l.ExitCode != nil {
			word = fmt.Sprintf("FAIL exit %d", *l.ExitCode)
		}
	case status.Skipped:
		word = "SKIP"
	case status.Interrupted:
		word = "INTERRUPTED"
	case status.Waiting:
		word = display.WaitingText(l.WaitingOn)
	case status.Running:
		word = "running"
		if d, ok := l.Ran(now); ok { // its clock stops while it asks
			word += " " + display.Elapsed(d)
		}
		if !proc.Local(l.Host) {
			word += " on " + l.Host // its process is there, not here
		}
	case status.Running + "?", status.Waiting + "?":
		word = state + " (process gone: interrupted)"
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
		fmt.Println(p.Paint(ui.Yellow, "lock "+h.Name+" held by "+h.By))
	}
	sv := newStatusView(root, cfg)
	refused := lintRefusals(root, cfg)
	for i := range f.Lanes {
		l := &f.Lanes[i]
		// The lanes in use; those named (swim status N M) whatever they are.
		if len(only) > 0 && !slices.Contains(only, l.Lane) || len(only) == 0 && !cfg.IsActive(l.Lane) {
			continue
		}
		state := effectiveState(l)
		word := stateWord(l, state, now)
		label := p.Paint(ui.LaneColor(l.Lane)+ui.Bold, fmt.Sprintf(" swim %-2d", l.Lane))
		round := l.Round
		pv := sv.pending(l)
		// A lane that never ran (or was archived) shows its pending round
		// as its row.
		idlePending := state == status.Idle && pv != nil && l.Pending != ""
		color := ui.StateColor(state)
		switch {
		case idlePending:
			word, round, color = "pending", l.Pending, ui.Cyan
		case state == status.Idle:
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
			detail("job: "+l.PendingJob, ui.Dim)
		} else if round != "" && l.Job != "" {
			detail("job: "+l.Job, ui.Dim)
		}
		if idlePending {
			// The row says it.
		} else if l.Pending != "" && (l.Pending != round || (l.PendingJob != "" && l.PendingJob != l.Job)) {
			pending := "pending: " + l.Pending
			if l.PendingJob != "" {
				pending += "  (job " + l.PendingJob + ")"
			}
			detail(pending, ui.Cyan)
		} else if l.Pending == "" && state != status.Running {
			if info, _ := lane.ReadScript(root, l.Lane); info.Stub {
				detail("stub: "+info.Message, ui.Dim)
			} else if !info.Exists {
				detail("no lane script", ui.Dim)
			}
		}
		if state != status.Idle && state != status.Waiting && state != status.Skipped {
			counts := fmt.Sprintf("pass %d  fail %d  skip %d  drift %d", l.Pass, l.Fail, l.Skip, l.Drift)
			if l.FinishedAt != nil {
				counts += "  finished " + *l.FinishedAt
				if l.DurationS != nil {
					counts += fmt.Sprintf(" (%s)", display.Elapsed(time.Duration(*l.DurationS*float64(time.Second))))
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
		if state == status.Running && l.Message != "" {
			m := l.Message
			if l.Progress != "" {
				m += " (" + l.Progress + ")"
			}
			if t, err := time.Parse(time.RFC3339, l.MessageAt); err == nil && time.Since(t) > time.Minute {
				m += " · " + display.Elapsed(time.Since(t)) + " ago"
			}
			detail("msg: "+m, ui.Dim)
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
		if l.Reason != "" {
			detail("reason: "+l.Reason, ui.Yellow)
		}
		// Loud on purpose: nobody was asked about these guards.
		if t := l.UnpromptedText(); t != "" {
			color := ui.Dim
			if len(l.AutoApproved) > 0 {
				color = ui.Yellow + ui.Bold
			}
			detail("approved without a prompt: "+t, color)
		}
		if len(l.DryRun) > 0 {
			detail(display.DryMark+" dry run (not approved): "+strings.Join(l.DryRun, ", "), ui.Yellow)
		}
		for _, fs := range l.FailedSteps {
			detail(fs, ui.Red)
		}
		if l.LastArchive != "" && state == status.Idle {
			detail("last archive: "+l.LastArchive, ui.Dim)
		}
	}
	if entries, err := queue.List(root, false); err == nil && len(entries) > 0 {
		waiting, running, orphaned := 0, 0, 0
		for _, e := range entries {
			switch e.State {
			case queue.Queued:
				waiting++
			case queue.Running:
				running++
			case queue.Orphaned:
				orphaned++
			}
		}
		line := fmt.Sprintf("queue: %d waiting, %d running", waiting, running)
		if orphaned > 0 {
			line += fmt.Sprintf(", %d orphaned", orphaned)
		}
		fmt.Println(p.Paint(ui.Yellow, line+"   (swim queue; a swimmer: swim watch)"))
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
			archive := l.LastArchive
			l.ResetRun()
			l.LastArchive = archive
			if !r.Found {
				l.State, l.Round, l.Job = status.Idle, "", ""
				continue
			}
			l.Round, l.Job = r.Title, r.Job
			l.StartedAt = status.Str(r.StartedAt)
			l.Pass, l.Fail, l.Skip, l.Drift = r.Pass, r.Fail, r.Skip, r.Drift
			for _, fr := range r.Failed() {
				l.FailedSteps = append(l.FailedSteps, fr.Text())
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
				if f[i] == "swim" {
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
		label := p.Paint(ui.LaneColor(n)+ui.Bold, fmt.Sprintf(" swim %-2d", n))
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
			word := "PASS"
			switch {
			case !round.Finished:
				word = "running?"
			case round.Interrupt:
				word = "INTERRUPTED"
			case round.ExitCode != 0:
				word = fmt.Sprintf("FAIL exit %d", round.ExitCode)
			case round.DryRun:
				word = "SKIP (dry run)"
			}
			fmt.Printf("%s %s %s\n", label, p.Paint(ui.StateColor(word), fmt.Sprintf("%-22s", word)), round.Title)
			fmt.Println(strings.Repeat(" ", 10) + p.Paint(ui.Dim, fmt.Sprintf("job: %s  pass %d  fail %d  skip %d  drift %d  stages: %s",
				round.Job, round.Pass, round.Fail, round.Skip, round.Drift, logparse.StagesText(round.StageResults()))))
			for _, fr := range round.Failed() {
				fmt.Println(strings.Repeat(" ", 10) + p.Paint(ui.Red, fr.Text()))
			}
		case skips[n] != "":
			found++
			fmt.Printf("%s %s %s\n", label, p.Paint(ui.Yellow, fmt.Sprintf("%-22s", "SKIP")), skips[n])
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
