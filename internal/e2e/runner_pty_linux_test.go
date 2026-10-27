package e2e

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The first .ts lane with no #! line and no runner: at a terminal swim
// finds the candidates on PATH, asks, and saves the answer for this repo;
// the next run doesn't ask.
func TestRunnerConfirmAtTerminal(t *testing.T) {
	r := newSerialRepo(t, "")
	tsx := fakeRunner(t, r, "tsx")
	r.write("lane.1.ts", "// Round: typescript lane\nconsole.log('hi')\n", 0o644)

	ptm, pts := openPTY(t)
	defer ptm.Close()
	c := r.cmd(bin, "run", "1", "--plain")
	c.Stdin, c.Stdout, c.Stderr = pts, pts, pts
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	pts.Close()
	var mu sync.Mutex
	var out bytes.Buffer
	go io.Copy(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return out.Write(p)
	}), ptm)
	screen := func() string {
		mu.Lock()
		defer mu.Unlock()
		return out.String()
	}
	deadline := time.Now().Add(waitDeadline)
	for !strings.Contains(screen(), "[Y/n, a number, or type a command]") {
		if time.Now().After(deadline) {
			t.Fatalf("no runner question:\n%s", screen())
		}
		time.Sleep(20 * time.Millisecond)
	}
	ptm.Write([]byte("\r"))
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("swim run: %v\n%s", err, screen())
		}
	case <-time.After(waitDeadline):
		t.Fatalf("swim run didn't finish:\n%s", screen())
	}
	contains(t, "terminal", screen(), "lane.1.ts is the first .ts lane here", "1) "+tsx, "Run .ts lanes with "+tsx+"?",
		".ts lanes run with "+tsx+" (saved as runners.ts", "tsx runs lane.1.ts")
	cfg, _ := os.ReadFile(filepath.Join(envOf(r, "XDG_CONFIG_HOME"), "swim", "config.yml"))
	contains(t, "config", string(cfg), "runners:", "ts: "+tsx)

	// Remembered: off a terminal too.
	contains(t, "second run", r.mustSwim("run", "1", "--plain"), "tsx runs lane.1.ts")
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
