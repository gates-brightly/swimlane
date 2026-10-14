package proc

import (
	"os"
	"os/exec"
	"testing"
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
