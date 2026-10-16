// Package observe computes observations: facts about a run, with the
// numbers behind them and the runs they come from, from what swim already
// records (each round's results, durations and details, and the run
// record). Nothing here decides anything a round does: observations are
// for people and planners to read. The detectors are pure functions of
// their Input; gather.go reads the files.
package observe

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/runrec"
)

// Schema names an observations file.
const Schema = "swim.observations/v1"

// Kinds of observation.
const (
	Timeout = "timeout" // a step hit its time limit
	Retried = "retried" // a step needed retries to pass
	Waited  = "waited"  // a lane waited long, on its dependencies, a lock or a slot
	Slow    = "slow"    // a step took much longer than its usual time
	Fast    = "fast"    // a step took far less than its usual time (skipped work?)
	Failing = "failing" // a step failed in each of its last runs
	Dry     = "dry"     // a guard ran as a dry run in each of its last runs
	Drift   = "drift"   // the same DRIFT came back
	Flaky   = "flaky"   // a target's steps needed retries, or failed, in a share of its runs
)

// An Observation is one fact about a run.
type Observation struct {
	ID    string   `yaml:"id"`
	Kind  string   `yaml:"kind"`
	Lane  int      `yaml:"lane"`
	Job   string   `yaml:"job,omitempty"`
	Step  string   `yaml:"step,omitempty"` // the step's (or guard's) label
	Text  string   `yaml:"text"`           // the fact, with its numbers
	Cites []string `yaml:"cites,omitempty"`
}

// File is a run's observations, as .swim/observations/<run>.yml holds them.
type File struct {
	Schema       string        `yaml:"schema"`
	Run          string        `yaml:"run"`
	Observations []Observation `yaml:"observations"`
}

// Thresholds tune the detectors (swim.yml observe:). Zero values take the
// defaults.
type Thresholds struct {
	SlowFactor    float64 `yaml:"slow_factor,omitempty"`    // over this × p90 (1.5)
	SlowMin       float64 `yaml:"slow_min_s,omitempty"`     // and over the median by this many seconds (30)
	BaselineMin   int     `yaml:"baseline_min,omitempty"`   // earlier runs needed for a baseline (5)
	Window        int     `yaml:"window,omitempty"`         // the runs a baseline and counts look at (20)
	WaitMin       float64 `yaml:"wait_min_s,omitempty"`     // a wait worth saying (120)
	FailingStreak int     `yaml:"failing_streak,omitempty"` // failed in this many runs in a row (3)
	DryStreak     int     `yaml:"dry_streak,omitempty"`     // dry runs in a row (3)
	FlakyShare    float64 `yaml:"flaky_share,omitempty"`    // a target troubled in this share of its runs (0.25)
	FlakyMin      int     `yaml:"flaky_min,omitempty"`      // runs of the target needed to judge (4)
}

func (t Thresholds) withDefaults() Thresholds {
	def := func(v *float64, d float64) {
		if *v <= 0 {
			*v = d
		}
	}
	defi := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&t.SlowFactor, 1.5)
	def(&t.SlowMin, 30)
	defi(&t.BaselineMin, 5)
	defi(&t.Window, 20)
	def(&t.WaitMin, 120)
	defi(&t.FailingStreak, 3)
	defi(&t.DryStreak, 3)
	def(&t.FlakyShare, 0.25)
	defi(&t.FlakyMin, 4)
	return t
}

// A Past round of a lane, for its history.
type Past struct {
	Run   string
	Round *logparse.Round
}

// Input is what a run's observations are computed from.
type Input struct {
	Run     *runrec.Run             // the run record (nil: none, waits aren't judged)
	Rounds  map[int]*logparse.Round // this run's round of each lane
	History map[int][]Past          // each lane's earlier rounds, oldest first
}

// Compute is the run's observations, by lane, each detector in turn.
func Compute(in Input, t Thresholds) []Observation {
	t = t.withDefaults()
	var lanes []int
	for n := range in.Rounds {
		lanes = append(lanes, n)
	}
	sort.Ints(lanes)
	run := ""
	if in.Run != nil {
		run = in.Run.Run
	}
	var out []Observation
	for _, n := range lanes {
		r := in.Rounds[n]
		past := in.History[n]
		if len(past) > t.Window {
			past = past[len(past)-t.Window:]
		}
		add := func(kind, step, text string, cites ...string) {
			out = append(out, Observation{Kind: kind, Lane: n, Job: r.Job, Step: step, Text: text, Cites: cites})
		}
		for _, res := range r.Results {
			if res.Handler != "" {
				continue
			}
			switch {
			case res.Step && res.Kind == logparse.Fail && strings.Contains(res.Detail, "timeout"):
				times := 1
				for _, p := range past {
					if timedOut(p.Round, stepKey(res)) {
						times++
					}
				}
				add(Timeout, res.Label, fmt.Sprintf("%q: %s; %d of the last %d runs", res.Label, res.Detail, times, len(past)+1), run)
			case res.Step && res.Kind == logparse.Pass && attempts(res.Detail) > 1:
				add(Retried, res.Label, fmt.Sprintf("%q passed on attempt %d (%s)", res.Label, attempts(res.Detail), res.Detail), run)
			}
			if res.Step && res.Kind == logparse.Pass {
				if o, ok := pace(res, past, t); ok {
					add(o.Kind, res.Label, o.Text, o.Cites...)
				}
			}
			// A step that timed out is said once, as a timeout (with how often).
			if res.Step && res.Kind == logparse.Fail && !strings.Contains(res.Detail, "timeout") && streak(past, t.FailingStreak-1, func(p *logparse.Round) bool { return failed(p, stepKey(res)) }) {
				add(Failing, res.Label, fmt.Sprintf("%q failed in each of the last %d runs (now: %s)", res.Label, t.FailingStreak, res.Detail), citeRuns(past, t.FailingStreak-1, run)...)
			}
			if res.Kind == logparse.Skip && strings.HasPrefix(res.Detail, "dry run") && streak(past, t.DryStreak-1, func(p *logparse.Round) bool { return dryRun(p, res.Label) }) {
				add(Dry, res.Label, fmt.Sprintf("guard %q ran as a dry run in each of the last %d runs: nobody approved it", res.Label, t.DryStreak), citeRuns(past, t.DryStreak-1, run)...)
			}
			if res.Kind == logparse.Drift {
				seen := 0
				var cites []string
				for _, p := range past {
					if has(p.Round, logparse.Drift, res.Label) {
						seen++
						cites = append(cites, p.Run)
					}
				}
				if seen > 0 {
					add(Drift, res.Label, fmt.Sprintf("DRIFT %q again: %d of the last %d runs before this one", res.Label, seen, len(past)), append(cites, run)...)
				}
			}
		}
		if in.Run != nil {
			for _, l := range in.Run.Lanes {
				if l.Lane == n {
					for _, w := range waits(l, t.WaitMin) {
						add(Waited, "", w, run)
					}
				}
			}
		}
	}
	out = append(out, flaky(in, run, t)...)
	for i := range out {
		out[i].ID = fmt.Sprintf("%s-%d-%d", out[i].Kind, out[i].Lane, i+1)
	}
	return out
}

// flaky: a target (what steps talk to: --target, # Targets:) whose steps
// needed retries or failed in this run, and in at least FlakyShare of the
// runs it appeared in (FlakyMin or more, across lanes, the last Window).
func flaky(in Input, run string, t Thresholds) []Observation {
	type tally struct {
		appeared, troubled map[string]bool
		lanes              []int
	}
	targets := map[string]*tally{}
	note := func(runID string, lane int, r *logparse.Round, now bool) {
		for _, res := range r.Results {
			if !res.Step || res.Target == "" || res.Handler != "" {
				continue
			}
			tl := targets[res.Target]
			if tl == nil {
				tl = &tally{appeared: map[string]bool{}, troubled: map[string]bool{}}
				targets[res.Target] = tl
			}
			tl.appeared[runID] = true
			if res.Kind == logparse.Fail || attempts(res.Detail) > 1 {
				tl.troubled[runID] = true
				if now && !containsInt(tl.lanes, lane) {
					tl.lanes = append(tl.lanes, lane)
				}
			}
		}
	}
	var lanes []int
	for n := range in.Rounds {
		lanes = append(lanes, n)
	}
	sort.Ints(lanes)
	for _, n := range lanes {
		past := in.History[n]
		if len(past) > t.Window {
			past = past[len(past)-t.Window:]
		}
		for _, p := range past {
			note(p.Run, n, p.Round, false)
		}
		note(run, n, in.Rounds[n], true)
	}
	var names []string
	for name := range targets {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []Observation
	for _, name := range names {
		tl := targets[name]
		if !tl.troubled[run] || len(tl.appeared) < t.FlakyMin {
			continue
		}
		share := float64(len(tl.troubled)) / float64(len(tl.appeared))
		if share < t.FlakyShare {
			continue
		}
		var cites []string
		for r := range tl.troubled {
			cites = append(cites, r)
		}
		sort.Strings(cites)
		out = append(out, Observation{Kind: Flaky, Lane: tl.lanes[0], Step: "target " + name,
			Text:  fmt.Sprintf("target %s: its steps needed retries or failed in %d of its last %d runs (swim %s this run)", name, len(tl.troubled), len(tl.appeared), joinInts(tl.lanes)),
			Cites: cites})
	}
	return out
}

func containsInt(ns []int, n int) bool {
	for _, x := range ns {
		if x == n {
			return true
		}
	}
	return false
}

// attempts is the attempt a step's detail says it ended on ("attempt 3/4":
// 3), 1 if none.
func attempts(detail string) int {
	for _, part := range strings.Split(detail, ", ") {
		if v, ok := strings.CutPrefix(part, "attempt "); ok {
			n := 0
			for _, c := range v {
				if c < '0' || c > '9' {
					break
				}
				n = n*10 + int(c-'0')
			}
			if n > 0 {
				return n
			}
		}
	}
	return 1
}

// pace compares a passed step's time with its earlier passing runs: slow
// when over factor × p90 and over the median by SlowMin; fast when under a
// quarter of the median (and the median is over a second).
func pace(res logparse.Result, past []Past, t Thresholds) (Observation, bool) {
	var durs []float64
	for _, p := range past {
		for _, pr := range p.Round.Results {
			if pr.Step && pr.Kind == logparse.Pass && stepKey(pr) == stepKey(res) && pr.Handler == "" {
				durs = append(durs, pr.Dur)
			}
		}
	}
	if len(durs) < t.BaselineMin {
		return Observation{}, false
	}
	sort.Float64s(durs)
	med, p90 := quantile(durs, 0.5), quantile(durs, 0.9)
	switch {
	case res.Dur > t.SlowFactor*p90 && res.Dur > med+t.SlowMin:
		return Observation{Kind: Slow, Text: fmt.Sprintf("%q took %s; usually %s (p90 %s, %d runs)", res.Label, secs(res.Dur), secs(med), secs(p90), len(durs))}, true
	case med > 1 && res.Dur < 0.25*med:
		return Observation{Kind: Fast, Text: fmt.Sprintf("%q took %s; usually %s (%d runs): did it skip its work?", res.Label, secs(res.Dur), secs(med), len(durs))}, true
	}
	return Observation{}, false
}

// quantile is the q quantile of sorted xs (nearest rank).
func quantile(xs []float64, q float64) float64 {
	i := int(q*float64(len(xs)-1) + 0.5)
	return xs[min(i, len(xs)-1)]
}

func secs(s float64) string {
	if s < 60 {
		return fmt.Sprintf("%.1fs", s)
	}
	m := int(s) / 60
	return fmt.Sprintf("%dm%02ds", m, int(s)-60*m)
}

// streak reports whether the last k rounds of past all satisfy ok (k <= 0:
// true; fewer than k rounds: false).
func streak(past []Past, k int, ok func(*logparse.Round) bool) bool {
	if k <= 0 {
		return true
	}
	if len(past) < k {
		return false
	}
	for _, p := range past[len(past)-k:] {
		if !ok(p.Round) {
			return false
		}
	}
	return true
}

func citeRuns(past []Past, k int, run string) []string {
	var out []string
	if k > 0 && len(past) >= k {
		for _, p := range past[len(past)-k:] {
			out = append(out, p.Run)
		}
	}
	return append(out, run)
}

func has(r *logparse.Round, kind, label string) bool {
	for _, res := range r.Results {
		if res.Kind == kind && res.Label == label && res.Handler == "" {
			return true
		}
	}
	return false
}

// stepKey is what a step's history is matched by: its --key, else its
// label.
func stepKey(r logparse.Result) string {
	if r.Key != "" {
		return r.Key
	}
	return r.Label
}

func failed(r *logparse.Round, key string) bool {
	for _, res := range r.Results {
		if res.Step && res.Kind == logparse.Fail && stepKey(res) == key && res.Handler == "" {
			return true
		}
	}
	return false
}

func timedOut(r *logparse.Round, key string) bool {
	for _, res := range r.Results {
		if res.Step && res.Kind == logparse.Fail && stepKey(res) == key && strings.Contains(res.Detail, "timeout") {
			return true
		}
	}
	return false
}

func dryRun(r *logparse.Round, label string) bool {
	for _, res := range r.Results {
		if res.Kind == logparse.Skip && res.Label == label && strings.HasPrefix(res.Detail, "dry run") {
			return true
		}
	}
	return false
}

// waits says how long lane l waited, by cause, where it's over min
// seconds: for the lanes it needs (ready), its locks, a parallel slot.
func waits(l runrec.Lane, min float64) []string {
	var out []string
	at := 0.0
	step := func(t *float64, what string) {
		if t == nil {
			return
		}
		if d := *t - at; d >= min {
			out = append(out, fmt.Sprintf("waited %s %s", secs(d), what))
		}
		at = max(at, *t)
	}
	if len(l.After) > 0 {
		step(l.Ready, fmt.Sprintf("for swim %s", joinInts(l.After)))
	} else if l.Ready != nil {
		at = *l.Ready
	}
	if len(l.Locks) > 0 {
		step(l.Locked, "for its locks ("+strings.Join(l.Locks, ", ")+")")
	}
	step(l.Slot, "for a parallel slot")
	return out
}

func joinInts(ns []int) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = fmt.Sprint(n)
	}
	return strings.Join(parts, ", ")
}
