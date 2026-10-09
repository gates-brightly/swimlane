package launcher

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// Beyond returns lane scripts numbered above the configured lanes that hold
// jobs which would run if the lanes existed (not already passed, unless
// rerun).
func Beyond(root string, cfg *config.Config, rerun bool) []int {
	st, _ := status.Load(root)
	var out []int
	for _, n := range lane.ScriptsBeyond(root, cfg.Lanes) {
		info, err := lane.ReadScript(root, n)
		if err != nil || !info.Pending() {
			continue
		}
		var last *status.Lane
		if st != nil {
			last = st.Get(n)
		}
		if !rerun && AlreadyPassed(info, last) {
			continue
		}
		out = append(out, n)
	}
	return out
}

// WritePlan prints what `swim run` with these options would do, without
// running anything: a tree of the lanes that will run (each under the
// lanes it waits for), predicted skips, guard flags, and what won't run.
func WritePlan(w io.Writer, o Options, color bool) error {
	p := ui.Painter{On: color}
	o.DryRun = true
	sel, passed, err := Select(o)
	if err != nil {
		return err
	}
	deps, err := ResolveDeps(o.Root, o.Cfg, sel)
	if err != nil {
		return err
	}
	selected := map[int]bool{}
	for _, n := range sel {
		selected[n] = true
	}
	scripts := map[int]lane.Info{}
	for n := 1; n <= o.Cfg.Lanes; n++ {
		scripts[n], _ = lane.ReadScript(o.Root, n)
	}

	// Edges inside the run, and lanes predicted to be skipped.
	children := map[int][]int{}
	inRunDeps := map[int][]int{}
	blocked := map[int]string{}
	for _, n := range sel {
		for _, d := range deps[n] {
			if selected[d.Lane] {
				children[d.Lane] = append(children[d.Lane], n)
				inRunDeps[n] = append(inRunDeps[n], d.Lane)
			} else if why := outsideBlocker(o.Root, d); why != "" && blocked[n] == "" {
				blocked[n] = why
			}
		}
	}
	skip := map[int]string{}
	var skipReason func(n int) string
	skipReason = func(n int) string {
		if r, ok := skip[n]; ok {
			return r
		}
		skip[n] = "" // guards against revisiting; cycles were rejected already
		r := blocked[n]
		if r == "" {
			for _, d := range inRunDeps[n] {
				if skipReason(d) != "" {
					r = fmt.Sprintf("swim %d won't run", d)
					break
				}
			}
		}
		skip[n] = r
		return r
	}
	for _, n := range sel {
		skipReason(n)
	}

	what := "swim all"
	if len(o.Lanes) > 0 {
		parts := make([]string, len(o.Lanes))
		for i, n := range o.Lanes {
			parts[i] = fmt.Sprint(n)
		}
		what = "swim run " + strings.Join(parts, " ")
	} else if o.Rerun {
		what = "swim all --rerun"
	}

	// Each lane in the run gets a Terraform-style action.
	st, _ := status.Load(o.Root)
	actions := map[int]planAction{}
	counts := map[planAction]int{}
	for _, n := range sel {
		var last *status.Lane
		if st != nil {
			last = st.Get(n)
		}
		a := actionFor(scripts[n], last, skip[n] != "")
		actions[n] = a
		counts[a]++
	}

	fmt.Fprintln(w, p.Paint(ui.Bold, fmt.Sprintf("swim plan · %s · %s · lanes 1..%d", filepath.Base(o.Root), what, o.Cfg.Lanes)))
	fmt.Fprintln(w)
	if len(sel) > 0 {
		fmt.Fprintln(w, "Job actions are indicated with the following symbols:")
		for _, a := range []planAction{actRun, actRetry, actRerun, actSkip} {
			if counts[a] > 0 {
				fmt.Fprintf(w, "  %s %s\n", a.symbol(p), p.Paint(ui.Dim, a.legend()))
			}
		}
		fmt.Fprintln(w)
		fmt.Fprintf(w, "Swim will perform the following actions (%s).\n", p.Paint(ui.Dim, "top-level lanes start at once; each lane starts when the lanes it sits under pass"))
		fmt.Fprintln(w)
	}

	labelW := len(fmt.Sprintf("swim %d", o.Cfg.Lanes))
	shown := map[int]bool{}
	var walk func(n int, prefix, branch string)
	walk = func(n int, prefix, branch string) {
		info := scripts[n]
		label := p.Paint(ui.LaneColor(n)+ui.Bold, fmt.Sprintf("%-*s", labelW, fmt.Sprintf("swim %d", n)))
		sym := actions[n].symbol(p)
		if shown[n] {
			fmt.Fprintf(w, "%s%s%s %s %s\n", prefix, branch, sym, label, p.Paint(ui.Dim, "(shown above)"))
			return
		}
		shown[n] = true
		line := prefix + branch + sym + " " + label + "  " + info.Round
		if info.Job != "" {
			line += "  " + p.Paint(ui.Dim, lane.ShortJob(info.Job))
		}
		var notes []string
		if ds := inRunDeps[n]; len(ds) > 1 {
			parts := make([]string, len(ds))
			for i, d := range ds {
				parts[i] = fmt.Sprint(d)
			}
			notes = append(notes, p.Paint(ui.Dim, "wait ["+strings.Join(parts, ",")+"]"))
		}
		if pid, ok := lane.Running(o.Root, n); ok {
			notes = append(notes, p.Paint(ui.Red, fmt.Sprintf("RUNNING NOW (pid %d): swim run would refuse", pid)))
		}
		if r := skip[n]; r != "" {
			notes = append(notes, p.Paint(ui.Red, "skip: "+r))
		}
		if len(notes) > 0 {
			line += "  " + strings.Join(notes, "  ")
		}
		fmt.Fprintln(w, line)

		childPrefix := prefix
		switch branch {
		case "├── ":
			childPrefix += "│   "
		case "└── ":
			childPrefix += "    "
		}
		bar := "│ "
		if len(children[n]) == 0 {
			bar = "  "
		}
		for _, g := range info.Guards {
			state := p.Paint(ui.Yellow, "unset: dry run")
			if os.Getenv(g.Flag) == "1" {
				state = p.Paint(ui.Green, "set: approved")
			}
			desc := ""
			if g.Desc != "" {
				desc = "  " + p.Paint(ui.Dim, g.Desc)
			}
			fmt.Fprintf(w, "%s%s%s %s=1 (%s)%s\n", childPrefix, p.Paint(ui.Dim, bar), p.Paint(ui.Dim, "guard"), g.Flag, state, desc)
		}
		if info.Timeout > 0 {
			fmt.Fprintf(w, "%s%s%s\n", childPrefix, p.Paint(ui.Dim, bar), p.Paint(ui.Dim, "timeout "+info.TimeoutText))
		}
		kids := children[n]
		sort.Ints(kids)
		for i, c := range kids {
			b := "├── "
			if i == len(kids)-1 {
				b = "└── "
			}
			walk(c, childPrefix, b)
		}
	}
	for _, n := range sel {
		if len(inRunDeps[n]) == 0 {
			walk(n, "", "")
		}
	}

	// No-action items are summarised, not listed.
	var idle, notes []string
	if len(o.Lanes) == 0 {
		for n := 1; n <= o.Cfg.Lanes; n++ {
			if selected[n] || contains(passed, n) {
				continue
			}
			switch info := scripts[n]; {
			case !info.Exists:
				idle = append(idle, fmt.Sprintf("swim %d (no lane script)", n))
			case info.Stub:
				idle = append(idle, fmt.Sprintf("swim %d (stub)", n))
			default:
				idle = append(idle, fmt.Sprintf("swim %d (no Round: line)", n))
			}
		}
		for _, n := range Beyond(o.Root, o.Cfg, o.Rerun) {
			notes = append(notes, fmt.Sprintf("lane.%d.sh holds a job beyond swim %d (%s): swim all will offer to add lanes, or run swim config --lanes %d",
				n, o.Cfg.Lanes, scripts0(o.Root, n).Round, n))
		}
	}

	if len(sel) > 0 {
		fmt.Fprintln(w)
	}
	if len(sel) == 0 {
		fmt.Fprintln(w, p.Paint(ui.Green+ui.Bold, "No changes.")+" "+completedLine(len(passed)))
	} else {
		fmt.Fprintf(w, "%s %s to run, %s to retry, %s to rerun, %s to skip.\n", p.Paint(ui.Bold, "Plan:"),
			p.Paint(ui.Green, fmt.Sprint(counts[actRun])), p.Paint(ui.Yellow, fmt.Sprint(counts[actRetry])),
			p.Paint(ui.Mag, fmt.Sprint(counts[actRerun])), p.Paint(ui.Red, fmt.Sprint(counts[actSkip])))
		if len(passed) > 0 {
			fmt.Fprintln(w, completedLine(len(passed)))
		}
	}
	if len(idle) > 0 {
		fmt.Fprintln(w, p.Paint(ui.Dim, "Idle lanes: "+strings.Join(idle, ", ")))
	}
	for _, n := range notes {
		fmt.Fprintln(w, p.Paint(ui.Yellow, "Note: ")+n)
	}
	return nil
}

func completedLine(n int) string {
	word := "items have"
	if n == 1 {
		word = "item has"
	}
	if n == 0 {
		return "Nothing pending."
	}
	return fmt.Sprintf("%d %s completed with no remaining work.", n, word)
}

// planAction is what `swim run` would do with one lane, Terraform style.
type planAction int

const (
	actRun   planAction = iota // [+]   new job, never run
	actRetry                   // [~]   ran before and didn't pass
	actRerun                   // [+/-] already passed; runs again
	actSkip                    // [-]   pending but won't run
)

func (a planAction) symbol(p ui.Painter) string {
	switch a {
	case actRetry:
		return p.Paint(ui.Yellow, "[~]")
	case actRerun:
		return p.Paint(ui.Mag, "[+/-]")
	case actSkip:
		return p.Paint(ui.Red, "[-]")
	}
	return p.Paint(ui.Green, "[+]")
}

func (a planAction) legend() string {
	switch a {
	case actRetry:
		return "retry: the job ran before and didn't pass"
	case actRerun:
		return "rerun: the job already passed and runs again"
	case actSkip:
		return "skip: the job won't run (a dependency won't pass)"
	}
	return "run: a new job"
}

// actionFor classifies a lane's pending job against its last run.
func actionFor(info lane.Info, last *status.Lane, skipped bool) planAction {
	if skipped {
		return actSkip
	}
	if AlreadyPassed(info, last) {
		return actRerun
	}
	if last != nil && last.State != status.Idle {
		same := info.Job != "" && last.Job == info.Job
		if info.Job == "" {
			same = last.Round == info.Round
		}
		if same {
			return actRetry
		}
	}
	return actRun
}

func scripts0(root string, n int) lane.Info {
	info, _ := lane.ReadScript(root, n)
	return info
}

func contains(ns []int, n int) bool {
	for _, x := range ns {
		if x == n {
			return true
		}
	}
	return false
}
