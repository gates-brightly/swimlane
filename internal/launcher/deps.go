package launcher

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/status"
)

// Dep is one thing a lane waits for.
type Dep struct {
	Lane int
	Job  string // set when pinned by job id in `# After:`: that exact job must pass
}

func (d Dep) String() string {
	if d.Job != "" {
		return fmt.Sprintf("swim %d (job %s)", d.Lane, lane.ShortJob(d.Job))
	}
	return fmt.Sprintf("swim %d", d.Lane)
}

// ResolveDeps returns each selected lane's dependencies: the config deps
// table plus the `# After:` line of its script (lane numbers or job ids).
// It fails, before anything runs, on a reference that doesn't resolve or a
// cycle among the selected lanes.
func ResolveDeps(root string, cfg *config.Config, sel []int) (map[int][]Dep, error) {
	st, err := status.Load(root)
	if err != nil {
		return nil, err
	}
	st.Normalize(cfg.Lanes)
	scripts := map[int]lane.Info{}
	for n := 1; n <= cfg.Lanes; n++ {
		if scripts[n], err = lane.ReadScript(root, n); err != nil {
			return nil, err
		}
	}

	out := map[int][]Dep{}
	for _, n := range sel {
		byLane := map[int]Dep{}
		for _, d := range cfg.DepsOf(n) {
			byLane[d] = Dep{Lane: d}
		}
		for _, ref := range scripts[n].After {
			d, err := resolveAfter(cfg, scripts, st, n, ref)
			if err != nil {
				return nil, fmt.Errorf("lane.%d.sh `# After: %s`: %w", n, ref, err)
			}
			if prev, ok := byLane[d.Lane]; !ok || prev.Job == "" {
				byLane[d.Lane] = d
			}
		}
		var deps []Dep
		for _, d := range byLane {
			deps = append(deps, d)
		}
		sort.Slice(deps, func(a, b int) bool { return deps[a].Lane < deps[b].Lane })
		out[n] = deps
	}
	if cyc := selectedCycle(out); cyc != "" {
		return nil, fmt.Errorf("dependency cycle among the lanes being run: %s", cyc)
	}
	return out, nil
}

func resolveAfter(cfg *config.Config, scripts map[int]lane.Info, st *status.File, n int, ref string) (Dep, error) {
	if m, err := strconv.Atoi(ref); err == nil {
		if !cfg.ValidLane(m) {
			return Dep{}, fmt.Errorf("no swim %d (lanes are 1..%d)", m, cfg.Lanes)
		}
		if m == n {
			return Dep{}, fmt.Errorf("a lane can't wait for itself")
		}
		return Dep{Lane: m}, nil
	}
	var hits []Dep
	for m := 1; m <= cfg.Lanes; m++ {
		if info := scripts[m]; info.Pending() && lane.MatchJob(info.Job, ref) {
			hits = append(hits, Dep{Lane: m, Job: info.Job})
		}
	}
	if len(hits) == 0 {
		// Not in any script any more: it may have run already.
		for m := 1; m <= cfg.Lanes; m++ {
			if l := st.Get(m); l != nil && lane.MatchJob(l.Job, ref) {
				hits = append(hits, Dep{Lane: m, Job: l.Job})
			}
		}
	}
	switch {
	case len(hits) == 0:
		return Dep{}, fmt.Errorf("no lane holds or last ran job %s", ref)
	case len(hits) > 1:
		return Dep{}, fmt.Errorf("job %s is ambiguous; use more characters", ref)
	case hits[0].Lane == n:
		return Dep{}, fmt.Errorf("a lane can't wait for itself")
	}
	return hits[0], nil
}

// selectedCycle finds a cycle among lanes that wait on each other in this
// run (dependencies outside the run never block, so they can't deadlock).
func selectedCycle(deps map[int][]Dep) string {
	const (
		unseen = iota
		active
		done
	)
	state := map[int]int{}
	var stack []int
	var visit func(n int) string
	visit = func(n int) string {
		state[n] = active
		stack = append(stack, n)
		for _, d := range deps[n] {
			if _, inRun := deps[d.Lane]; !inRun {
				continue
			}
			switch state[d.Lane] {
			case active:
				var parts []string
				for i, s := range stack {
					if s == d.Lane {
						for _, x := range append(append([]int(nil), stack[i:]...), d.Lane) {
							parts = append(parts, fmt.Sprintf("swim %d", x))
						}
						return strings.Join(parts, " -> ")
					}
				}
			case unseen:
				if c := visit(d.Lane); c != "" {
					return c
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[n] = done
		return ""
	}
	var lanes []int
	for n := range deps {
		lanes = append(lanes, n)
	}
	sort.Ints(lanes)
	for _, n := range lanes {
		if state[n] == unseen {
			if c := visit(n); c != "" {
				return c
			}
		}
	}
	return ""
}

// outsideBlocker checks a dependency that is not part of this run. It
// returns "" when satisfied, or why the waiting lane must be skipped:
//   - pinned to a job: that job must be the dependency lane's last run, passed;
//   - otherwise: if the dependency lane holds a pending round, that round
//     must have passed already (a stubbed or empty lane never blocks).
func outsideBlocker(root string, d Dep) string {
	st, _ := status.Load(root)
	var last status.Lane
	if st != nil {
		if l := st.Get(d.Lane); l != nil {
			last = *l
		}
	}
	if d.Job != "" {
		if last.Job == d.Job && last.State == status.Passed {
			return ""
		}
		return fmt.Sprintf("%s has not passed; run it too: swim run %d ...", d, d.Lane)
	}
	info, _ := lane.ReadScript(root, d.Lane)
	if !info.Pending() || AlreadyPassed(info, &last) {
		return ""
	}
	return fmt.Sprintf("swim %d's pending round has not passed; run it too: swim run %d ...", d.Lane, d.Lane)
}

// AlreadyPassed reports whether the round in a lane's script is the one the
// lane last ran, and that run passed. Rounds are matched by job id, or by
// Round: line for scripts without a Job: line.
func AlreadyPassed(info lane.Info, last *status.Lane) bool {
	if last == nil || last.State != status.Passed || !info.Pending() {
		return false
	}
	if info.Job != "" {
		return last.Job == info.Job
	}
	return last.Round == info.Round
}
