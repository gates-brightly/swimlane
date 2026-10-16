package observe

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/chars"
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
	var lanes []int
	for _, l := range rec.Lanes {
		lanes = append(lanes, l.Lane)
	}
	in, err := GatherLanes(root, run, lanes)
	in.Run = rec
	return in, err
}

// GatherLanes is Gather for some lanes of a run still going (a finally
// lane's upstream lanes, which have ended): their rounds and history, and
// no run record yet, so no waits.
func GatherLanes(root, run string, lanes []int) (Input, error) {
	in := Input{Run: &runrec.Run{Run: run}, Rounds: map[int]*logparse.Round{}, History: map[int][]Past{}}
	for _, n := range lanes {
		var all []*logparse.Round
		files, _ := filepath.Glob(filepath.Join(lane.LogDir(root), fmt.Sprintf("agent%d.prev-*.log", n)))
		sort.Strings(files)
		for _, f := range append(files, lane.Log(root, n)) {
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
				in.Rounds[n] = r
			case in.Rounds[n] == nil && r.Run != "":
				in.History[n] = append(in.History[n], Past{Run: r.Run, Round: r})
			}
		}
	}
	return in, nil
}

// PathFor is the observations a finally lane n of run gets
// (SWIM_OBSERVATIONS): those of the lanes it waited for.
func PathFor(root, run string, n int) string {
	return filepath.Join(Dir(root), fmt.Sprintf("%s.for-%d.yml", run, n))
}

// RecordFor computes the observations of lanes (ended) of run, for finally
// lane n, writes them to PathFor and returns the path.
func RecordFor(root, run string, n int, lanes []int, t Thresholds) (string, error) {
	in, err := GatherLanes(root, run, lanes)
	if err != nil {
		return "", err
	}
	f := &File{Schema: Schema, Run: run, Observations: Compute(in, t)}
	if f.Observations == nil {
		f.Observations = []Observation{}
	}
	return PathFor(root, run, n), write(root, PathFor(root, run, n), f)
}

func write(root, path string, f *File) error {
	data, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(Dir(root), 0o755); err != nil {
		return err
	}
	return fsutil.WriteAtomic(path, data, 0o644)
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
	return f, write(root, Path(root, run), f)
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

// Cited are the observation ids text cites (kind-lane-n: slow-2-3), in
// order, once each.
func Cited(text string) []string {
	kinds := map[string]bool{Timeout: true, Retried: true, Waited: true, Slow: true, Fast: true, Failing: true, Dry: true, Drift: true}
	seen := map[string]bool{}
	var out []string
	for _, w := range strings.FieldsFunc(text, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-')
	}) {
		p := strings.Split(w, "-")
		if len(p) == 3 && kinds[p[0]] && chars.Digits(p[1]) && chars.Digits(p[2]) && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}
