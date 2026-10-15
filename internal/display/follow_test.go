package display

import (
	"strings"
	"testing"
	"time"
)

func TestEndHeader(t *testing.T) {
	zero, one := 0, 1
	if got := EndHeader(nil, 80, true); got != nil {
		t.Errorf("nil end: %q", got)
	}

	passed := EndHeader(&FollowEnd{Result: "passed", Exit: &zero, Elapsed: 133 * time.Second}, 40, false)
	if len(passed) != 1 || passed[0] != "== ✓ PASSED · 2:13 ==" {
		t.Errorf("passed, plain: %q", passed)
	}

	failed := EndHeader(&FollowEnd{Result: "failed", Exit: &one, Details: []string{"FAIL  go test (exit 1)"}}, 40, false)
	if len(failed) != 2 || failed[0] != "== ✗ FAILED · exit 1 ==" || failed[1] != "  FAIL  go test (exit 1)" {
		t.Errorf("failed, plain: %q", failed)
	}

	// In color the bar fills the width, red for a failure.
	bar := EndHeader(&FollowEnd{Result: "failed", Exit: &one}, 30, true)[0]
	if !strings.HasPrefix(bar, "\x1b[41;97m\x1b[1m ✗ FAILED · exit 1") || len([]rune(stripANSI(bar))) != 30 {
		t.Errorf("failed bar: %q", bar)
	}
	if !strings.HasPrefix(EndHeader(&FollowEnd{Result: "passed"}, 30, true)[0], "\x1b[42;30m") {
		t.Error("passed bar isn't green")
	}
	if !strings.HasPrefix(EndHeader(&FollowEnd{Result: "interrupted"}, 30, true)[0], "\x1b[43;30m") {
		t.Error("interrupted bar isn't yellow")
	}

	// More than maxEndDetails details: the rest are counted.
	many := EndHeader(&FollowEnd{Result: "failed", Details: []string{"a", "b", "c", "d", "e", "f", "g"}}, 40, false)
	if len(many) != 1+maxEndDetails+1 || many[len(many)-1] != "  … 2 more" {
		t.Errorf("many details: %q", many)
	}
}
