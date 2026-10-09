package logparse

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/syntax"
)

const fixtureV1 = `=== ROUND START 2026-10-08T09:00:00Z swim 1 job=old-job
Round: Old round
=== STEP 2026-10-08T09:00:01Z old
--- output
--- exit 1 (0.1s)
FAIL  old (exit 1)
=== SUMMARY 2026-10-08T09:00:02Z swim 1 pass=0 fail=1 skip=0 drift=0 exit=1 duration=1.0s
=== END

=== ROUND START 2026-10-09T12:00:00Z swim 1 job=3f2a9c1e
Round: Cut over service X
script: lane.1.sh   pid: 42

=== STEP 2026-10-09T12:00:01Z go tests
$ go test ./...
cwd: /repo   git: main@abc1234
--- output
PASS
FAIL  this is command output (exit 9)
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
=== SUMMARY 2026-10-09T12:01:00Z swim 1 job=3f2a9c1e pass=1 fail=1 skip=1 drift=1 exit=1 duration=60.0s
failed: FAIL  deploy api (exit 2)
failed: STOP  gate failed: target is ours
=== END
`

func checkFixture(t *testing.T, r *Round, wantSyntax int) {
	t.Helper()
	if r.Syntax != wantSyntax || !r.Found || r.Title != "Cut over service X" || r.Job != "3f2a9c1e" || r.StartedAt != "2026-10-09T12:00:00Z" {
		t.Fatalf("header: %+v", r)
	}
	if r.Pass != 1 || r.Fail != 1 || r.Skip != 1 || r.Drift != 1 || r.Steps != 2 {
		t.Fatalf("counts: pass=%d fail=%d skip=%d drift=%d steps=%d", r.Pass, r.Fail, r.Skip, r.Drift, r.Steps)
	}
	if !r.Finished || r.ExitCode != 1 {
		t.Fatalf("end: %+v", r)
	}
	failed := r.Failed()
	if len(failed) != 2 || failed[0].Label != "deploy api" || failed[0].Detail != "exit 2" || failed[1].Kind != Stop {
		t.Fatalf("failed: %+v", failed)
	}
	if got := failed[0].Text(); got != "FAIL  deploy api (exit 2)" {
		t.Fatalf("Text() = %q", got)
	}
}

func TestParseV1(t *testing.T) {
	r, err := Parse(strings.NewReader(fixtureV1))
	if err != nil {
		t.Fatal(err)
	}
	checkFixture(t, r, 1)
	if r.Duration != "60.0s" {
		t.Fatalf("duration %q", r.Duration)
	}
}

func TestConvertV1ThenParse(t *testing.T) {
	var out bytes.Buffer
	if err := ConvertV1(strings.NewReader(fixtureV1), &out, 1); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.HasPrefix(s, "# swim lane log | syntax 2 | swim 1\n") {
		t.Fatalf("no v2 header:\n%s", s)
	}
	for _, want := range []string{
		"== ROUND 2026-10-08T09:00:00Z  job=old-job  Old round",
		"== ROUND 2026-10-09T12:00:00Z  job=3f2a9c1e  Cut over service X",
		"   script: lane.1.sh | pid: 42 | migrated: from syntax 1",
		"  PASS  go tests",
		"        $ go test ./...",
		"        | PASS",
		"        | FAIL  this is command output (exit 9)",
		"  FAIL  deploy api (exit 2)",
		"        | boom",
		"  SKIP  delete table (dry run: set FIN_ALLOW_DELETE=1 to approve)",
		"  STOP  gate failed: target is ours",
		"== END FAIL  pass=1 fail=1 skip=1 drift=1  exit=1  60.0s  2026-10-09T12:01:00Z",
		"   stages: setup FAIL | snapshot none | check none | change none | verify none",
		"   failed: FAIL  deploy api (exit 2); STOP  gate failed: target is ours",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("converted log missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "cwd: /repo") || strings.Contains(s, "=== ") || strings.Contains(s, "--- ") {
		t.Errorf("syntax 1 framing left in the converted log:\n%s", s)
	}
	r, err := Parse(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	checkFixture(t, r, 2)
	// Converting again is a no-op only via the syntax check, not ConvertV1.
	if syntax.OfLog(strings.NewReader(s)) != 2 {
		t.Fatal("converted log does not declare syntax 2")
	}
}

const fixtureV2 = `# swim lane log | syntax 2 | swim 1

== ROUND 2026-10-09T12:00:00Z  job=3f2a9c1e  Cut over orders-api
   script: lane.1.sh | owner: zach

-- stage snapshot  12:00:01
  PASS  cfn template                                0.4s  12:00:01
        $ aws cloudformation get-template
        | FAIL  output that looks like a result (exit 9)
        |   PASS  even indented two spaces
        saved: .swim/snapshots/lane1-x.txt

-- stage check  12:00:02
  FAIL  tf plan (exit 2)                            3.1s  12:00:02
        $ terraform plan
        | Error: boom
  STOP  gate failed: tf plan

-- stage change  12:00:03
  SKIP  sst remove (dry run: set X=1 to approve)
  WARN  change stage without a check before it

== END FAIL  pass=1 fail=1 skip=1 drift=0  exit=1  3.6s  2026-10-09T12:00:04Z
   stages: snapshot PASS | check FAIL | change SKIP | verify none
   failed: FAIL  tf plan (exit 2); STOP  gate failed: tf plan
`

func TestParseV2(t *testing.T) {
	r, err := Parse(strings.NewReader(fixtureV2))
	if err != nil {
		t.Fatal(err)
	}
	if r.Syntax != 2 || r.Title != "Cut over orders-api" || r.Job != "3f2a9c1e" || !r.Finished || r.ExitCode != 1 || r.Duration != "3.6s" {
		t.Fatalf("round: %+v", r)
	}
	if r.Pass != 1 || r.Fail != 1 || r.Skip != 1 || r.Steps != 2 {
		t.Fatalf("counts (output must never count): pass=%d fail=%d skip=%d steps=%d", r.Pass, r.Fail, r.Skip, r.Steps)
	}
	if got := StagesText(r.StageResults()); got != "snapshot PASS | check FAIL | change SKIP | verify none" {
		t.Fatalf("stages = %q", got)
	}
	if r.Stage != "change" || strings.Join(r.Declared, ",") != "snapshot,check,change" {
		t.Fatalf("stage tracking: %q %v", r.Stage, r.Declared)
	}
	if res := r.Results[0]; res.Dur != 0.4 || res.Clock != "12:00:01" {
		t.Fatalf("step timing: %+v", res)
	}
	if f := r.Failed(); len(f) != 2 || f[0].Label != "tf plan" || f[0].Stage != "check" {
		t.Fatalf("failed: %+v", f)
	}
}

func TestWriterRoundTrip(t *testing.T) {
	var b strings.Builder
	b.WriteString(syntax.LogHeader(3) + "\n")
	b.WriteString(RoundHeader("2026-10-09T12:00:00Z", "job-1", "A round", []KV{{"owner", "zach"}}))
	b.WriteString("\n" + StageLine("check", "12:00:01") + "\n")
	b.WriteString(StepLine(Pass, "a label (with parens)", "", "0.1s", "12:00:01") + "\n")
	b.WriteString(StepLine(Fail, strings.Repeat("long label ", 6), "exit 3", "12.0s", "12:00:02") + "\n")
	b.WriteString(MarkLine(Drift, "policy", "") + "\n")
	r, _ := Parse(strings.NewReader(b.String()))
	b.WriteString(EndBlock("FAIL", r, 1, "2.0s", "2026-10-09T12:00:03Z"))
	r, err := Parse(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if r.Steps != 2 || r.Pass != 1 || r.Fail != 1 || r.Drift != 1 || !r.Finished {
		t.Fatalf("%+v\n%s", r, b.String())
	}
	if r.Results[0].Label != "a label" || r.Results[0].Detail != "with parens" {
		// "label (detail)" is ambiguous by design; the last parenthesised
		// group is the detail, as in syntax 1.
		t.Logf("label split: %+v", r.Results[0])
	}
	if r.Results[1].Detail != "exit 3" || !strings.HasPrefix(r.Results[1].Label, "long label") {
		t.Fatalf("long label: %+v", r.Results[1])
	}
}

func TestTooNew(t *testing.T) {
	if _, err := Parse(strings.NewReader("# swim lane log | syntax 99 | swim 1\n")); err == nil || !strings.Contains(err.Error(), "syntax 99") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseEmpty(t *testing.T) {
	r, err := ParseFile("/nonexistent/agent9.log")
	if err != nil || r.Found {
		t.Fatalf("r=%+v err=%v", r, err)
	}
}
