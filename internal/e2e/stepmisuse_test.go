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
			r := newRepo(t, "")
			// Lint refuses these before swim run starts ([step]); the lint-ignore
			// lets the round run, to prove the runtime fails closed by itself.
			r.shortLane(1, "# Round: drop it\n# Guards: DB_ALLOW_DROP  drop the table", "stage check\n# swim:lint-ignore step the runtime's own check is under test\n"+tc.gate+"\nstage change\nif guard DB_ALLOW_DROP \"drop the table\"; then\n  run \"drop\" echo DROPPED\nfi")
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
	r := newRepo(t, "")
	r.shortLane(1, "# Round: spellings", "stage check\n"+
		"gate \"dash dash\" -- echo gate-ran\n"+
		"run --timeout=1m \"opt equals\" echo run-ran\n"+
		"snapshot \"snap\" -- echo snap-ran\n"+
		"gate --retry=1 --backoff=1s \"fails closed\" -- false\n"+
		"run \"after the gate\" echo SHOULD-NOT-RUN")
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
