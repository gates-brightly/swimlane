package launcher

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/status"
)

// heartbeat tracks when each running lane last wrote a line.
type heartbeat struct {
	mu      sync.Mutex
	last    map[int]time.Time
	started map[int]time.Time
}

func (h *heartbeat) begin(n int, at time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.started[n], h.last[n] = at, at
}

func (h *heartbeat) end(n int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.started, n)
	delete(h.last, n)
}

func (h *heartbeat) saw(n int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.started[n]; ok {
		h.last[n] = time.Now()
	}
}

// silent returns the running lanes quiet for at least d, marking them as
// heard from now so each prints once per d.
func (h *heartbeat) silent(d time.Duration) map[int]time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[int]time.Duration{}
	now := time.Now()
	for n, t := range h.last {
		if now.Sub(t) >= d {
			out[n] = now.Sub(h.started[n])
			h.last[n] = now
		}
	}
	return out
}

// run prints a heartbeat line for silent lanes until the returned func is
// called.
func (h *heartbeat) run(o Options, disp *display.Display) func() {
	done := make(chan struct{})
	tick := o.Heartbeat / 4
	if tick > time.Second {
		tick = time.Second
	}
	go func() {
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				quiet := h.silent(o.Heartbeat)
				if len(quiet) == 0 {
					continue
				}
				st, _ := status.Load(o.Root)
				var ns []int
				for n := range quiet {
					ns = append(ns, n)
				}
				sort.Ints(ns)
				for _, n := range ns {
					step := "lane script"
					if st != nil {
						if l := st.Get(n); l != nil && l.CurrentStep != "" {
							step = l.CurrentStep
						}
					}
					disp.Message(fmt.Sprintf("%sstill running: %s (%s)", display.Prefix(n, disp.Color()), step, display.Elapsed(quiet[n])))
				}
			}
		}
	}()
	return func() { close(done) }
}

// or returns s, or def when s is empty.
func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
