package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
)

func cmdStatus(args []string) error {
	var raw, rebuild bool
	rest, err := flags{bools: map[string]*bool{"yaml": &raw, "rebuild": &rebuild}}.parse(args)
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
			detail("step: "+l.CurrentStep, ui.Cyan)
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
