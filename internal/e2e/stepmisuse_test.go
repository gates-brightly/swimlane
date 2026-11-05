package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shortLane writes lane n in the short form: its header lines, then body.
func (r *repo) shortLane(n int, header, body string) {
	r.t.Helper()
	s := "#!/usr/bin/env swim\n# swim: syntax 4\n" + header + "\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(r.root, fmt.Sprintf("lane.%d.sh", n)), []byte(s), 0o755); err != nil {
		r.t.Fatal(err)
	}
}

// A step written wrong fails closed: the round stops there, before the
// change it guards, and the log says why. Each of these once let the
// approved drop run, the gate never having checked anything.
func TestStepWrittenWrongStopsTheRound(t *testing.T) {
	for _, tc := range []struct{ name, gate, why string }{
		{"no command", `gate "no command given"`, `step "no command given" has a label but no command`},
		{"misspelled option", `gate --retries 2 "typo option" false`, "unknown step option --retries"},
		{"option missing its value", `gate "x" --timeout`, ""}, // label "x", command --timeout: runs, fails, stops as a failed gate
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRepo(t)
			// Lint refuses these before swim run starts ([step]); the lint-ignore
			// lets the round run, to prove the runtime fails closed by itself.
			r.shortLane(1, "# Round: drop it\n# Guards: DB_ALLOW_DROP  drop the table", "stage check\n# swim:lint-ignore step the runtime's own check is under test\n"+tc.gate+"\nstage change\nif guard DB_ALLOW_DROP \"drop the table\"; then\n  step \"drop\" echo DROPPED\nfi")
			cmd := r.cmd(bin, "run", "1", "--plain")
			cmd.Env = append(cmd.Env, "DB_ALLOW_DROP=1")
			out, _ := cmd.CombinedOutput()
			log := r.log(1)
			if strings.Contains(string(out), "DROPPED") || strings.Contains(log, "DROPPED") {
				t.Fatalf("the change ran past a gate written wrong:\n%s\n%s", out, log)
			}
			if !strings.Contains(log, "== END FAIL") {
				t.Errorf("the round didn't end FAIL:\n%s", log)
			}
			if tc.why != "" && !strings.Contains(log, "STOP  step written wrong: "+tc.why) {
				t.Errorf("the log doesn't say why it stopped (%q):\n%s", tc.why, log)
			}
		})
	}
}

// The unambiguous spellings run as written: -- after the label (as swim
// run takes it from other languages) and --opt=value.
func TestStepAcceptsDashDashAndOptEquals(t *testing.T) {
	r := newRepo(t)
	r.shortLane(1, "# Round: spellings", "stage check\n"+
		"gate \"dash dash\" -- echo gate-ran\n"+
		"step --timeout=1m \"opt equals\" echo run-ran\n"+
		"snapshot \"snap\" -- echo snap-ran\n"+
		"gate --retry=1 --backoff=1s \"fails closed\" -- false\n"+
		"step \"after the gate\" echo SHOULD-NOT-RUN")
	out, _ := r.swim("run", "1", "--plain")
	log := r.log(1)
	for _, want := range []string{"PASS  dash dash", "| gate-ran", "PASS  opt equals", "| run-ran", "PASS  snapshot: snap", "| snap-ran", "FAIL  fails closed", "STOP  gate failed: fails closed"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s\n%s", want, log, out)
		}
	}
	if strings.Contains(log, "SHOULD-NOT-RUN") {
		t.Errorf("a step ran after a failed gate:\n%s", log)
	}
	if strings.Contains(log, "$ -- ") {
		t.Errorf("-- was run as the command:\n%s", log)
	}
}

// A gate that fails in a subshell ($(...), or fed by a pipe) ends the
// round, not just the subshell: the change after it never runs, and the
// round ends FAIL, not interrupted.
func TestGateInASubshellStopsTheRound(t *testing.T) {
	for _, tc := range []struct{ name, gate string }{
		{"command substitution", `acct=$(gate "account check" false)`},
		{"pipe", `echo x | gate "piped gate" false`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRepo(t)
			r.shortLane(1, "# Round: change it\n# Guards: ALLOW_CHANGE  change it",
				"stage check\n# swim:lint-ignore step-subshell the runtime's own check is under test\n"+tc.gate+
					"\nstage change\nif guard ALLOW_CHANGE \"change it\"; then\n  step \"change\" echo CHANGED\nfi")
			cmd := r.cmd(bin, "run", "1", "--plain")
			cmd.Env = append(cmd.Env, "ALLOW_CHANGE=1")
			out, _ := cmd.CombinedOutput()
			log := r.log(1)
			if strings.Contains(log, "CHANGED") || strings.Contains(string(out), "CHANGED") {
				t.Fatalf("the change ran past a failed gate:\n%s\n%s", out, log)
			}
			if !strings.Contains(log, "== END FAIL") || !strings.Contains(log, "STOP  gate failed") {
				t.Errorf("the round didn't end FAIL at the gate:\n%s", log)
			}
			if strings.Contains(log, "fail=0") {
				t.Errorf("the summary lost the gate's failure:\n%s", log)
			}
		})
	}
}

// A stop marker an earlier round left behind doesn't turn this round's
// interrupt into a failure: the lane clears it as it starts.
func TestStaleStopMarkerIsCleared(t *testing.T) {
	r := newRepo(t)
	r.shortLane(1, "# Round: fine", "stage check\nstep \"x\" true")
	os.MkdirAll(filepath.Join(r.root, ".swim", "logs"), 0o755)
	marker := filepath.Join(r.root, ".swim", "logs", "agent1.log.stopped")
	os.WriteFile(marker, nil, 0o644)
	if out, code := r.swim("run", "1", "--plain"); code != 0 {
		t.Fatalf("run 1: exit %d\n%s", code, out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the stale stop marker is still there")
	}
}
