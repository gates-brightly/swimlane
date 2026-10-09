package display

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var update = flag.Bool("update", false, "rewrite golden files")

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i = escEnd(s, i)
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func fixture() ([]LaneView, time.Time) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 200*int(time.Millisecond), time.UTC) // throbber frame 2: "-"
	return []LaneView{
		{N: 1, State: Running, Round: "Cut over service X to Terraform", Job: "3f2a9c1e-7b4d-4e2a-9c1e-7b4d4e2a9c1e", Started: now.Add(-72 * time.Second)},
		{N: 2, State: Waiting, Round: "Diagnose Y", WaitingOn: []int{1}},
		{N: 3, State: Idle, Round: ""},
		{N: 4, State: Waiting, Round: "Remove old stack", WaitingOn: []int{2, 3}},
		{N: 5, State: Passed, Round: "Audit", Started: now.Add(-3 * time.Minute), Finished: now.Add(-2 * time.Minute)},
		{N: 6, State: Failed, Exit: 2, Round: "Deploy", Started: now.Add(-30 * time.Second), Finished: now},
		{N: 7, State: Skipped, Reason: "swim 6 failed", Round: "Cleanup"},
	}, now
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		os.MkdirAll("testdata", 0o755)
		os.WriteFile(path, []byte(got), 0o644)
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create)", err)
	}
	if got != string(want) {
		t.Errorf("%s mismatch\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func TestRenderPanelGolden(t *testing.T) {
	views, now := fixture()
	title := "swim · app · main@abc1234 · 1:12"
	golden(t, "panel_plain.golden", strings.Join(RenderPanel(title, views, 80, now, false), "\n")+"\n")
	golden(t, "panel_color.golden", strings.Join(RenderPanel(title, views, 80, now, true), "\n")+"\n")
}

func TestRenderPanelContent(t *testing.T) {
	views, now := fixture()
	lines := RenderPanel("t", views, 80, now, false)
	if len(lines) != len(views)+2 {
		t.Fatalf("got %d lines", len(lines))
	}
	all := strings.Join(lines, "\n")
	for _, want := range []string{"swim 1", "swim 7", "Cut over service X to Terraform 3f2a9c1e", "- waiting on swim 1 ", "waiting on swim 2, 3", "- running 1:12", "PASS 1:00", "FAIL exit 2 0:30", "SKIP (swim 6 failed)", "idle"} {
		if !strings.Contains(all, want) {
			t.Errorf("panel missing %q:\n%s", want, all)
		}
	}
	// Coloured output has the same visible text.
	colored := RenderPanel("t", views, 80, now, true)
	for i := range lines {
		if stripANSI(colored[i]) != lines[i] {
			t.Errorf("line %d differs once colour stripped:\n%q\n%q", i, stripANSI(colored[i]), lines[i])
		}
	}
}

func TestRenderPanelTruncatesToWidth(t *testing.T) {
	views, now := fixture()
	for _, w := range []int{30, 45, 60} {
		for _, l := range RenderPanel(strings.Repeat("title ", 20), views, w, now, true) {
			if n := utf8.RuneCountInString(stripANSI(l)); n > w {
				t.Errorf("width %d: line has %d columns: %q", w, n, stripANSI(l))
			}
		}
	}
}

func TestThrobberCycles(t *testing.T) {
	base := time.UnixMilli(0)
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		seen[Throbber(base.Add(time.Duration(i)*100*time.Millisecond))] = true
	}
	if len(seen) != 4 {
		t.Fatalf("frames = %v", seen)
	}
}

func TestPlainTransitions(t *testing.T) {
	var buf bytes.Buffer
	d := &Display{out: &buf, views: []LaneView{{N: 1, State: Idle}, {N: 2, State: Idle}}, now: time.Now, title: func(time.Time) string { return "" }}
	d.Set(2, func(v *LaneView) { v.State, v.WaitingOn = Waiting, []int{1} })
	d.Set(2, func(v *LaneView) { v.State, v.WaitingOn = Waiting, []int{1} }) // no change, no line
	d.Line(1, "hello")
	d.Set(2, func(v *LaneView) { v.State, v.Reason = Skipped, "swim 1 failed" })
	got := buf.String()
	want := "[2] waiting on swim 1\n[1] hello\n[2] SKIP (swim 1 failed)\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestWrapANSI(t *testing.T) {
	cases := []struct {
		in   string
		w    int
		want []string
	}{
		{"", 5, []string{""}},
		{"abcdefghij", 4, []string{"abcd", "efgh", "ij"}},
		{"abcd", 4, []string{"abcd"}},
		// Colour codes take no columns and carry over the break.
		{"\x1b[32mabcdef\x1b[0mgh", 4, []string{"\x1b[32mabcd", "\x1b[32mef\x1b[0mgh"}},
		{"a\tb", 20, []string{"a       b"}},
		{"héllo wörld", 5, []string{"héllo", " wörl", "d"}},
		// OSC hyperlinks are never split.
		{"\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\", 2, []string{"\x1b]8;;http://x\x1b\\li", "nk\x1b]8;;\x1b\\"}},
	}
	for _, c := range cases {
		got := WrapANSI(c.in, c.w)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("WrapANSI(%q, %d) = %q, want %q", c.in, c.w, got, c.want)
		}
		for _, l := range got {
			if n := utf8.RuneCountInString(stripANSI(l)); n > c.w {
				t.Errorf("line %q is %d columns, over %d", l, n, c.w)
			}
		}
	}
}

func TestLiveLineNeverReachesLastColumn(t *testing.T) {
	var buf bytes.Buffer
	d := &Display{out: &buf, live: true, color: true, cols: 40, views: []LaneView{{N: 1}}, now: time.Now, title: func(time.Time) string { return "" }}
	d.Line(1, strings.Repeat("x", 100))
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines: %q", len(lines), lines)
	}
	for _, l := range lines {
		vis := stripANSI(l)
		if !strings.HasPrefix(vis, "[1] ") || utf8.RuneCountInString(vis) > 39 {
			t.Errorf("bad line %q", vis)
		}
	}
}

func manyLanes(now time.Time, states map[int]string, n int) []LaneView {
	var vs []LaneView
	for i := 1; i <= n; i++ {
		st, ok := states[i]
		if !ok {
			st = Idle
		}
		vs = append(vs, LaneView{N: i, State: st, Round: fmt.Sprintf("round %d", i), WaitingOn: []int{1}, Started: now, Finished: now})
	}
	return vs
}

func TestPanelCapsRowsAndPrefersRunning(t *testing.T) {
	now := time.Now()
	// 24 lanes: 3 running, 1 failed, 4 waiting, 6 passed, 2 done, 8 idle.
	states := map[int]string{
		20: Running, 21: Running, 22: Running, 9: Failed,
		10: Waiting, 11: Waiting, 12: Queued, 13: Waiting,
		1: Passed, 2: Passed, 3: Passed, 4: Passed, 5: Passed, 6: Passed,
		7: Done, 8: Done,
	}
	lines := RenderPanel("t", manyLanes(now, states, 24), 100, now, false)
	if len(lines) != PanelRows(24)+2 || PanelRows(24) != MaxRows {
		t.Fatalf("got %d lines", len(lines))
	}
	all := strings.Join(lines, "\n")
	for _, n := range []int{20, 21, 22, 9, 10, 11, 12, 13} {
		if !strings.Contains(all, fmt.Sprintf(" swim %d ", n)) {
			t.Errorf("swim %d (running/failed/waiting) hidden:\n%s", n, all)
		}
	}
	// 14 rows: 3 running + 1 failed + 4 waiting + 6 passed; done and idle hidden.
	if strings.Contains(all, " swim 7 ") || strings.Contains(all, " swim 14 ") {
		t.Errorf("done/idle lanes should be hidden first:\n%s", all)
	}
	contains := strings.Contains(lines[len(lines)-2], "… 10 more: 2 done earlier, 8 idle")
	if !contains {
		t.Errorf("more line = %q", lines[len(lines)-2])
	}
	// Visible rows stay in lane order.
	last := 0
	for _, l := range lines[1 : len(lines)-2] {
		var n int
		fmt.Sscanf(strings.TrimSpace(l), "swim %d", &n)
		if n <= last {
			t.Errorf("rows out of order:\n%s", all)
		}
		last = n
	}
}

func TestPanelHidesPassedBeforeRunning(t *testing.T) {
	now := time.Now()
	states := map[int]string{}
	for i := 1; i <= 20; i++ {
		states[i] = Running
	}
	for i := 1; i <= 10; i++ {
		states[i] = Passed
	}
	lines := RenderPanel("t", manyLanes(now, states, 20), 100, now, false)
	all := strings.Join(lines, "\n")
	for i := 11; i <= 20; i++ {
		if !strings.Contains(all, fmt.Sprintf(" swim %d ", i)) {
			t.Errorf("running swim %d hidden", i)
		}
	}
	if !strings.Contains(lines[len(lines)-2], "6 more: 6 passed") {
		t.Errorf("more line = %q", lines[len(lines)-2])
	}
	// Up to MaxRows lanes: no more line, nothing hidden.
	if l := RenderPanel("t", manyLanes(now, nil, MaxRows), 100, now, false); len(l) != MaxRows+2 || strings.Contains(strings.Join(l, ""), "more:") {
		t.Errorf("%d lanes should all show", MaxRows)
	}
}
