package launcher

import (
	"io"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/testwait"
)

// A signal during swim run's setup stops the run before any lane starts;
// one after the hand-over goes to the interrupter.
func TestEarlySignals(t *testing.T) {
	e := CatchSignals(notifyWriter{&displayed})
	syscall.Kill(os.Getpid(), syscall.SIGINT)
	// Caught, not merely sent: catching it prints a line.
	testwait.Until(t, "the signal caught", &displayed, func() bool { return e.hit.Load() != nil })
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
