package logparse

import (
	"strings"
	"testing"
)

const fixture = `=== ROUND START 2026-10-08T09:00:00Z swim 1
Round: Old round
=== STEP 2026-10-08T09:00:01Z old
--- output
--- exit 1 (0.1s)
FAIL  old (exit 1)
=== SUMMARY 2026-10-08T09:00:02Z swim 1 pass=0 fail=1 skip=0 drift=0 exit=1 duration=1.0s
=== END

=== ROUND START 2026-10-09T12:00:00Z swim 1
Round: Cut over service X

=== STEP 2026-10-09T12:00:01Z go tests
$ go test ./...
--- output
PASS
FAIL  this is command output (exit 9)
PASS  also output
--- exit 0 (3.2s)
PASS  go tests

=== STEP 2026-10-09T12:00:05Z deploy api
$ deploy
--- output
boom
--- exit 2 (0.5s)
FAIL  deploy api (exit 2)

SKIP  delete table (dry run: set FIN_ALLOW_DELETE=1 to approve)
DRIFT  queue policy differs
STOP  gate failed: target is ours
=== SUMMARY 2026-10-09T12:01:00Z swim 1 pass=1 fail=1 skip=1 drift=1 exit=1 duration=60.0s
=== END
`

func TestParseLastRound(t *testing.T) {
	r, err := Parse(strings.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if !r.Found || r.Title != "Cut over service X" || r.StartedAt != "2026-10-09T12:00:00Z" {
		t.Fatalf("header: %+v", r)
	}
	if r.Pass != 1 || r.Fail != 1 || r.Skip != 1 || r.Drift != 1 || r.Steps != 2 {
		t.Fatalf("counts: pass=%d fail=%d skip=%d drift=%d steps=%d", r.Pass, r.Fail, r.Skip, r.Drift, r.Steps)
	}
	if !r.Finished || r.ExitCode != 1 || r.Duration != "60.0s" {
		t.Fatalf("summary: %+v", r)
	}
	failed := r.Failed()
	if len(failed) != 2 || failed[0].Label != "deploy api" || failed[0].Detail != "exit 2" || failed[1].Kind != Stop {
		t.Fatalf("failed: %+v", failed)
	}
	if got := failed[0].Text(); got != "FAIL  deploy api (exit 2)" {
		t.Fatalf("Text() = %q", got)
	}
}

func TestParseUnfinishedInterruptedRound(t *testing.T) {
	log := `=== ROUND START 2026-10-09T12:00:00Z swim 2
Round: Diagnose Y
=== STEP 2026-10-09T12:00:01Z long
--- output
partial
--- interrupted (signal interrupt) exit 130 (2.0s)
FAIL  long (exit 130, interrupted)
`
	r, err := Parse(strings.NewReader(log))
	if err != nil {
		t.Fatal(err)
	}
	if r.Finished || !r.Interrupt || r.Fail != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestParseEmpty(t *testing.T) {
	r, err := ParseFile("/nonexistent/agent9.log")
	if err != nil || r.Found {
		t.Fatalf("r=%+v err=%v", r, err)
	}
}
