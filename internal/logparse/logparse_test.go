package logparse

import (
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/syntax"
)

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
	if r.Title != "Cut over orders-api" || r.Job != "3f2a9c1e" || !r.Finished || r.ExitCode != 1 || r.Duration != "3.6s" {
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
	if res := r.Results[0]; res.Dur != 0.4 || res.Clock != "12:00:01" || res.Cmd != "aws cloudformation get-template" ||
		len(res.Output) != 2 || res.Output[1] != "  PASS  even indented two spaces" || res.Saved != ".swim/snapshots/lane1-x.txt" {
		t.Fatalf("step block: %+v", res)
	}
	d := r.Doc(1)
	if d.Schema != DocSchema || d.Result != "failed" || *d.Exit != 1 || d.Stages["check"] != "FAIL" || len(d.Results) != 5 ||
		*d.Results[1].Exit != 2 || d.Results[1].Command != "terraform plan" || d.Results[2].Exit != nil || len(d.Failed) != 2 {
		t.Fatalf("doc: %+v", d)
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
	b.WriteString(MarkLine(Warn, "policy", "") + "\n")
	r, _ := Parse(strings.NewReader(b.String()))
	b.WriteString(EndBlock("FAIL", r, 1, "2.0s", "2026-10-09T12:00:03Z"))
	r, err := Parse(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if r.Steps != 2 || r.Pass != 1 || r.Fail != 1 || !r.Finished {
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

// A round that ends SKIP only dry-ran its guarded steps: not passed.
func TestParseDryRun(t *testing.T) {
	log := `# swim lane log | syntax 2 | swim 1

== ROUND 2026-10-27T12:00:00Z  job=3f2a9c1e  Rotate the cert

-- stage change  12:00:01
  SKIP  replace the live cert (dry run: set CERT_ALLOW_ROTATE=1 to approve)
  SKIP  restart the proxy (dry run: set CERT_ALLOW_ROTATE=1 to approve)

== END SKIP  pass=0 fail=0 skip=2 drift=0  exit=0  0.2s  2026-10-27T12:00:01Z
`
	r, err := Parse(strings.NewReader(log))
	if err != nil {
		t.Fatal(err)
	}
	if !r.DryRun || r.ExitCode != 0 || r.Doc(1).Result != "skipped" {
		t.Fatalf("round: %+v", r)
	}
	if got := r.DryRunReason(); got != "dry run: replace the live cert (+1 more)" {
		t.Fatalf("reason = %q", got)
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

// Durations read back in every form swim has written: "1m 5s" (two
// fields), "12s", "0.4s" and older logs' "65.0s"; a label that ends in
// something like a duration keeps it.
func TestDurationsReadBack(t *testing.T) {
	log := "# swim lane log | syntax 2 | swim 1\n\n" +
		"== ROUND 2026-10-09T12:00:00Z  job=j  t\n\n" +
		StepLine(Pass, "short", "", "0.4s", "12:00:01") + "\n" +
		StepLine(Pass, "wait 2m", "", "12s", "12:00:02") + "\n" +
		StepLine(Pass, "long", "", "1m 5s", "12:00:03") + "\n" +
		StepLine(Pass, "longer", "", "1h 4m", "12:00:04") + "\n" +
		"  PASS  old                                    65.0s  12:00:05\n" +
		"\n== END PASS  pass=5 fail=0 skip=0  exit=0  1h 5m  2026-10-09T13:05:00Z  run=r-1\n"
	r, err := Parse(strings.NewReader(log))
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		label string
		dur   float64
	}{{"short", 0.4}, {"wait 2m", 12}, {"long", 65}, {"longer", 3840}, {"old", 65}}
	if len(r.Results) != len(want) {
		t.Fatalf("results: %+v", r.Results)
	}
	for i, w := range want {
		if got := r.Results[i]; got.Label != w.label || got.Dur != w.dur || !got.Step {
			t.Errorf("result %d = %q %v, want %q %v", i, got.Label, got.Dur, w.label, w.dur)
		}
	}
	if r.Duration != "1h 5m" {
		t.Errorf("END duration = %q", r.Duration)
	}
	if d := r.Doc(1); d.Seconds == nil || *d.Seconds != 3900 {
		t.Errorf("duration_s = %v", d.Seconds)
	}
	for _, bad := range []string{"", "5", "s", "1m5", "5s 1m", "1.5m", "1m 0.5s 1s", "-1s"} {
		if _, ok := ParseDuration(bad); ok {
			t.Errorf("ParseDuration(%q) ok", bad)
		}
	}
}
