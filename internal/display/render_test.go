package display

import (
	"bytes"
	"flag"
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
	in := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == 0x1b {
			in = true
			continue
		}
		if in {
			if c >= 0x40 && c <= 0x7e && c != '[' {
				in = false
			}
			continue
		}
		b.WriteByte(c)
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
