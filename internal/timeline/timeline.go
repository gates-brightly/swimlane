// Package timeline turns a run record (internal/runrec) into a timeline:
// when each lane waited, ran and finished, how long it started after its
// last parent, and the longest dependency chain against the run's length.
// swim timeline renders it as text, YAML or a self-contained HTML chart.
package timeline

import (
	"math"
	"sort"
	"time"

	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/runrec"
)

// Schema identifies the YAML form.
const Schema = "swim.timeline/v1"

// Step is one step (or, for a staged round, one stage) of a lane, laid out
// back to back from the lane's start by duration.
type Step struct {
	Label string  `yaml:"label"`
	Kind  string  `yaml:"result,omitempty"`
	Stage string  `yaml:"stage,omitempty"`
	Start float64 `yaml:"start"`
	Dur   float64 `yaml:"duration_s"`
}

// Lane is one lane's row. Times are seconds since the run started.
type Lane struct {
	Lane      int      `yaml:"lane"`
	Job       string   `yaml:"job,omitempty"`
	Round     string   `yaml:"round,omitempty"`
	State     string   `yaml:"state"`
	Reason    string   `yaml:"reason,omitempty"`
	Exit      *int     `yaml:"exit,omitempty"`
	After     []int    `yaml:"after,flow"`
	Ready     *float64 `yaml:"ready,omitempty"`
	Start     *float64 `yaml:"start,omitempty"`
	End       *float64 `yaml:"end,omitempty"`
	Duration  float64  `yaml:"duration_s"`
	DepWait   float64  `yaml:"dep_wait_s"`
	LockWait  float64  `yaml:"lock_wait_s"`
	QueueWait float64  `yaml:"queue_wait_s"`
	Delay     *float64 `yaml:"start_delay_s,omitempty"` // start after the last parent finished
	OnChain   bool     `yaml:"longest_chain,omitempty"`
	Steps     []Step   `yaml:"steps,omitempty"`

	locked, slot *float64
}

// Ran reports whether the lane's process started.
func (l Lane) Ran() bool { return l.Start != nil && l.End != nil }

// Delays summarises start delays after the last parent.
type Delays struct {
	Count  int     `yaml:"count"`
	Median float64 `yaml:"median_s"`
	P95    float64 `yaml:"p95_s"`
	Max    float64 `yaml:"max_s"`
	MaxOf  int     `yaml:"max_lane,omitempty"`
}

// Share is the fraction of lane-time (each lane's span from the run's
// start to its end) spent waiting.
type Share struct {
	Deps   float64 `yaml:"deps"`
	Queued float64 `yaml:"queued"`
	Locks  float64 `yaml:"locks"`
}

// Timeline is one run.
type Timeline struct {
	Schema   string    `yaml:"schema"`
	Run      string    `yaml:"run"`
	Started  time.Time `yaml:"started"`
	Total    float64   `yaml:"total_s"`    // the run's wall time
	Makespan float64   `yaml:"makespan_s"` // first lane start to last lane end
	Chain    []int     `yaml:"longest_chain,flow"`
	ChainS   float64   `yaml:"longest_chain_s"`
	Overhead float64   `yaml:"overhead_s"` // total minus the longest chain
	Delays   Delays    `yaml:"start_delay"`
	Waiting  Share     `yaml:"waiting"`
	Lanes    []Lane    `yaml:"lanes"`
}

func r3(f float64) float64 { return math.Round(f*1000) / 1000 }

func val(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// Build computes a run's timeline. rounds, if not nil, returns a lane's
// parsed round in this run, for steps.
func Build(rec *runrec.Run, rounds func(lane int) *logparse.Round) *Timeline {
	t := &Timeline{Schema: Schema, Run: rec.Run, Started: rec.Started, Total: r3(rec.Finished.Sub(rec.Started).Seconds())}
	byLane := map[int]*Lane{}
	for _, rl := range rec.Lanes {
		l := Lane{Lane: rl.Lane, Job: rl.Job, Round: rl.Round, State: rl.State, Reason: rl.Reason, Exit: rl.Exit,
			After: rl.After, Ready: rl.Ready, Start: rl.Start, End: rl.End, locked: rl.Locked, slot: rl.Slot}
		if l.After == nil {
			l.After = []int{}
		}
		t.Lanes = append(t.Lanes, l)
	}
	sort.Slice(t.Lanes, func(a, b int) bool { return t.Lanes[a].Lane < t.Lanes[b].Lane })
	for i := range t.Lanes {
		byLane[t.Lanes[i].Lane] = &t.Lanes[i]
	}

	first, last, any := 0.0, 0.0, false
	var delays []float64
	var laneTime, depTime, lockTime, queueTime float64
	for i := range t.Lanes {
		l := &t.Lanes[i]
		if l.Ready != nil && len(l.After) > 0 {
			l.DepWait = r3(*l.Ready)
		}
		if l.Ready != nil && l.locked != nil {
			l.LockWait = r3(*l.locked - *l.Ready)
		}
		if l.slot != nil {
			from := val(l.Ready)
			if l.locked != nil {
				from = *l.locked
			}
			l.QueueWait = r3(math.Max(0, *l.slot-from))
		}
		if l.End != nil {
			laneTime += *l.End
			depTime += l.DepWait
			lockTime += l.LockWait
			queueTime += l.QueueWait
		}
		if !l.Ran() {
			continue
		}
		l.Duration = r3(*l.End - *l.Start)
		if !any || *l.Start < first {
			first = *l.Start
		}
		if !any || *l.End > last {
			last = *l.End
		}
		any = true
		parentEnd, parents := 0.0, false
		for _, p := range l.After {
			if pl := byLane[p]; pl != nil && pl.End != nil {
				parentEnd, parents = math.Max(parentEnd, *pl.End), true
			}
		}
		if parents {
			d := r3(*l.Start - parentEnd)
			l.Delay = &d
			delays = append(delays, d)
			if d > t.Delays.Max || t.Delays.Count == 0 {
				t.Delays.Max, t.Delays.MaxOf = d, l.Lane
			}
			t.Delays.Count++
		}
		if rounds != nil {
			if r := rounds(l.Lane); r != nil {
				l.Steps = stepsOf(r, *l.Start)
			}
		}
	}
	if any {
		t.Makespan = r3(last - first)
	}
	if len(delays) > 0 {
		sort.Float64s(delays)
		t.Delays.Median = r3(quantile(delays, .5))
		t.Delays.P95 = r3(quantile(delays, .95))
	}
	if laneTime > 0 {
		t.Waiting = Share{Deps: r3(depTime / laneTime), Queued: r3(queueTime / laneTime), Locks: r3(lockTime / laneTime)}
	}
	t.Chain, t.ChainS = longestChain(t.Lanes, byLane)
	for _, n := range t.Chain {
		byLane[n].OnChain = true
	}
	t.Overhead = r3(math.Max(0, t.Total-t.ChainS))
	return t
}

// quantile is the nearest-rank quantile of sorted xs.
func quantile(xs []float64, q float64) float64 {
	i := int(math.Ceil(q*float64(len(xs)))) - 1
	if i < 0 {
		i = 0
	}
	return xs[i]
}

// longestChain is the dependency path with the most running time: each
// lane's own duration plus the longest chain of the parents it waited for.
func longestChain(lanes []Lane, byLane map[int]*Lane) ([]int, float64) {
	best := map[int]float64{}
	prev := map[int]int{}
	var walk func(n int, seen map[int]bool) float64
	walk = func(n int, seen map[int]bool) float64 {
		if v, ok := best[n]; ok {
			return v
		}
		l := byLane[n]
		if l == nil || !l.Ran() || seen[n] {
			return 0
		}
		seen[n] = true
		top, from := 0.0, 0
		for _, p := range l.After {
			if v := walk(p, seen); v > top || (v == top && v > 0 && p < from) {
				top, from = v, p
			}
		}
		delete(seen, n)
		best[n] = l.Duration + top
		if from > 0 {
			prev[n] = from
		}
		return best[n]
	}
	end, top := 0, 0.0
	for _, l := range lanes {
		if v := walk(l.Lane, map[int]bool{}); v > top {
			end, top = l.Lane, v
		}
	}
	if end == 0 {
		return []int{}, 0
	}
	var chain []int
	for n := end; n != 0; n = prev[n] {
		chain = append([]int{n}, chain...)
	}
	return chain, r3(top)
}

// stepsOf lays a round's steps out back to back from the lane's start. A
// round that declared stages gets one entry per stage instead.
func stepsOf(r *logparse.Round, start float64) []Step {
	var out []Step
	at := start
	staged := len(r.Declared) > 0
	for _, res := range r.Results {
		if !res.Step {
			continue
		}
		if staged && len(out) > 0 && out[len(out)-1].Stage == res.Stage {
			s := &out[len(out)-1]
			s.Dur = r3(s.Dur + res.Dur)
			if worse(res.Kind, s.Kind) {
				s.Kind = res.Kind
			}
			at += res.Dur
			continue
		}
		s := Step{Label: res.Label, Kind: res.Kind, Stage: res.Stage, Start: r3(at), Dur: r3(res.Dur)}
		if staged {
			s.Label = "stage " + res.Stage
		}
		out = append(out, s)
		at += res.Dur
	}
	return out
}

func worse(a, b string) bool {
	rank := map[string]int{logparse.Pass: 0, logparse.Approved: 0, logparse.Fail: 2, logparse.Blocked: 2}
	return rank[a] > rank[b]
}

// Entry is one lane's round in one run, for a lane's or job's history.
type Entry struct {
	Run     string    `yaml:"run"`
	Started time.Time `yaml:"started"`
	Lane    Lane      `yaml:"lane"`
}

// History is a lane's or job's rounds across runs, oldest first.
type History struct {
	Schema  string  `yaml:"schema"`
	Of      string  `yaml:"of"`
	Entries []Entry `yaml:"rounds"`
}

// HistoryOf collects the rounds that match across runs.
func HistoryOf(of string, runs []*runrec.Run, match func(runrec.Lane) bool) *History {
	h := &History{Schema: "swim.timeline-history/v1", Of: of}
	for _, rec := range runs {
		t := Build(rec, nil)
		for _, l := range t.Lanes {
			for _, rl := range rec.Lanes {
				if rl.Lane == l.Lane && match(rl) {
					h.Entries = append(h.Entries, Entry{Run: rec.Run, Started: rec.Started, Lane: l})
				}
			}
		}
	}
	return h
}
