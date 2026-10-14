package proc

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestSame(t *testing.T) {
	me := os.Getpid()
	pid, start, ok := Parse(Record(me))
	if !ok || pid != me {
		t.Fatalf("Parse(Record) = %d %q %v", pid, start, ok)
	}
	if !Same(me, start) {
		t.Fatal("this process isn't the one it recorded")
	}
	if start != "" && Same(me, start+"1") {
		t.Fatal("a wrong start time matched")
	}
	if !Same(me, "") {
		t.Fatal("a bare pid of a live process should count")
	}
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skip(err)
	}
	if Same(cmd.Process.Pid, "") {
		t.Error("an exited process counted")
	}
	for _, bad := range []string{"", "x", "0", "-3 1", "1 2 3"} {
		if _, _, ok := Parse(bad); ok {
			t.Errorf("Parse(%q) accepted", bad)
		}
	}
	if pid, start, ok := Parse("42\n"); !ok || pid != 42 || start != "" {
		t.Errorf("legacy pid: %d %q %v", pid, start, ok)
	}
	if _, start, _ := Parse("42 -"); start != "" {
		t.Errorf("unknown start: %q", start)
	}
}

// A process that exited but wasn't reaped yet (a zombie, as a killed
// lane stays where nothing reaps orphans) isn't alive.
func TestZombieIsNotAlive(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	defer cmd.Wait()
	pid := cmd.Process.Pid
	deadline := time.Now().Add(5 * time.Second)
	for !zombie(pid) {
		if time.Now().After(deadline) {
			t.Skip("can't tell a zombie on this system")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if Alive(pid) || Same(pid, Start(pid)) {
		t.Error("a zombie counts as alive")
	}
}
