package observe

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/fsutil"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/runrec"
)

// Dir is where runs' observations are kept.
func Dir(root string) string { return filepath.Join(root, ".swim", "observations") }

// Path is run's observations file.
func Path(root, run string) string { return filepath.Join(Dir(root), run+".yml") }

// Gather reads what run's observations are computed from: its run record,
// each of its lanes' round in it, and their earlier rounds (the lane's log
// and its archives). Matrix cells and jobs aren't read yet.
func Gather(root, run string) (Input, error) {
	rec, ok, err := runrec.Load(root, run)
	if err != nil {
		return Input{}, err
	}
	if !ok {
		return Input{}, fmt.Errorf("no run %s (swim timeline lists runs)", run)
	}
	in := Input{Run: rec, Rounds: map[int]*logparse.Round{}, History: map[int][]Past{}}
	for _, l := range rec.Lanes {
		var all []*logparse.Round
		files, _ := filepath.Glob(filepath.Join(lane.LogDir(root), fmt.Sprintf("agent%d.prev-*.log", l.Lane)))
		sort.Strings(files)
		for _, f := range append(files, lane.Log(root, l.Lane)) {
			rs, err := logparse.RoundsInFile(f)
			if err != nil {
				return in, err
			}
			all = append(all, rs...)
		}
		sort.SliceStable(all, func(a, b int) bool { return all[a].StartedAt < all[b].StartedAt })
		for _, r := range all {
			switch {
			case r.Run == run:
				in.Rounds[l.Lane] = r
			case in.Rounds[l.Lane] == nil && r.Run != "":
				in.History[l.Lane] = append(in.History[l.Lane], Past{Run: r.Run, Round: r})
			}
		}
	}
	return in, nil
}

// Record computes run's observations and writes them to its file.
func Record(root, run string, t Thresholds) (*File, error) {
	in, err := Gather(root, run)
	if err != nil {
		return nil, err
	}
	f := &File{Schema: Schema, Run: run, Observations: Compute(in, t)}
	if f.Observations == nil {
		f.Observations = []Observation{}
	}
	data, err := yaml.Marshal(f)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(Dir(root), 0o755); err != nil {
		return nil, err
	}
	return f, fsutil.WriteAtomic(Path(root, run), data, 0o644)
}

// Load reads run's observations ("ok" false: none were recorded).
func Load(root, run string) (*File, bool, error) {
	data, err := os.ReadFile(Path(root, run))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, false, fmt.Errorf("%s: %w", Path(root, run), err)
	}
	return &f, true, nil
}
