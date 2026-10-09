package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
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
	if len(rest) > 1 {
		return usagef("usage: swim status [N|JOB] [--yaml] [--rebuild]")
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	warnVersion(root)
	if run != "" {
		return printRunStatus(root, cfg, run, ui.Painter{On: ui.ColorEnabled(os.Stdout)})
	}
	only := 0
	if len(rest) == 1 {
		if only, err = laneRef(root, cfg, rest[0], true); err != nil {
			return err
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
	if raw {
		data, err := os.ReadFile(status.Path(root))
		if err != nil {
			return err
		}
		fmt.Print(string(data))
		return nil
	}
	f, err := status.Load(root)
	if err != nil {
		return err
	}
	f.Normalize(cfg.Lanes)
	printStatus(root, cfg, f, only, ui.Painter{On: ui.ColorEnabled(os.Stdout)}, time.Now())
	return nil
}

// effectiveState flags lanes whose recorded process is gone.
func effectiveState(l *status.Lane) string {
	if (l.State == status.Running || l.State == status.Waiting) && l.PID > 0 && !lane.Alive(l.PID) {
		return l.State + "?"
	}
	return l.State
}

func printStatus(root string, cfg *config.Config, f *status.File, only int, p ui.Painter, now time.Time) {
	title := fmt.Sprintf("swim status · %s", filepath.Base(root))
	if f.Branch != "" {
		title += " · " + f.Branch
	}
	if f.UpdatedAt != "" {
		title += " · updated " + f.UpdatedAt
	}
	fmt.Println(p.Paint(ui.Bold, title))
	for i := range f.Lanes {
		l := &f.Lanes[i]
		if l.Lane > cfg.Lanes || (only != 0 && l.Lane != only) {
			continue
		}
		state := effectiveState(l)
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
			if l.StartedAt != nil {
				if t, err := time.Parse(time.RFC3339, *l.StartedAt); err == nil {
					word += " " + display.Elapsed(now.Sub(t))
				}
			}
		case status.Running + "?", status.Waiting + "?":
			word = state + " (process gone: interrupted)"
		}
		label := p.Paint(ui.LaneColor(l.Lane)+ui.Bold, fmt.Sprintf(" swim %-2d", l.Lane))
		round := l.Round
		if state == status.Idle {
			round = ""
		}
		line := fmt.Sprintf("%s %s", label, p.Paint(ui.StateColor(state), fmt.Sprintf("%-22s", word)))
		if round != "" {
			line += " " + round
		}
		fmt.Println(line)

		indent := strings.Repeat(" ", 10)
		detail := func(s string, color string) { fmt.Println(indent + p.Paint(color, s)) }
		if round != "" && l.Job != "" {
			detail("job: "+l.Job, ui.Dim)
		}
		if l.Pending != "" && (l.Pending != round || (l.PendingJob != "" && l.PendingJob != l.Job)) {
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
		if len(l.Stages) > 0 && state != status.Running {
			detail("stages: "+strings.Join(l.Stages, " | "), ui.Dim)
		}
		if l.Pending != "" && state != status.Running {
			if info, err := lane.ReadScript(root, l.Lane); err == nil && info.Pending() {
				var meta []string
				if info.Owner != "" {
					meta = append(meta, "owner "+info.Owner)
				}
				if info.Timeout > 0 {
					meta = append(meta, "timeout "+info.TimeoutText)
				}
				if len(info.Guards) > 0 {
					meta = append(meta, "guards "+strings.Join(info.GuardFlags(), " "))
				}
				if len(meta) > 0 {
					detail(strings.Join(meta, " · "), ui.Dim)
				}
			}
		}
		if l.Reason != "" {
			detail("reason: "+l.Reason, ui.Yellow)
		}
		for _, fs := range l.FailedSteps {
			detail(fs, ui.Red)
		}
		if l.LastArchive != "" && state == status.Idle {
			detail("last archive: "+l.LastArchive, ui.Dim)
		}
	}
	fmt.Println(p.Paint(ui.Dim, "raw: "+rel(root, status.Path(root))+"   logs: swim log N"))
}

// rebuildStatus reconstructs every lane's entry from its lane script and log.
func rebuildStatus(root string, cfg *config.Config) error {
	return status.UpdateFile(root, cfg.Lanes, func(f *status.File) error {
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
				if pid, ok := lane.Running(root, l.Lane); ok {
					l.State, l.PID = status.Running, pid
				} else {
					l.State = status.Interrupted
				}
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
