package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/observe"
	"github.com/gates-brightly/swimlane/internal/runrec"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// cmdObserve is swim observe [RUN|N|JOB] [--yaml]: a run's observations
// (the last run's by default; for N or JOB, that lane's or job's in the
// last run it ran in). Facts computed from the record, with their
// numbers and the runs they cite; they decide nothing.
func cmdObserve(args []string) error {
	if len(args) > 0 && args[0] == "check" {
		return observeCheck(args[1:])
	}
	var asYAML bool
	rest, err := flags{bools: map[string]*bool{"yaml": &asYAML}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return usagef("usage: swim observe [RUN|N|JOB] [--yaml]")
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	runs, err := runrec.List(root)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		return fmt.Errorf("no recorded runs yet: run some lanes with swim run first")
	}
	ref := strings.Join(rest, "")
	var rec *runrec.Run
	keep := func(observe.Observation) bool { return true }
	switch {
	case ref == "":
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
	default:
		// A lane or a job: the last run it ran in, its observations only.
		n, err := strconv.Atoi(ref)
		match := func(l runrec.Lane) bool { return lane.MatchJob(l.Job, ref) }
		if err == nil {
			if n, err = laneArg(cfg, ref); err != nil {
				return err
			}
			match = func(l runrec.Lane) bool { return l.Lane == n }
		}
		for _, r := range runs {
			for _, l := range r.Lanes {
				if match(l) {
					rec, n = r, l.Lane
				}
			}
		}
		keep = func(o observe.Observation) bool { return o.Lane == n }
	}
	if rec == nil {
		return fmt.Errorf("no recorded run for %q (swim timeline lists runs)", ref)
	}
	f, ok, err := observe.Load(root, rec.Run)
	if err != nil {
		return err
	}
	if !ok {
		// A run from before observations were recorded: computed now, not
		// written (this command only reads).
		in, err := observe.Gather(root, rec.Run)
		if err != nil {
			return err
		}
		f = &observe.File{Schema: observe.Schema, Run: rec.Run, Observations: observe.Compute(in, cfg.ObserveThresholds())}
	}
	var shown []observe.Observation
	for _, o := range f.Observations {
		if keep(o) {
			shown = append(shown, o)
		}
	}
	if asYAML {
		if shown == nil {
			shown = []observe.Observation{}
		}
		return yaml.NewEncoder(os.Stdout).Encode(observe.File{Schema: observe.Schema, Run: f.Run, Observations: shown})
	}
	p := ui.Painter{On: ui.ColorEnabled(os.Stdout)}
	fmt.Println(p.Paint(ui.Bold, fmt.Sprintf("swim observe · run %s · %d lanes · %d observations", rec.Run, len(rec.Lanes), len(shown))))
	if len(shown) == 0 {
		fmt.Println(p.Paint(ui.Dim, "  nothing out of the ordinary"))
	}
	for _, o := range shown {
		printObservation(p, o, "")
	}
	return nil
}

// printObservation writes one observation as a line: its kind, lane, the
// fact, and (dim) its id and the runs it cites.
func printObservation(p ui.Painter, o observe.Observation, indent string) {
	printObservationTo(os.Stdout, p, o, indent)
}

func printObservationTo(w io.Writer, p ui.Painter, o observe.Observation, indent string) {
	cites := ""
	if len(o.Cites) > 1 {
		cites = " · runs " + strings.Join(o.Cites, ", ")
	}
	fmt.Fprintf(w, "%s%-8s swim %-3d %s  %s\n", indent, p.Paint(ui.Yellow, o.Kind), o.Lane, o.Text, p.Paint(ui.Dim, o.ID+cites))
}

// observeCheck is swim observe check FILE [RUN]: the observation ids a
// write-up (FILE) cites that the run (the last one by default) doesn't
// have, so a reviewer can spot an invented citation. Exit 1 on any.
func observeCheck(args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return usagef("usage: swim observe check FILE [RUN]")
	}
	root, _, err := repo()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	run := ""
	if len(args) == 2 {
		run = args[1]
	} else if st, _ := status.Load(root); st != nil {
		run = st.LastRun
	}
	if run == "" {
		return fmt.Errorf("no run to check against: name one (swim observe check FILE RUN)")
	}
	// The run's observations, and those its finally lanes were given
	// (SWIM_OBSERVATIONS: a write-up cites those).
	have := map[string]bool{}
	files, _ := filepath.Glob(filepath.Join(observe.Dir(root), run+".for-*.yml"))
	for _, path := range append(files, observe.Path(root, run)) {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var f observe.File
		if yaml.Unmarshal(b, &f) == nil {
			for _, o := range f.Observations {
				have[o.ID] = true
			}
		}
	}
	cited := observe.Cited(string(data))
	var bad []string
	for _, id := range cited {
		if !have[id] {
			bad = append(bad, id)
		}
	}
	fmt.Printf("%s cites %d observations of run %s", args[0], len(cited), run)
	if len(bad) == 0 {
		fmt.Println(": each one is there")
		return nil
	}
	fmt.Printf("; not in the run: %s\n", strings.Join(bad, ", "))
	return exitError{1}
}
