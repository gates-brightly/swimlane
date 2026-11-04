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
			r.shortLane(1, "# Round: drop it\n# Guards: DB_ALLOW_DROP  drop the table", "stage check\n"+tc.gate+"\nstage change\nif guard DB_ALLOW_DROP \"drop the table\"; then\n  run \"drop\" echo DROPPED\nfi")
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
