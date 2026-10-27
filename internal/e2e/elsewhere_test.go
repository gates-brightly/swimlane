package e2e

import (
	"bytes"
	"strings"
	"syscall"
	"testing"
)

// Two swims in one repo: a lane the other swim runs shows in this one's
// view as running elsewhere, then with its result, not as idle.
func TestLaneRunningElsewhere(t *testing.T) {
	long(t)
	r := newRepo(t, "")
	r.script(1, "the other swim's lane", `run "work" `+hold("go1"))
	r.script(2, "this swim's lane", `run "longer" `+hold("go2"))

	other := r.cmd(bin, "run", "1", "--plain")
	other.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var oout bytes.Buffer
	other.Stdout, other.Stderr = &oout, &oout
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Kill(-other.Process.Pid, syscall.SIGKILL) })
	waitFor(t, func() bool { return laneOf(r, 1).State == "running" })

	// Lane 1 ends while this swim follows it; lane 2 outlasts it.
	this := start(t, r.cmd(bin, "run", "2", "--plain"), nil)
	this.saw(t, "[1] running in another swim")
	r.release("go1")
	this.saw(t, "[1] PASS")
	r.release("go2")
	if code := this.wait(t); code != 0 {
		t.Fatalf("exit %d\n%s", code, this.out.String())
	}
	out := this.out.String()
	other.Wait()
	i := strings.Index(out, "[1] running in another swim: the other swim's lane")
	j := strings.Index(out, "[1] PASS")
	if i < 0 || j < i {
		t.Errorf("lane 1 not followed from the other swim:\n%s", out)
	}
	// Lane 1 is the other swim's: never started, nor summarized, here.
	if strings.Contains(out, "[1] started") || strings.Contains(out, "  swim 1 ") {
		t.Errorf("this swim claimed lane 1:\n%s", out)
	}
}
