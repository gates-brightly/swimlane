package timeline

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/runrec"
)

func f(v float64) *float64 { return &v }

// diamond: 1 -> {2 (slow), 3} -> 4, plus 5 failing and 6 skipped after it.
func diamond() *runrec.Run {
	t0 := time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC)
	return &runrec.Run{Run: "r-test", Started: t0, Finished: t0.Add(2500 * time.Millisecond), Lanes: []runrec.Lane{
		{Lane: 1, State: "passed", After: []int{}, Ready: f(0), Slot: f(0), Start: f(0.01), End: f(0.5)},
		{Lane: 2, State: "passed", After: []int{1}, Ready: f(0.5), Slot: f(0.5), Start: f(0.55), End: f(2.05)},
		{Lane: 3, State: "passed", After: []int{1}, Ready: f(0.5), Slot: f(0.8), Start: f(0.82), End: f(1.0)},
		{Lane: 4, State: "passed", After: []int{2, 3}, Ready: f(2.05), Locked: f(2.2), Slot: f(2.2), Start: f(2.3), End: f(2.4)},
		{Lane: 5, State: "failed", After: []int{}, Ready: f(0), Slot: f(0), Start: f(0.01), End: f(0.2)},
		{Lane: 6, State: "skipped", Reason: "swim 5 failed", After: []int{5}, End: f(0.2)},
	}}
}

func TestBuild(t *testing.T) {
	tl := Build(diamond(), nil)
	if got := joinInts(tl.Chain); got != "1 2 4" {
		t.Fatalf("chain = %s", got)
	}
	if tl.ChainS != 2.09 || tl.Total != 2.5 || tl.Overhead != 0.41 || tl.Makespan != 2.39 {
		t.Fatalf("chain %.3f total %.3f overhead %.3f makespan %.3f", tl.ChainS, tl.Total, tl.Overhead, tl.Makespan)
	}
	// Delays after the last parent: 2 0.05, 3 0.32, 4 0.25.
	if tl.Delays.Count != 3 || tl.Delays.Median != 0.25 || tl.Delays.Max != 0.32 || tl.Delays.MaxOf != 3 || tl.Delays.P95 != 0.32 {
		t.Fatalf("delays %+v", tl.Delays)
	}
	l := map[int]Lane{}
	for _, x := range tl.Lanes {
		l[x.Lane] = x
	}
	if l[3].QueueWait != 0.3 || l[4].LockWait != 0.15 || l[4].DepWait != 2.05 || l[1].DepWait != 0 {
		t.Fatalf("waits: 3=%+v 4=%+v", l[3], l[4])
	}
	if !l[2].OnChain || l[3].OnChain || l[6].Ran() || l[6].Delay != nil {
		t.Fatalf("lanes: %+v", tl.Lanes)
	}
}

func TestSteps(t *testing.T) {
	r := &logparse.Round{Declared: []string{"check", "verify"}, Results: []logparse.Result{
		{Kind: "PASS", Label: "a", Stage: "check", Step: true, Dur: 0.5},
		{Kind: "FAIL", Label: "b", Stage: "check", Step: true, Dur: 0.25},
		{Kind: "SKIP", Label: "mark", Stage: "check"},
		{Kind: "PASS", Label: "c", Stage: "verify", Step: true, Dur: 1},
	}}
	got := stepsOf(r, 2)
	if len(got) != 2 || got[0].Label != "stage check" || got[0].Dur != 0.75 || got[0].Kind != "FAIL" || got[1].Start != 2.75 {
		t.Fatalf("staged: %+v", got)
	}
	r.Declared = nil
	if got := stepsOf(r, 0); len(got) != 3 || got[2].Label != "c" || got[2].Start != 0.75 {
		t.Fatalf("plain: %+v", got)
	}
}

func TestText(t *testing.T) {
	var b bytes.Buffer
	Text(&b, Build(diamond(), nil), Options{Width: 80})
	s := b.String()
	for _, line := range []string{
		"swim  1 |████                  | 0.01 → 0.50    (root)                         ◆",
		"swim  3 |░░░░▒▒▒█              | 0.82 → 1.00    after 1",
		"swim  4 |░░░░░░░░░░░░░░░░░░▓ █ | 2.30 → 2.40    after 2 3                      ◆",
		"swim  6 |                      |     skipped    after 5                         skipped: swim 5 failed",
		"longest chain: 1 → 2 → 4",
		"start delay after last parent: median 0.25s · p95 0.32s · max 0.32s (swim 3)",
		"waiting on dependencies: ",
	} {
		if !strings.Contains(s, line) {
			t.Errorf("missing %q in:\n%s", line, s)
		}
	}
}

func TestTopKeepsChain(t *testing.T) {
	tl := Build(diamond(), nil)
	shown, hidden := Shown(tl, Options{Top: 1})
	var ns []int
	for _, l := range shown {
		ns = append(ns, l.Lane)
	}
	if joinInts(ns) != "1 2 4" || hidden != 3 {
		t.Fatalf("shown %v hidden %d", ns, hidden)
	}
}

func TestYAML(t *testing.T) {
	out, err := yaml.Marshal(Build(diamond(), nil))
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := yaml.Unmarshal(out, &back); err != nil || back["schema"] != Schema {
		t.Fatalf("%v %v", err, back["schema"])
	}
	for _, key := range []string{"run:", "total_s: 2.5", "longest_chain: [1, 2, 4]", "longest_chain_s: 2.09", "start_delay:", "waiting:", "dep_wait_s:"} {
		if !strings.Contains(string(out), key) {
			t.Errorf("yaml missing %q:\n%s", key, out)
		}
	}
}

func TestHTML(t *testing.T) {
	var b bytes.Buffer
	if err := HTML(&b, Build(diamond(), nil)); err != nil {
		t.Fatal(err)
	}
	s := b.String()
	if !strings.Contains(s, "<svg") || !strings.Contains(s, `class="run failed"`) || !strings.Contains(s, "skipped: swim 5 failed") || strings.Contains(s, "http") && strings.Contains(s, "src=") {
		t.Fatalf("html:\n%s", s)
	}
}
