package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A first run says what happened and little else: no signing warning in a
// repo with no approvals set up, one summary for one lane, and a round
// header that leaves out what's at its default.
func TestQuietDefaults(t *testing.T) {
	r := newRepo(t, "")
	r.mustSwim("config", "signing", "auto")
	r.mustSwim("config", "signing_key", filepath.Join(t.TempDir(), "no-such-key.pub"))
	os.Remove(filepath.Join(r.root, "lookup.yml"))
	r.shortLane(1, "# Round: say hello", "stage check\nrun \"hello\" echo hi")

	out, code := r.swim("run", "1", "--plain")
	if code != 0 {
		t.Fatalf("run 1: exit %d\n%s", code, out)
	}
	if strings.Contains(out, "no signing key") {
		t.Errorf("a repo with no lookup.yml warned about signing:\n%s", out)
	}
	if strings.Contains(out, "swim summary") || !strings.Contains(out, "swim 1 summary: PASS") || !strings.Contains(out, "logs: swim log 1") {
		t.Errorf("one lane should print its own summary, then only where its log is:\n%s", out)
	}
	head := strings.Split(r.log(1), "\n")[3] // the round's context line
	for _, gone := range []string{"owner: -", "after: -", "timeout: none", "step-timeout: none", "guards: -", "git: -", "(staged "} {
		if strings.Contains(head, gone) {
			t.Errorf("the round header still says %q:\n%s", gone, head)
		}
	}
	for _, kept := range []string{"run: r-", "script: lane.1.sh", "sha256: ", "manifest: "} {
		if !strings.Contains(head, kept) {
			t.Errorf("the round header lost %q:\n%s", kept, head)
		}
	}

	// With approvals set up, an unsigned run is worth saying.
	r.write("lookup.yml", "users: {}\n", 0o644)
	if out, _ := r.swim("run", "1", "--plain"); !strings.Contains(out, "no signing key") {
		t.Errorf("a repo with lookup.yml didn't warn about an unsigned run:\n%s", out)
	}
	// Two lanes still get the table.
	r.shortLane(2, "# Round: again", "stage check\nrun \"hello\" echo hi")
	if out, _ := r.swim("run", "1", "2", "--plain"); !strings.Contains(out, "swim summary") {
		t.Errorf("two lanes lost the summary table:\n%s", out)
	}
}

// What cost a turn is in .swim.log: a command swim refused as typed (the
// command and message, not its arguments), and the lint errors lint, run
// and queue found, by code. Nothing is written outside a repo swim is in.
func TestUsageAndLintAreLogged(t *testing.T) {
	r := newRepo(t, "")
	r.shortLane(1, "# Round: two mistakes", "stage check\nrun echo hi\ngate --retries 2 \"x\" true")
	r.swim("lint", "1")
	r.swim("run", "1", "--plain")
	r.swim("queue", "1")
	r.swim("status", "--json")
	r.swim("statsu")
	r.swim("config", "signing_key") // not a usage error: prints the value
	log := r.mustSwim("log")
	for _, want := range []string{
		"lane.1.sh: step x2 (via lint)", "lane.1.sh: step x2 (via run)", "lane.1.sh: step x2 (via queue)",
		"status: unknown flag --json", `unknown command "statsu"`,
	} {
		found := false
		for _, line := range strings.Split(log, "\n") {
			if f := strings.Fields(line); len(f) > 3 && (f[2] == "lint" || f[2] == "usage") && strings.Contains(line, want) {
				found = true
			}
		}
		if !found {
			t.Errorf(".swim.log has no lint or usage line with %q:\n%s", want, log)
		}
	}
	if strings.Count(log, " usage ") != 2 {
		t.Errorf("want exactly two usage lines (swim config KEY isn't one):\n%s", log)
	}
	// Outside any repo swim is set up in, nothing is written.
	out := t.TempDir()
	c := r.cmd(bin, "statsu")
	c.Dir = out
	c.Run()
	if _, err := os.Stat(filepath.Join(out, ".swim.log")); err == nil {
		t.Error("a usage error outside a swim repo wrote .swim.log")
	}
}
