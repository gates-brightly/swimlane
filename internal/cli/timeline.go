package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/term"
	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/runrec"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/timeline"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// cmdTimeline draws when each lane of a run waited, ran and finished.
func cmdTimeline(args []string) error {
	var all, steps, asYAML bool
	var top, htmlOut string
	rest, err := flags{
		bools: map[string]*bool{"all": &all, "steps": &steps, "yaml": &asYAML},
		strs:  map[string]*string{"top": &top, "html": &htmlOut},
	}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return usagef("usage: swim timeline [RUN|N|JOB] [--top N] [--all] [--steps] [--yaml] [--html FILE]")
	}
	o := timeline.Options{All: all, Steps: steps, Color: ui.ColorEnabled(os.Stdout)}
	if top != "" {
		if o.Top, err = strconv.Atoi(top); err != nil || o.Top < 1 {
			return usagef("--top takes a number of lanes, got %q", top)
		}
	}
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		o.Width = w
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	runs, err := runrec.List(root)
	if err != nil {
		return err
	}

	ref := strings.Join(rest, "")
	var rec *runrec.Run
	switch {
	case ref == "":
		if len(runs) == 0 {
			return fmt.Errorf("no recorded runs yet: swim records each run in %s (from this version on); run some lanes with `swim run` first",
				rel(root, runrec.Dir(root)))
		}
		rec = runs[len(runs)-1]
		if st, _ := status.Load(root); st != nil && st.LastRun != "" {
			if r, ok, _ := runrec.Load(root, st.LastRun); ok {
				rec = r
			}
		}
	case strings.HasPrefix(ref, "r-"):
		for _, r := range runs {
			if r.Run == ref || strings.HasPrefix(r.Run, ref) {
				if rec != nil && rec.Run != r.Run {
					return fmt.Errorf("%q matches more than one run (%s, %s); give more of the run id", ref, rec.Run, r.Run)
				}
				rec = r
			}
		}
		if rec == nil {
			return fmt.Errorf("no recorded run %q; recorded runs are in %s", ref, rel(root, runrec.Dir(root)))
		}
	}

	if rec == nil {
		// A lane or a job: its rounds across runs.
		var h *timeline.History
		if _, err := strconv.Atoi(ref); err == nil {
			n, err := laneArg(cfg, ref)
			if err != nil {
				return err
			}
			h = timeline.HistoryOf(fmt.Sprintf("swim %d", n), runs, func(l runrec.Lane) bool { return l.Lane == n })
		} else {
			h = timeline.HistoryOf("job "+ref, runs, func(l runrec.Lane) bool { return lane.MatchJob(l.Job, ref) })
		}
		if htmlOut != "" {
			return usagef("--html draws one run; give a run id (swim timeline r-...)")
		}
		if asYAML {
			return yaml.NewEncoder(os.Stdout).Encode(h)
		}
		timeline.HistoryText(os.Stdout, h, o)
		return nil
	}

	var rounds func(int) *logparse.Round
	if steps || asYAML || htmlOut != "" {
		rounds = func(n int) *logparse.Round { return roundInRun(root, n, rec.Run) }
	}
	t := timeline.Build(rec, rounds)
	switch {
	case htmlOut != "":
		f, err := os.Create(htmlOut)
		if err != nil {
			return err
		}
		if err := timeline.HTML(f, t); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		fmt.Printf("swim: wrote %s (run %s)\n", htmlOut, t.Run)
		return nil
	case asYAML:
		return yaml.NewEncoder(os.Stdout).Encode(t)
	}
	timeline.Text(os.Stdout, t, o)
	return nil
}

// roundInRun finds lane n's round in run id, in its current log or any of
// its archived ones (a round archived with swim archive is still the run's
// history).
func roundInRun(root string, n int, run string) *logparse.Round {
	paths := []string{lane.Log(root, n)}
	archived, _ := filepath.Glob(filepath.Join(lane.LogDir(root), fmt.Sprintf("agent%d.prev-*.log", n)))
	paths = append(paths, archived...)
	for _, p := range paths {
		rounds, err := roundsOf(p, run)
		if err != nil || len(rounds) == 0 {
			continue
		}
		if r, err := logparse.ParseRound(rounds[len(rounds)-1]); err == nil {
			return r
		}
	}
	return nil
}
