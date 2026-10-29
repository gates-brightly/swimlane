// Package testwait waits for events in tests, never for time. A wait
// checks its condition, and when it doesn't hold yet blocks until
// something it watches changes (a file under a directory, a process
// exiting, a writer it's told about), then checks again. Its deadline
// only fails a test that hung; while things work it costs nothing.
//
// Tests use it instead of time.Sleep, tickers or polling loops (the
// policy test bans those in test code).
package testwait

import (
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Slow is E2E_SLOW (default 1): CI runs the suites side by side on a
// loaded runner, so a deadline that fails a hung test there is longer.
var Slow = func() time.Duration {
	if f, err := strconv.Atoi(os.Getenv("E2E_SLOW")); err == nil && f >= 1 {
		return time.Duration(f)
	}
	return 1
}()

// Deadline bounds every wait: past it a test is hung, not slow.
var Deadline = 60 * time.Second * Slow

// A Source says when something it watches changed: Changed returns a
// channel that is closed at the next change after the call.
type Source interface {
	Changed() <-chan struct{}
}

// Until waits until cond holds, checking it again at every change src
// reports; a test still waiting at Deadline fails, naming what.
func Until(t testing.TB, what string, src Source, cond func() bool) {
	t.Helper()
	if !UntilOK(src, cond, Deadline) {
		t.Fatalf("timed out waiting for %s (%s)", what, Deadline)
	}
}

// UntilOK is Until without a test: false if cond didn't hold by within.
func UntilOK(src Source, cond func() bool, within time.Duration) bool {
	deadline := time.NewTimer(within)
	defer deadline.Stop()
	for {
		// Take the channel before checking, so a change while cond runs
		// isn't missed.
		ch := src.Changed()
		if cond() {
			return true
		}
		select {
		case <-ch:
		case <-deadline.C:
			return cond()
		}
	}
}

// Signal is a Source a writer notifies: every Notify closes the channel
// Changed last handed out.
type Signal struct {
	mu sync.Mutex
	ch chan struct{}
}

// Changed implements Source.
func (s *Signal) Changed() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ch == nil {
		s.ch = make(chan struct{})
	}
	return s.ch
}

// Notify wakes every wait on s.
func (s *Signal) Notify() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ch != nil {
		close(s.ch)
		s.ch = nil
	}
}

// Any is a Source that changes when any of its sources does.
type Any []Source

// Changed implements Source.
func (a Any) Changed() <-chan struct{} {
	out := make(chan struct{})
	chs := make([]<-chan struct{}, len(a))
	for i, s := range a {
		chs[i] = s.Changed()
	}
	var once sync.Once
	for _, ch := range chs {
		go func() {
			select {
			case <-ch:
				once.Do(func() { close(out) })
			case <-out: // another source fired first
			}
		}()
	}
	return out
}
