package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/timeline"
)

// cmdTimelineCheck reads `swim timeline --yaml` on stdin and checks it
// against the run, and against the audit's own numbers (its output is the
// file argument): every lane started after its parents finished, the longest
// chain is a real dependency path, and the audit's critical-path work (timed
// inside the lanes) fits within swim's chain (timed around the lanes).
func cmdTimelineCheck(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: e2etool timeline-check LANES AUDIT_OUTPUT_FILE < timeline.yml")
	}
	want, _ := strconv.Atoi(args[0])
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	var t timeline.Timeline
	if err := yaml.Unmarshal(data, &t); err != nil {
		return err
	}
	var bad []string
	if t.Schema != timeline.Schema {
		bad = append(bad, "schema "+t.Schema)
	}
	if len(t.Lanes) != want {
		bad = append(bad, fmt.Sprintf("%d lanes in the timeline, want %d", len(t.Lanes), want))
	}
	lanes := map[int]timeline.Lane{}
	for _, l := range t.Lanes {
		lanes[l.Lane] = l
	}
	for _, l := range t.Lanes {
		if !l.Ran() {
			bad = append(bad, fmt.Sprintf("swim %d never ran", l.Lane))
			continue
		}
		for _, p := range l.After {
			if pl := lanes[p]; pl.End != nil && *l.Start < *pl.End {
				bad = append(bad, fmt.Sprintf("swim %d started at %.3f before swim %d finished at %.3f", l.Lane, *l.Start, p, *pl.End))
			}
		}
	}
	sum := 0.0
	for i, n := range t.Chain {
		sum += lanes[n].Duration
		if i == 0 {
			continue
		}
		found := false
		for _, p := range lanes[n].After {
			found = found || p == t.Chain[i-1]
		}
		if !found {
			bad = append(bad, fmt.Sprintf("longest chain: swim %d does not wait on swim %d", n, t.Chain[i-1]))
		}
	}
	if d := sum - t.ChainS; d > 0.01 || d < -0.01 {
		bad = append(bad, fmt.Sprintf("longest chain is %.3fs but its lanes add up to %.3fs", t.ChainS, sum))
	}
	if t.ChainS > t.Total+0.01 {
		bad = append(bad, fmt.Sprintf("longest chain %.2fs is longer than the run %.2fs", t.ChainS, t.Total))
	}
	audit, err := os.ReadFile(args[1])
	if err != nil {
		return err
	}
	if m := regexp.MustCompile(`critical-path work ([0-9.]+)s`).FindSubmatch(audit); m == nil {
		bad = append(bad, "no critical-path line in the audit output")
	} else if crit, _ := strconv.ParseFloat(string(m[1]), 64); crit > t.ChainS+0.05 {
		bad = append(bad, fmt.Sprintf("audit critical path %.2fs is longer than swim's longest chain %.2fs", crit, t.ChainS))
	}
	fmt.Printf("timeline: %d lanes · %.2fs · longest chain %.2fs over %d lanes · start delay median %.2fs max %.2fs (swim %d)\n",
		len(t.Lanes), t.Total, t.ChainS, len(t.Chain), t.Delays.Median, t.Delays.Max, t.Delays.MaxOf)
	if len(bad) > 0 {
		for _, b := range bad {
			fmt.Println("  " + b)
		}
		return exitCode(1)
	}
	return nil
}
