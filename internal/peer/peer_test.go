package peer

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPeerAndDescends(t *testing.T) {
	if !Supported {
		t.Skip("no peer credentials on this platform")
	}
	path := filepath.Join(t.TempDir(), "s")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan int, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			got <- 0
			return
		}
		defer c.Close()
		p, _ := PID(c)
		got <- p
	}()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if p := <-got; p != os.Getpid() {
		t.Fatalf("peer pid %d, want %d", p, os.Getpid())
	}
	// A child descends from this process; this process doesn't descend
	// from the child.
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	if !Descends(cmd.Process.Pid, os.Getpid()) {
		t.Error("a child doesn't descend from its parent")
	}
	if Descends(os.Getpid(), cmd.Process.Pid) {
		t.Error("a parent descends from its child")
	}
	if !Descends(os.Getpid(), os.Getpid()) {
		t.Error("a process doesn't count as its own")
	}
}

// Cwd reads a process's working directory: this one's, and a child's
// started elsewhere (as swim's hidden commands read a swim ancestor's).
func TestCwd(t *testing.T) {
	if !Supported {
		t.Skip("no process introspection on this platform")
	}
	want, _ := filepath.EvalSymlinks(mustGetwd(t))
	got, ok := Cwd(os.Getpid())
	if g, _ := filepath.EvalSymlinks(got); !ok || g != want {
		t.Fatalf("Cwd(self) = %q, %v; want %q", got, ok, want)
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	c := exec.Command("sleep", "5")
	c.Dir = dir
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer c.Process.Kill()
	got, ok = Cwd(c.Process.Pid)
	if g, _ := filepath.EvalSymlinks(got); !ok || g != dir {
		t.Fatalf("Cwd(child) = %q, %v; want %q", got, ok, dir)
	}
	if _, ok := Cwd(1 << 30); ok {
		t.Error("Cwd of a pid that doesn't exist")
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}
