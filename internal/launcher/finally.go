package launcher

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/status"
)

// Finally lanes (# Finally: 1 2) are the run's after_script, as Always
// lanes are its before_script:
//
//   - a finally lane starts once the lanes it names have ended, in whatever
//     state (passed, failed, skipped, interrupted), where After: needs them
//     to pass; it's never skipped for them, and one not in the run has
//     ended;
//   - naming one of those lanes in a run pulls the finally lane in
//     (expandFinally; --no-finally leaves it out);
//   - it sees how each ended (SWIM_FINALLY_<N>, SWIM_FINALLY_<N>_JOB) and
//     whether all passed (SWIM_FINALLY);
//   - the first Ctrl-C doesn't stop it, and it still starts, within
//     finally_timeout (interrupt.go);
//   - its result never changes another lane's: the run reports it on its
//     own line, and exits 89 when an ordinary lane and a finally lane both
//     failed.

// finallyCtx holds what each finally lane saw of the lanes it waited for.
type finallyCtx struct {
	mu  sync.Mutex
	env map[int][]string
	saw map[int]string // "1 pass, 2 fail, 3 skip"
}

func (f *finallyCtx) set(n int, env []string, saw string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.env[n], f.saw[n] = env, saw
}

func (f *finallyCtx) envOf(n int) []string {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.env[n]
}

// finallyWord is how SWIM_FINALLY_<N> says a lane ended.
func finallyWord(state string) string {
	switch state {
	case status.Passed:
		return "pass"
	case status.Failed:
		return "fail"
	case status.Skipped:
		return "skip"
	case status.Interrupted:
		return "interrupted"
	}
	return state
}

// finallyEnv is a finally lane's context from the outcomes of the lanes it
// waited for (outside the run: nil, its variable empty).
func finallyEnv(saw map[int]*Outcome, order []int) (env []string, summary string) {
	all := true
	var parts []string
	for _, d := range order {
		oc := saw[d]
		word, job := "", ""
		if oc != nil {
			word = finallyWord(oc.State)
			if oc.State != status.Skipped || oc.Exit != -1 {
				job = oc.Job
			}
			parts = append(parts, fmt.Sprintf("%d %s", d, word))
		} else {
			parts = append(parts, fmt.Sprintf("%d not in this run", d))
		}
		if word != "pass" {
			all = false
		}
		env = append(env, fmt.Sprintf("SWIM_FINALLY_%d=%s", d, word), fmt.Sprintf("SWIM_FINALLY_%d_JOB=%s", d, job))
	}
	if all {
		env = append(env, "SWIM_FINALLY=pass")
	} else {
		env = append(env, "SWIM_FINALLY=fail")
	}
	return env, strings.Join(parts, ", ")
}

// finallyRefs are the lanes each lane's # Finally: names (resolved like
// After:; refs that don't resolve are left to ResolveDeps to report).
func finallyRefs(o Options, scripts map[int]lane.Info) map[int][]int {
	st, _ := status.Load(o.Root)
	if st == nil {
		st = &status.File{}
	}
	st.Normalize(o.Cfg.Lanes)
	out := map[int][]int{}
	for n := 1; n <= o.Cfg.Lanes; n++ {
		for _, ref := range scripts[n].Finally {
			if d, err := resolveAfter(o.Cfg, scripts, st, n, ref); err == nil {
				out[n] = append(out[n], d.Lane)
			}
		}
	}
	return out
}

// expandFinally adds to a run of named lanes the pending finally lanes
// that name one of them (and the finally lanes those pull in), unless
// --no-finally. swim all runs pending finally lanes like any others.
func expandFinally(o Options, sel []int) []int {
	if len(o.Lanes) == 0 || o.NoFinally {
		return sel
	}
	scripts := map[int]lane.Info{}
	any := false
	for n := 1; n <= o.Cfg.Lanes; n++ {
		scripts[n], _ = lane.ReadScript(o.Root, n)
		any = any || len(scripts[n].Finally) > 0
	}
	if !any {
		return sel
	}
	refs := finallyRefs(o, scripts)
	in := map[int]bool{}
	for _, n := range sel {
		in[n] = true
	}
	for changed := true; changed; {
		changed = false
		for n := 1; n <= o.Cfg.Lanes; n++ {
			if in[n] || !scripts[n].Pending() || scripts[n].Always {
				continue
			}
			for _, d := range refs[n] {
				if in[d] {
					in[n], changed = true, true
					sel = append(sel, n)
					break
				}
			}
		}
	}
	sort.Ints(sel)
	return sel
}

// finallyLanes are the run's finally lanes (any # Finally:).
func finallyLanes(o Options, sel []int) map[int]bool {
	out := map[int]bool{}
	for _, n := range sel {
		if info, _ := lane.ReadScript(o.Root, n); len(info.Finally) > 0 {
			out[n] = true
		}
	}
	return out
}

// finallyCounts says how a run's finally lanes ended: "1 passed, 1 failed".
func finallyCounts(fin map[int]bool, outcomes map[int]*Outcome) (text string, passed, failed int) {
	var ns []int
	for n := range fin {
		ns = append(ns, n)
	}
	sort.Ints(ns)
	for _, n := range ns {
		if oc := outcomes[n]; oc != nil {
			if oc.State == status.Passed {
				passed++
			} else if oc.State == status.Failed || oc.State == status.Interrupted {
				failed++
			}
		}
	}
	var parts []string
	if passed > 0 {
		parts = append(parts, strconv.Itoa(passed)+" passed")
	}
	if failed > 0 {
		parts = append(parts, strconv.Itoa(failed)+" failed")
	}
	if len(parts) == 0 {
		parts = append(parts, "none ran")
	}
	return strings.Join(parts, ", "), passed, failed
}

// WithFinally is lanes and the finally lanes they pull in (a queued batch
// pins them with it), and which those are.
func WithFinally(root string, cfg *config.Config, lanes []int) (all, added []int) {
	all = expandFinally(Options{Root: root, Cfg: cfg, Lanes: lanes}, append([]int(nil), lanes...))
	in := map[int]bool{}
	for _, n := range lanes {
		in[n] = true
	}
	for _, n := range all {
		if !in[n] {
			added = append(added, n)
		}
	}
	return all, added
}
