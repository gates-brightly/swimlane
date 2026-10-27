package launcher

import (
	"io"
	"os"
	"syscall"
	"testing"
	"time"
)

// A signal during swim run's setup stops the run before any lane starts;
// one after the hand-over goes to the interrupter.
func TestEarlySignals(t *testing.T) {
	e := CatchSignals(io.Discard)
	syscall.Kill(os.Getpid(), syscall.SIGINT)
	// Caught, not merely sent: under -race on a loaded runner it's late.
	for end := time.Now().Add(10 * time.Second); e.hit.Load() == nil && time.Now().Before(end); {
		time.Sleep(5 * time.Millisecond)
	}
	if !e.handOver(func(os.Signal) { t.Error("a setup signal reached the interrupter") }) {
		t.Fatal("a Ctrl-C during setup didn't stop the run")
	}
	e.Stop()

	e = CatchSignals(io.Discard)
	defer e.Stop()
	got := make(chan os.Signal, 1)
	if e.handOver(func(s os.Signal) { got <- s }) {
		t.Fatal("no signal, but the run stopped")
	}
	syscall.Kill(os.Getpid(), syscall.SIGINT)
	select {
	case s := <-got:
		if s != syscall.SIGINT {
			t.Errorf("interrupter got %v", s)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a Ctrl-C after the hand-over didn't reach the interrupter")
	}
}
