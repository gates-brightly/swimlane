package launcher

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"
)

// earlyGrace is how long swim run waits, after a signal during its setup,
// to reach the point where it can stop cleanly; setup stuck longer (on a
// passphrase prompt) exits there and then.
const earlyGrace = 2 * time.Second

// EarlySignals catches swim run's signals from the moment it has lanes to
// run. Before, a Ctrl-C during setup (staging every lane's files, opening
// secrets) was lost when the shell started swim with SIGINT ignored (a
// background job), or killed it partway through writing its state. Until
// handOver, a signal only records that the run was interrupted, and the
// run stops before any lane starts. After, the interrupter gets them.
type EarlySignals struct {
	ch        chan os.Signal
	hit       atomic.Value // os.Signal: the first one, during setup
	handed    chan struct{}
	stopEarly chan struct{}
}

// CatchSignals starts catching SIGINT and SIGTERM for a swim run: as the
// first thing swim run does (the CLI passes it in Options.Signals), so a
// Ctrl-C however early is caught, not lost or fatal mid-setup.
func CatchSignals(errw io.Writer) *EarlySignals {
	e := &EarlySignals{ch: make(chan os.Signal, 4), handed: make(chan struct{}), stopEarly: make(chan struct{})}
	signal.Notify(e.ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case s := <-e.ch:
			e.hit.Store(s)
			fmt.Fprintln(errw, "swim: interrupted; stopping before any lane starts")
			select {
			case <-e.handed:
			case <-time.After(earlyGrace):
				os.Exit(130) // setup is blocked (a prompt): nothing has started
			}
		case <-e.handed:
		}
		close(e.stopEarly)
	}()
	return e
}

// handOver ends setup: it reports whether a signal came during it (the run
// must stop now), and otherwise passes every later signal to handle.
func (e *EarlySignals) handOver(handle func(os.Signal)) (interrupted bool) {
	close(e.handed)
	<-e.stopEarly
	if e.hit.Load() != nil {
		return true
	}
	go func() {
		for s := range e.ch {
			handle(s)
		}
	}()
	return false
}

// alsoHangup catches SIGHUP too (a run that ends its lanes when the
// terminal closes, unless --no-hangup).
func (e *EarlySignals) alsoHangup() { signal.Notify(e.ch, syscall.SIGHUP) }

// Stop stops catching signals.
func (e *EarlySignals) Stop() { signal.Stop(e.ch) }
