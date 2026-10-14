package ui

import (
	"os"
	"testing"
)

func TestPainterAndColors(t *testing.T) {
	if (Painter{On: false}).Paint(Red, "x") != "x" || (Painter{On: true}).Paint("", "x") != "x" {
		t.Error("painting off")
	}
	if got := (Painter{On: true}).Paint(Red, "x"); got != Red+"x"+Reset {
		t.Errorf("painted %q", got)
	}
	for state, want := range map[string]string{"PASS": Green, "approved": Green, "FAIL": Red, "interrupted": Red, "STOP": Red,
		"BLOCKED": Red, "skip": Yellow, "DRIFT": Yellow, "running?": Yellow, "running": Cyan, "idle": Dim, "MSG": Dim, "WARN": ""} {
		if got := StateColor(state); got != want {
			t.Errorf("StateColor(%s) = %q, want %q", state, got, want)
		}
	}
	if LaneColor(1) == LaneColor(2) || LaneColor(1) != LaneColor(7) {
		t.Error("lane colours cycle every six lanes")
	}
}

func TestColorEnabled(t *testing.T) {
	f, _ := os.CreateTemp(t.TempDir(), "out")
	defer f.Close()
	t.Setenv("NO_COLOR", "")
	t.Setenv("SWIM_COLOR", "1")
	if !ColorEnabled(f) {
		t.Error("SWIM_COLOR=1 forces colour")
	}
	t.Setenv("SWIM_COLOR", "")
	if ColorEnabled(f) {
		t.Error("a file isn't a terminal")
	}
	t.Setenv("SWIM_COLOR", "1")
	t.Setenv("NO_COLOR", "1")
	if ColorEnabled(f) {
		t.Error("NO_COLOR wins")
	}
}

func TestJobColors(t *testing.T) {
	p := Painter{On: true}
	got := p.Job("1.4", "")
	want := LaneColor(1) + "1" + Reset + LaneColor(1) + "." + Reset + LaneColor(4) + "4" + Reset
	if got != want {
		t.Errorf("Job(1.4) = %q, want %q", got, want)
	}
	if got := (Painter{}).Job("1.5.9", Bold); got != "1.5.9" {
		t.Errorf("no colour: %q", got)
	}
}
