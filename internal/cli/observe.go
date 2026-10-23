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
	if len(args) > 0 {
		switch args[0] {
		case "check":
			return observeCheck(args[1:])
		case "knowledge":
			return observeKnowledge(args[1:])
		case "accept", "reject":
			return observeDecide(args[0], args[1:])
		}
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
	ref := taskRef(root, cfg, strings.Join(rest, ""))
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

// observeKnowledge is swim observe knowledge [--yaml]: what the runs keep
// showing (proposed, for someone to accept or reject), and what people
// accepted (knowledge/steps.yml), with any that no longer holds.
func observeKnowledge(args []string) error {
	var asYAML bool
	if rest, err := (flags{bools: map[string]*bool{"yaml": &asYAML}}).parse(args); err != nil {
		return err
	} else if len(rest) > 0 {
		return usagef("usage: swim observe knowledge [--yaml]")
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	runs, err := observe.History(root)
	if err != nil {
		return err
	}
	k, err := observe.LoadKnowledge(root)
	if err != nil {
		return err
	}
	accepted, rejected := map[string]bool{}, observe.Rejected(root)
	for _, e := range k.Entries {
		accepted[e.ID] = true
	}
	var proposed []observe.Entry
	for _, e := range observe.Propose(runs, cfg.ObserveThresholds().Window) {
		if !accepted[e.ID] && !rejected[e.ID] {
			proposed = append(proposed, e)
		}
	}
	type acceptedDoc struct {
		observe.Entry `yaml:",inline"`
		Retired       string `yaml:"retired,omitempty"`
	}
	var acc []acceptedDoc
	for _, e := range k.Entries {
		acc = append(acc, acceptedDoc{e, observe.Retired(e, runs)})
	}
	if asYAML {
		if proposed == nil {
			proposed = []observe.Entry{}
		}
		if acc == nil {
			acc = []acceptedDoc{}
		}
		return yaml.NewEncoder(os.Stdout).Encode(map[string]any{"schema": "swim.knowledge-list/v1", "proposed": proposed, "accepted": acc})
	}
	p := ui.Painter{On: ui.ColorEnabled(os.Stdout)}
	fmt.Println(p.Paint(ui.Bold, fmt.Sprintf("swim observe knowledge · %d proposed · %d accepted", len(proposed), len(acc))))
	for _, e := range proposed {
		fmt.Printf("  proposed  %s  %s\n", e.Claim, p.Paint(ui.Dim, e.ID+" · swim observe accept "+e.ID))
	}
	for _, a := range acc {
		line := fmt.Sprintf("  accepted  %s  %s", a.Claim, p.Paint(ui.Dim, a.ID+" · by "+a.AcceptedBy))
		if a.Retired != "" {
			line += p.Paint(ui.Yellow, "  retired: "+a.Retired+" (remove it from "+rel(root, observe.KnowledgePath(root))+")")
		}
		fmt.Println(line)
	}
	return nil
}

// observeDecide is swim observe accept ID [--note TEXT] (into the
// committed knowledge/steps.yml) and swim observe reject ID (not proposed
// again).
func observeDecide(what string, args []string) error {
	var note string
	rest, err := flags{strs: map[string]*string{"note": &note}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 || what == "reject" && note != "" {
		return usagef("usage: swim observe accept ID [--note TEXT] | swim observe reject ID")
	}
	id := rest[0]
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	runs, err := observe.History(root)
	if err != nil {
		return err
	}
	var entry *observe.Entry
	for _, e := range observe.Propose(runs, cfg.ObserveThresholds().Window) {
		if e.ID == id {
			e := e
			entry = &e
		}
	}
	if entry == nil {
		return fmt.Errorf("no proposed knowledge %s (swim observe knowledge lists them)", id)
	}
	if what == "reject" {
		if err := observe.Reject(root, id); err != nil {
			return err
		}
		fmt.Printf("rejected %s: it won't be proposed again\n", id)
		return nil
	}
	if err := observe.Accept(root, *entry, operator(), note); err != nil {
		return err
	}
	fmt.Printf("accepted %s into %s: %s\ncommit it; swim plan shows it beside swim %d\n", id, rel(root, observe.KnowledgePath(root)), entry.Claim, entry.Lane)
	return nil
}
