package logparse

import "testing"

// The line readers that replaced the regexes read what the writers write.
func TestLineReaders(t *testing.T) {
	line := StepLine(Fail, "deploy orders", "exit 2", "1.5s", "12:04:10")
	kind, rest, ok := SplitResult(line)
	if !ok || kind != Fail {
		t.Fatalf("SplitResult(%q) = %q %q %v", line, kind, rest, ok)
	}
	head, dur, clock, ok := SplitTimed(rest)
	if !ok || dur != "1.5s" || clock != "12:04:10" {
		t.Fatalf("SplitTimed(%q) = %q %q %q %v", rest, head, dur, clock, ok)
	}
	if l, d := splitLabel(head); l != "deploy orders" || d != "exit 2" {
		t.Errorf("splitLabel(%q) = %q %q", head, l, d)
	}
	if l, d := splitLabel("plain label"); l != "plain label" || d != "" {
		t.Errorf("splitLabel plain = %q %q", l, d)
	}
	// As the regex it replaced: the detail starts at the first " (".
	if l, d := splitLabel("deploy (orders) (exit 2)"); l != "deploy" || d != "orders) (exit 2" {
		t.Errorf("splitLabel nested = %q %q", l, d)
	}
	if _, _, _, ok := SplitTimed("no timing here"); ok {
		t.Error("SplitTimed took text with no timing")
	}
	if _, _, ok := SplitResult("  RUNNING  x"); ok {
		t.Error("RUNNING isn't a logged kind")
	}
	if k, _, ok := SplitResult("  RUNNING  x", "RUNNING"); !ok || k != "RUNNING" {
		t.Error("RUNNING as an extra kind")
	}
	hdr := RoundHeader("2026-10-16T12:00:00Z", "abc-123", "deploy orders", nil)[1:]
	hdr = hdr[:len(hdr)-1]
	if ts, job, title, ok := roundV2(hdr); !ok || ts != "2026-10-16T12:00:00Z" || job != "abc-123" || title != "deploy orders" {
		t.Errorf("roundV2(%q) = %q %q %q %v", hdr, ts, job, title, ok)
	}
	end := "== END FAIL  pass=1 fail=2 skip=0 drift=0  exit=3  4.5s  2026-10-16T12:00:05Z  run=r-1"
	if st, code, d, ok := endV2(end); !ok || st != "FAIL" || code != 3 || d != "4.5s" {
		t.Errorf("endV2 = %q %d %q %v", st, code, d, ok)
	}
	if code, ok := EndExit(end); !ok || code != 3 {
		t.Errorf("EndExit = %d %v", code, ok)
	}
	for detail, want := range map[string]int{"exit 2": 2, "timeout: step limit 5s, exit 124": 124, "exit 1, interrupted": 1} {
		if code, ok := detailExit(detail); !ok || code != want {
			t.Errorf("detailExit(%q) = %d %v", detail, code, ok)
		}
	}
	if _, ok := detailExit("failed as expected: exit 3"); ok {
		t.Error("detailExit read an exit that isn't the step's")
	}
}
