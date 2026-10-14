package launcher

import (
	"sort"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
)

// Always lanes (# Always: yes) are setup work, like a CI before_script:
//
//   - they never count as already passed (AlreadyPassed), so swim all
//     always runs them, and a lane run by name pulls in the Always lanes
//     it names in After: (and the Always lanes those name);
//   - every other lane in the run waits for them to finish, whatever their
//     result (a Setup dependency, see setupDeps); only a lane that names
//     one in After: needs it to pass;
//   - with # Rerun-Children: yes, swim all also reruns every lane
//     downstream of one, at any depth, even if it already passed.
//
// An Always lane may only wait on other Always lanes (ResolveDeps).

// expandAlways adds to a selection the Always lanes the selected lanes
// wait on and, in swim all (no explicit lanes), the passed lanes
// downstream of a Rerun-Children lane, moving those out of done.
func expandAlways(o Options, sel, done []int) ([]int, []int) {
	scripts := map[int]lane.Info{}
	for n := 1; n <= o.Cfg.Lanes; n++ {
		scripts[n], _ = lane.ReadScript(o.Root, n)
	}
	in := map[int]bool{}
	for _, n := range sel {
		in[n] = true
	}
	for queue := append([]int(nil), sel...); len(queue) > 0; queue = queue[1:] {
		for _, d := range laneDeps(o.Root, o.Cfg, queue[0]) {
			if !in[d] && scripts[d].Always && scripts[d].Pending() {
				in[d] = true
				queue = append(queue, d)
			}
		}
	}

	if len(o.Lanes) == 0 && len(done) > 0 {
		children := map[int][]int{}
		for _, n := range append(append([]int(nil), sel...), done...) {
			for _, d := range laneDeps(o.Root, o.Cfg, n) {
				children[d] = append(children[d], n)
			}
		}
		stale := map[int]bool{}
		var queue []int
		for n := range in {
			if scripts[n].Always && scripts[n].RerunChildren {
				queue = append(queue, n)
			}
		}
		for ; len(queue) > 0; queue = queue[1:] {
			for _, c := range children[queue[0]] {
				if !stale[c] {
					stale[c] = true
					queue = append(queue, c)
				}
			}
		}
		var still []int
		for _, n := range done {
			if stale[n] {
				in[n] = true
			} else {
				still = append(still, n)
			}
		}
		done = still
	}

	sel = sel[:0:0]
	for n := range in {
		sel = append(sel, n)
	}
	sort.Ints(sel)
	return sel, done
}

// laneDeps is the lanes lane n waits on (none if they don't resolve).
func laneDeps(root string, cfg *config.Config, n int) []int {
	deps, _ := ResolveDeps(root, cfg, []int{n})
	var out []int
	for _, d := range deps[n] {
		out = append(out, d.Lane)
	}
	return out
}

// rerunBy is, for each selected lane downstream of a Rerun-Children lane,
// the nearest such Always lane (swim plan says why a passed lane reruns).
func rerunBy(root string, cfg *config.Config, sel []int, scripts map[int]lane.Info) map[int]int {
	out := map[int]int{}
	deps := map[int][]int{}
	for _, n := range sel {
		deps[n] = laneDeps(root, cfg, n)
	}
	var find func(n int, seen map[int]bool) int
	find = func(n int, seen map[int]bool) int {
		if seen[n] {
			return 0
		}
		seen[n] = true
		for _, d := range deps[n] {
			if s := scripts[d]; s.Always && s.RerunChildren {
				return d
			}
		}
		for _, d := range deps[n] {
			if a := find(d, seen); a != 0 {
				return a
			}
		}
		return 0
	}
	for _, n := range sel {
		if a := find(n, map[int]bool{}); a != 0 {
			out[n] = a
		}
	}
	return out
}

// setupDeps adds to each non-Always lane of a run a Setup dependency on
// every Always lane in it that the lane doesn't already wait on.
func setupDeps(sel []int, scripts map[int]lane.Info, out map[int][]Dep) {
	var always []int
	for _, n := range sel {
		if scripts[n].Always {
			always = append(always, n)
		}
	}
	for _, n := range sel {
		if scripts[n].Always {
			continue
		}
		has := map[int]bool{}
		for _, d := range out[n] {
			has[d.Lane] = true
		}
		for _, a := range always {
			if !has[a] {
				out[n] = append(out[n], Dep{Lane: a, Setup: true})
			}
		}
		sort.Slice(out[n], func(a, b int) bool { return out[n][a].Lane < out[n][b].Lane })
	}
}
