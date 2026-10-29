package testwait

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// The watcher sees a write in a directory created after it started.
func TestWatcherSeesNewDirectories(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "skipped"), 0o755)
	w, err := Watch(root, func(d string) bool { return filepath.Base(d) == "skipped" })
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	sub := filepath.Join(root, "a", "b")
	go func() {
		os.MkdirAll(sub, 0o755)
		// The new directory is watched once it's seen: write until the
		// test sees the file, each write an event of its own.
		for i := 0; ; i++ {
			if _, err := os.Stat(filepath.Join(root, "done")); err == nil {
				return
			}
			os.WriteFile(filepath.Join(sub, "f"), []byte{byte(i)}, 0o644)
			<-w.Changed()
		}
	}()
	Until(t, "a file in a new directory", w, func() bool {
		_, err := os.Stat(filepath.Join(sub, "f"))
		return err == nil
	})
	os.WriteFile(filepath.Join(root, "done"), nil, 0o644)
}

func TestSignal(t *testing.T) {
	var s Signal
	var got atomic.Bool
	go func() { got.Store(true); s.Notify() }()
	Until(t, "the notified flag", &s, got.Load)
}

// Exited says a running process hasn't exited and an ended one has.
func TestExited(t *testing.T) {
	c := exec.Command("cat")
	in, _ := c.StdinPipe()
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	if Exited(c.Process.Pid, 0) {
		t.Fatal("cat, still reading, has exited")
	}
	in.Close()
	if !Exited(c.Process.Pid, Deadline) {
		t.Fatal("cat didn't exit after its input closed")
	}
	c.Wait()
	if !Exited(c.Process.Pid, 0) {
		t.Error("a reaped pid hasn't exited")
	}
}
