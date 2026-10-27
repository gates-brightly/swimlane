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

func TestSafe(t *testing.T) {
	for _, c := range []struct{ name, in, safe, keepSGR string }{
		{"plain", "deploy billing", "deploy billing", "deploy billing"},
		{"tab kept", "a\tb", "a\tb", "a\tb"},
		{"utf-8 kept", "cut over — 切り替え ✓", "cut over — 切り替え ✓", "cut over — 切り替え ✓"},
		{"erase line and CR", "drop prod\x1b[2K\rlist files", "drop prodlist files", "drop prodlist files"},
		{"backspace", "rm -rf /\b\b\b\bls", "rm -rf /ls", "rm -rf /ls"},
		{"newline and DEL", "a\nb\x7fc", "abc", "abc"},
		{"bidi override", "approve ‮elif‬ ok ⁦x⁩‏؜", "approve elif ok x", "approve elif ok x"},
		{"C1 CSI rune", "a\u009b2Kb", "a2Kb", "a2Kb"},
		{"8-bit CSI byte", "a\x9b2Kb", "a�2Kb", "a�2Kb"},
		{"invalid utf-8", "ok\xff\xfe", "ok��", "ok��"},
		{"OSC 8 link", "\x1b]8;;https://evil.example\x1b\\click\x1b]8;;\x1b\\", "click", "click"},
		{"OSC title BEL", "\x1b]0;pwned\x07x", "x", "x"},
		{"DCS", "\x1bPq#0;2;0;0;0\x1b\\x", "x", "x"},
		{"cursor up", "\x1b[1Ahidden", "hidden", "hidden"},
		{"colour", "\x1b[31mred\x1b[0m \x1b[1;32mok\x1b[m", "red ok", "\x1b[31mred\x1b[0m \x1b[1;32mok\x1b[m"},
		{"two-byte ESC", "\x1b7a\x1b8\x1b(Bb", "ab", "ab"},
		{"unterminated", "x\x1b[31", "x", "x"},
	} {
		if got := Safe(c.in); got != c.safe {
			t.Errorf("%s: Safe(%q) = %q, want %q", c.name, c.in, got, c.safe)
		}
		if got := SafeKeepSGR(c.in); got != c.keepSGR {
			t.Errorf("%s: SafeKeepSGR(%q) = %q, want %q", c.name, c.in, got, c.keepSGR)
		}
	}
	if got := SafeField("a\tb\nc"); got != "a bc" {
		t.Errorf("SafeField: %q", got)
	}
}
