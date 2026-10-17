package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/logparse"
)

const slowLane = `run "quick" echo hello
run "slow" bash -c 'for i in 1 2 3 4 5 6; do echo tick-$i; sleep 0.3; done'
run "after" true`

func spooled(r *repo, n int, want string) func() bool {
	return func() bool {
		data, _ := os.ReadFile(filepath.Join(r.root, ".swim", "logs", "agent1.log.partial"))
		return strings.Contains(string(data), want)
	}
}

func TestLogShowsRunningStep(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "slow", slowLane)
	withHeader(t, r, 1, "# Job: 7e57a110-1111-4222-8333-444455556666")
	c := r.cmd(bin, "run", "1", "--plain")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer c.Wait()
	waitFor(t, spooled(r, 1, "tick-2"))

	out := r.mustSwim("log", "1")
	contains(t, "swim log 1", out, "PASS  quick", "  RUNNING  slow (in progress)", "        $ bash -c", "        | tick-1", "        | tick-2")
	if strings.Contains(r.mustSwim("log", "1", "--raw"), "RUNNING") {
		t.Error("--raw must be the file exactly")
	}
	contains(t, "swim log JOB", r.mustSwim("log", "7e57a110"), "RUNNING  slow (in progress)", "| tick-1")

	var d logparse.Doc
	if err := yaml.Unmarshal([]byte(r.mustSwim("log", "1", "--yaml")), &d); err != nil {
		t.Fatal(err)
	}
	last := d.Results[len(d.Results)-1]
	if d.Result != "running" || last.Result != "RUNNING" || last.Label != "slow" || len(last.Output) < 2 || last.Output[0] != "tick-1" || !strings.HasPrefix(last.Command, "bash -c") {
		t.Fatalf("--yaml running step: %+v / %+v", d, last)
	}

	c.Wait()
	if out := r.mustSwim("log", "1"); strings.Contains(out, "RUNNING") || !strings.Contains(out, "PASS  slow") {
		t.Errorf("after the run the log shows only its blocks:\n%s", out)
	}
}

func TestLogFollowUntilDone(t *testing.T) {
	long(t)
	r := newRepo(t, "")
	r.script(1, "slow then fail", slowLane+`
run "fails" false`)
	c := r.cmd(bin, "run", "1", "--plain")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer c.Wait()
	waitFor(t, spooled(r, 1, "tick-1"))
	f := r.cmd(bin, "log", "1", "-f", "--until-done")
	out, err := f.CombinedOutput()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 1 {
		t.Fatalf("--until-done should exit with the round's status (1): %v\n%s", err, out)
	}
	s := string(out)
	contains(t, "follow", s, "RUNNING  slow (in progress)", "| tick-1", "| tick-6", "  PASS  slow", "  FAIL  fails (exit 1)", "== END FAIL")
	if strings.Count(s, "| tick-3") != 1 || strings.Count(s, "  PASS  slow") != 1 {
		t.Errorf("a streamed step's output must not repeat when its block lands:\n%s", s)
	}
	if strings.Index(s, "| tick-6") > strings.Index(s, "  PASS  slow") {
		t.Errorf("output should stream before the step's result:\n%s", s)
	}
	// On an idle lane, --until-done prints the last round and exits with it.
	if _, code := r.swim("log", "1", "--until-done"); code != 1 {
		t.Errorf("idle lane, failed last round: exit %d", code)
	}
}

func TestLogCutOffStep(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "x", `run "a" true`)
	r.mustSwim("run", "1")
	// A spool left by a swim step that was killed (no lane running).
	os.WriteFile(filepath.Join(r.root, ".swim", "logs", "agent1.log.partial"),
		[]byte("#swim-spool\tstuck\t12:00:00\tsleep 99\npartial output\n"), 0o644)
	contains(t, "cut off", r.mustSwim("log", "1"), "RUNNING  stuck (cut off: swim step stopped; recorded as FAIL when the lane next runs)", "| partial output")
}
