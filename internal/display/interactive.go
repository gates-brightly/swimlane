package display

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"github.com/gates-brightly/swimlane/internal/ui"
)

// Interactive mode: the whole screen is drawn on the alternate screen
// buffer, stdin is in raw mode and keys move a viewport (tui.go). The
// terminal must come back on every exit path: a normal finish, Ctrl-C,
// SIGTERM/SIGHUP, an early return (Restore) and a panic in the view's own
// goroutines (recovered, restored, re-panicked).

// redrawEvery coalesces redraws: output and state changes mark the frame
// dirty and it is drawn at most this often.
const redrawEvery = 50 * time.Millisecond

// testPanic makes the redraw loop panic once a lane view opens, so the e2e
// tests can check the terminal is restored after a crash.
var testPanic = os.Getenv("SWIM_TEST_TUI_PANIC") == "1"

// interactive is the terminal side of the interactive view.
type interactive struct {
	in          int // stdin's fd, in raw mode while the view is up
	onInterrupt func()

	ttyMu  sync.Mutex // guards the terminal state below; never held with Display.mu waiting on it
	raw    *term.State
	active bool

	model    *tui
	frame    []string // rows last drawn, to redraw only what changed
	dirty    bool
	termSig  bool          // SIGTERM or SIGHUP arrived: never hold the view open
	quit     chan struct{} // closed when the held view may close
	quitOnce sync.Once
}

// TUIAllowed reports whether the environment lets swim use the interactive
// view: not when SWIM_TUI=0 or in CI (CI=true or 1).
func TUIAllowed(getenv func(string) string) bool {
	if getenv("SWIM_TUI") == "0" {
		return false
	}
	ci := strings.ToLower(strings.TrimSpace(getenv("CI")))
	return ci != "true" && ci != "1"
}

// foreground reports whether this process is in the terminal's foreground
// process group: a background job would be stopped (SIGTTOU) for changing
// the terminal's mode, and doesn't get its keys.
func foreground(fd int) bool {
	pg, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
	return err == nil && pg == syscall.Getpgrp()
}

// interruptAsTerminal does what Ctrl-C does outside raw mode: SIGINT to the
// terminal's foreground process group (swim and its lanes, or whatever the
// launcher has arranged), so the launcher's SIGINT handling runs unchanged.
func interruptAsTerminal(fd int) {
	if pg, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP); err == nil && pg > 0 && pg == syscall.Getpgrp() {
		syscall.Kill(-pg, syscall.SIGINT)
		return
	}
	syscall.Kill(os.Getpid(), syscall.SIGINT)
}

// startInteractive enters raw mode and the alternate screen and starts the
// key reader and redraw loop. It returns false (and changes nothing) if the
// terminal won't go raw; the caller then uses live mode.
func (d *Display) startInteractive() bool {
	it := d.tui
	st, err := term.MakeRaw(it.in)
	if err != nil {
		return false
	}
	it.ttyMu.Lock()
	it.raw, it.active = st, true
	it.ttyMu.Unlock()
	it.quit = make(chan struct{})
	d.mu.Lock()
	fmt.Fprint(d.out, "\x1b[?1049h\x1b[?25l\x1b[2J")
	d.drawFrameLocked()
	d.mu.Unlock()

	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGWINCH, syscall.SIGTERM, syscall.SIGHUP)
	d.wg.Add(2)
	go func() {
		defer d.wg.Done()
		defer d.restoreOnPanic()
		readKeys(it.in, d.done, func(k Key) {
			d.mu.Lock()
			act := it.model.key(k, d.views)
			it.dirty = true
			d.mu.Unlock()
			switch act {
			case actInterrupt:
				if it.onInterrupt != nil {
					it.onInterrupt()
				} else {
					interruptAsTerminal(it.in)
				}
			case actQuit:
				it.quitOnce.Do(func() { close(it.quit) })
			}
		})
	}()
	go func() {
		defer d.wg.Done()
		defer d.restoreOnPanic()
		defer signal.Stop(sigs)
		tick := time.NewTicker(redrawEvery)
		defer tick.Stop()
		last := time.Time{}
		for {
			select {
			case <-d.done:
				return
			case s := <-sigs:
				d.mu.Lock()
				if s == syscall.SIGWINCH {
					if cols, rows, err := term.GetSize(d.fd); err == nil {
						d.cols, d.rows = cols, rows
					}
					it.frame = nil // redraw everything
					fmt.Fprint(d.out, "\x1b[2J")
					it.dirty = true
				} else {
					it.termSig = true
					it.quitOnce.Do(func() { close(it.quit) })
				}
				d.mu.Unlock()
			case now := <-tick.C:
				d.mu.Lock()
				if it.model.pollLog() {
					it.dirty = true
				}
				// The throbber and clocks move every 100ms.
				if it.dirty || now.Sub(last) >= 100*time.Millisecond {
					d.drawFrameLocked()
					last = now
				}
				if testPanic && it.model.laneN != 0 {
					panic("SWIM_TEST_TUI_PANIC")
				}
				d.mu.Unlock()
			}
		}
	}()
	return true
}

// stopInteractive closes the view at the end of the run. If someone is
// reading a lane's log or has scrolled back, it first holds the screen open
// ("run finished · q to close") until they close it.
func (d *Display) stopInteractive() {
	it := d.tui
	d.mu.Lock()
	hold := it.model.holds() && !it.termSig
	if hold {
		it.model.finished = true
		it.model.pollLog() // the lane's END block
		d.drawFrameLocked()
	}
	d.mu.Unlock()
	if hold {
		<-it.quit
	}
	d.closeDone()
	d.wg.Wait()
	d.restoreTTY()
}

// closeDone tells the view's goroutines to stop. Safe to call twice.
func (d *Display) closeDone() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.done == nil {
		return
	}
	select {
	case <-d.done:
	default:
		close(d.done)
	}
}

// drawFrameLocked draws the frame, writing only the rows that changed.
func (d *Display) drawFrameLocked() {
	it := d.tui
	it.ttyMu.Lock()
	active := it.active
	it.ttyMu.Unlock()
	if !active {
		return
	}
	rows := it.model.render(d.titleLocked(), d.views, d.now(), d.rows, d.cols)
	var b strings.Builder
	for i, r := range rows {
		if i < len(it.frame) && it.frame[i] == r {
			continue
		}
		fmt.Fprintf(&b, "\x1b[%d;1H%s%s\x1b[K", i+1, r, ui.Reset)
	}
	if b.Len() > 0 {
		fmt.Fprint(d.out, b.String())
	}
	it.frame, it.dirty = rows, false
}

// restoreTTY leaves the alternate screen and raw mode. Safe to call twice
// and from any goroutine; it takes only the terminal lock.
func (d *Display) restoreTTY() {
	it := d.tui
	it.ttyMu.Lock()
	defer it.ttyMu.Unlock()
	if !it.active {
		return
	}
	it.active = false
	fmt.Fprint(d.out, "\x1b[?25h\x1b[?1049l")
	term.Restore(it.in, it.raw)
}

// restoreOnPanic, deferred in the view's goroutines, puts the terminal back
// before a panic ends the process, so the shell isn't left in raw mode.
func (d *Display) restoreOnPanic() {
	if r := recover(); r != nil {
		d.restoreTTY()
		panic(r)
	}
}
