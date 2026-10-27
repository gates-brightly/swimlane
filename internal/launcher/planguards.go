package launcher

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/authority"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/manifest"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// GuardVerdict is how swim run would decide one guard, as things stand
// now: by the same snapshot of flags, signed approvals and lookup.yml the
// run takes as it starts (authority.Snapshot).
type GuardVerdict struct {
	Flag string
	Desc string
	// Approved: nothing needs asking (its flag, signed approvals, -y).
	Approved bool
	// How it's approved ("FLAG=1", "approved by zach, alice (signed)"),
	// or why the signed approvals don't count yet ("" if there are none).
	How, Why string
	// Asks: not approved, and swim run would ask at its prompt (a dry run
	// when no one can be asked). False: a dry run outright.
	Asks bool
}

// planGuards decides each guard of each selected lane the way swim run
// would as it starts. A plan run outside a run has no evidence of its own,
// so an approval bound to a run's evidence doesn't count here (Why says so).
func planGuards(o Options, sel []int, scripts map[int]lane.Info) map[int][]GuardVerdict {
	snap := authority.Take(o.Root, os.Environ())
	asks := o.Cfg.ApprovalMode() != config.ApprovalsFlags
	now := time.Now()
	out := map[int][]GuardVerdict{}
	for _, n := range sel {
		info := scripts[n]
		if len(info.Guards) == 0 {
			continue
		}
		m, _ := manifest.Build(o.Root, o.Cfg, n, "")
		for _, g := range info.Guards {
			v := GuardVerdict{Flag: g.Flag, Desc: g.Desc}
			switch {
			case snap.Env[g.Flag] == "1":
				v.Approved, v.How = true, snap.FlagSet(g.Flag).How
			case o.AutoApprove:
				v.Approved, v.How = true, "swim run -y"
			default:
				ask := authority.Ask{Root: o.Root, Lane: n, Flag: g.Flag, Desc: g.Desc, Job: info.Job, Manifest: m}
				if m != nil {
					ask.Script = m.ScriptSHA
				}
				if r := snap.Signed(ask, now); r.OK {
					v.Approved, v.How = true, r.How
				} else {
					v.Why, v.Asks = r.Why, asks
				}
			}
			out[n] = append(out[n], v)
		}
	}
	return out
}

// awaitingApproval is, for each lane of the run, the lane whose guard
// isn't approved yet that it depends on (itself, or one it sits under):
// if that guard dry-runs, the round is skipped, not passed, and everything
// under it is skipped too. 0: nothing it waits on needs an approval.
func awaitingApproval(sel []int, inRunDeps map[int][]int, guards map[int][]GuardVerdict) map[int]int {
	needs := map[int]bool{}
	for n, vs := range guards {
		for _, v := range vs {
			if !v.Approved {
				needs[n] = true
			}
		}
	}
	out := map[int]int{}
	var find func(n int, seen map[int]bool) int
	find = func(n int, seen map[int]bool) int {
		if needs[n] {
			return n
		}
		if seen[n] {
			return 0
		}
		seen[n] = true
		for _, d := range inRunDeps[n] {
			if w := find(d, seen); w != 0 {
				return w
			}
		}
		return 0
	}
	for _, n := range sel {
		if w := find(n, map[int]bool{}); w != 0 {
			out[n] = w
		}
	}
	return out
}

// text is the guard's state in swim plan: approved and how, or what it
// needs.
func (v GuardVerdict) text(p ui.Painter) string {
	if v.Approved {
		if strings.HasPrefix(v.How, "approved") {
			return p.Paint(ui.Green, v.How) // "approved by alice, zach (signed)"
		}
		return p.Paint(ui.Green, "approved: "+v.How)
	}
	s := "not approved: a dry run (approvals: flags never ask)"
	if v.Asks {
		s = "not approved: asks at the prompt; a dry run if no one can answer"
	}
	if v.Why != "" {
		s += "; " + v.Why
	}
	return p.Paint(ui.Yellow, s)
}

func (v GuardVerdict) desc(p ui.Painter) string {
	if v.Desc == "" {
		return ""
	}
	return "  " + p.Paint(ui.Dim, v.Desc)
}

// approvalsLine sums up the run's guards for swim plan: how many are
// approved, and what a dry run of the rest would skip. "" for no guards.
func approvalsLine(pl *plan) string {
	total, ok := 0, 0
	var need []string
	for _, n := range pl.sel {
		if pl.actions[n] == actSkip {
			continue
		}
		for _, v := range pl.guards[n] {
			total++
			if v.Approved {
				ok++
			} else {
				need = append(need, fmt.Sprintf("swim %d %s", n, v.Flag))
			}
		}
	}
	if total == 0 {
		return ""
	}
	if len(need) == 0 {
		if total == 1 {
			return "the guard is approved."
		}
		return fmt.Sprintf("all %d guards are approved.", total)
	}
	var skipped []string
	for _, n := range pl.sel {
		if w := pl.awaiting[n]; w != 0 && w != n {
			skipped = append(skipped, fmt.Sprint(n))
		}
	}
	line := fmt.Sprintf("%d of %d guards approved. Not yet: %s (set the flag, swim approve, or answer the prompt).", ok, total, strings.Join(need, ", "))
	if len(skipped) > 0 {
		line += fmt.Sprintf(" A dry run there skips swim %s too.", strings.Join(skipped, ", "))
	}
	return line
}
