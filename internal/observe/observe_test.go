package observe

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/runrec"
)

// round is a parsed round of run with these result lines (StepLine and
// MarkLine as swim writes them).
func round(t *testing.T, run string, lines ...string) *logparse.Round {
	t.Helper()
	all := append([]string{logparse.RoundHeader("2026-10-16T12:00:00Z", "job-1", "r", []logparse.KV{{K: "run", V: run}})[1:]}, lines...)
	r, err := logparse.ParseRound(strings.Split(strings.Join(all, "\n"), "\n"))
	if err != nil || !r.Found {
		t.Fatalf("round: %v", err)
	}
	return r
}

func step(kind, label, detail string, dur float64) string {
	return logparse.StepLine(kind, label, detail, fmt.Sprintf("%.1fs", dur), "12:00:00")
}

func kinds(obs []Observation) string {
	var k []string
	for _, o := range obs {
		k = append(k, o.Kind)
	}
	return strings.Join(k, " ")
}

func TestDetectors(t *testing.T) {
	past := func(n int, lines ...string) []Past {
		var out []Past
		for i := 0; i < n; i++ {
			out = append(out, Past{Run: fmt.Sprintf("r-%d", i), Round: round(t, fmt.Sprintf("r-%d", i), lines...)})
		}
		return out
	}
	ready, slot := 200.0, 200.0
	for _, c := range []struct {
		name    string
		now     *logparse.Round
		past    []Past
		run     *runrec.Run
		want    string
		mention string
	}{
		{"nothing to say", round(t, "r", step("PASS", "apply", "", 10)), past(6, step("PASS", "apply", "", 10)), nil, "", ""},
		{"slow", round(t, "r", step("PASS", "apply", "", 100)), past(6, step("PASS", "apply", "", 10)), nil, "slow", "took 1m40s; usually 10.0s (p90 10.0s, 6 runs)"},
		{"no baseline yet", round(t, "r", step("PASS", "apply", "", 100)), past(4, step("PASS", "apply", "", 10)), nil, "", ""},
		{"fast", round(t, "r", step("PASS", "apply", "", 1)), past(6, step("PASS", "apply", "", 10)), nil, "fast", "did it skip its work?"},
		{"timeout", round(t, "r", step("FAIL", "wait", "timeout: step limit 5m", 300)), past(3, step("FAIL", "wait", "timeout: step limit 5m", 300)), nil, "timeout", "4 of the last 4 runs"},
		{"retried", round(t, "r", step("PASS", "call api", "attempt 3/4", 5)), nil, nil, "retried", "passed on attempt 3"},
		{"failing", round(t, "r", step("FAIL", "check", "exit 1", 1)), past(2, step("FAIL", "check", "exit 1", 1)), nil, "failing", "failed in each of the last 3 runs"},
		{"failing, not yet a streak", round(t, "r", step("FAIL", "check", "exit 1", 1)), past(1, step("FAIL", "check", "exit 1", 1)), nil, "", ""},
		{"dry", round(t, "r", logparse.MarkLine("SKIP", "drop table", "dry run: set ALLOW_DROP=1 to approve")), past(2, logparse.MarkLine("SKIP", "drop table", "dry run: set ALLOW_DROP=1 to approve")), nil, "dry", "nobody approved it"},
		{"drift", round(t, "r", logparse.MarkLine("DRIFT", "orders has 3 extra rows", "")), past(1, logparse.MarkLine("DRIFT", "orders has 3 extra rows", "")), nil, "drift", "1 of the last 1 runs"},
		{"waited", round(t, "r", step("PASS", "x", "", 1)), nil,
			&runrec.Run{Run: "r", Lanes: []runrec.Lane{{Lane: 1, After: []int{2}, Ready: &ready, Slot: &slot}}}, "waited", "waited 3m20s for swim 2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			run := c.run
			if run == nil {
				run = &runrec.Run{Run: "r"}
			}
			obs := Compute(Input{Run: run, Rounds: map[int]*logparse.Round{1: c.now}, History: map[int][]Past{1: c.past}}, Thresholds{})
			if got := kinds(obs); got != c.want {
				t.Fatalf("kinds %q, want %q: %+v", got, c.want, obs)
			}
			if c.mention != "" && !strings.Contains(obs[0].Text, c.mention) {
				t.Errorf("text %q, want it to say %q", obs[0].Text, c.mention)
			}
		})
	}
}
