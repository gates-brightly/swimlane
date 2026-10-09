package display

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/gates-brightly/swimlane/internal/ui"
)

// Display is the launcher's view. In live mode it pins the panel with an
// ANSI scroll region; in plain mode it prints prefixed lines and one line
// per state change.
type Display struct {
	out   io.Writer
	fd    int
	live  bool
	color bool
	title func(now time.Time) string
	now   func() time.Time
	cap   int

	mu       sync.Mutex
	views    []LaneView
	restored bool
	rows     int
	cols     int
	panelH   int
	done     chan struct{}
	wg       sync.WaitGroup
}

// Options configures New.
type Options struct {
	Out   *os.File
	Plain bool // force plain output
	Title func(now time.Time) string
	Cap   int // max_parallel; > 0 adds "running n/cap · queued m" to the title
}

// New prepares a display for lanes 1..len(views). Live mode needs a
// terminal, colour allowed, and enough rows for the panel plus output.
func New(o Options, views []LaneView) *Display {
	d := &Display{
		out:   o.Out,
		fd:    int(o.Out.Fd()),
		color: ui.ColorEnabled(o.Out) && !o.Plain,
		title: o.Title,
		now:   time.Now,
		cap:   o.Cap,
		views: views,
	}
	if d.title == nil {
		d.title = func(time.Time) string { return "swim" }
	}
	d.panelH = PanelRows(len(views)) + 2 // title + lane rows + separator
	if !o.Plain && d.color && ui.IsTTY(o.Out) {
		if cols, rows, err := term.GetSize(d.fd); err == nil && rows >= d.panelH+5 && cols >= 30 {
			d.live, d.cols, d.rows = true, cols, rows
		}
	}
	return d
}

// Live reports whether the pinned panel is in use.
func (d *Display) Live() bool { return d.live }

// Color reports whether output is coloured.
func (d *Display) Color() bool { return d.color }

// Start draws the panel and begins the throbber ticker.
func (d *Display) Start() {
	d.done = make(chan struct{})
	if !d.live {
		// Announce lanes that begin blocked; later changes print from Set.
		d.mu.Lock()
		for _, v := range d.views {
			if v.State == Waiting {
				p := ui.Painter{On: d.color}
				fmt.Fprintln(d.out, Prefix(v.N, d.color)+p.Paint(ui.StateColor(v.State), WaitingText(v.WaitingOn)))
			}
		}
		d.mu.Unlock()
		return
	}
	d.mu.Lock()
	fmt.Fprint(d.out, "\x1b[2J\x1b[H")
	d.setRegionLocked()
	d.drawLocked()
	d.mu.Unlock()

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer signal.Stop(winch)
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-d.done:
				return
			case <-winch:
				d.mu.Lock()
				if cols, rows, err := term.GetSize(d.fd); err == nil {
					d.cols, d.rows = cols, rows
				}
				d.setRegionLocked()
				d.drawLocked()
				d.mu.Unlock()
			case <-tick.C:
				d.mu.Lock()
				d.drawLocked()
				d.mu.Unlock()
			}
		}
	}()
}

// Stop draws the final panel and restores the terminal. Safe to call twice.
func (d *Display) Stop() {
	d.mu.Lock()
	if d.done == nil {
		d.mu.Unlock()
		return
	}
	select {
	case <-d.done:
		d.mu.Unlock()
		return
	default:
		close(d.done)
	}
	d.mu.Unlock()
	d.wg.Wait()
	if !d.live {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.drawLocked()
	d.restoreLocked()
}

// Restore resets the scroll region if Stop didn't; for early exits.
func (d *Display) Restore() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.restoreLocked()
}

func (d *Display) restoreLocked() {
	if d.live && !d.restored {
		d.restored = true
		// Resetting the region homes the cursor; put it back at the bottom.
		fmt.Fprintf(d.out, "\x1b[r\x1b[%d;1H\n", d.rows)
	}
}

// Line prints one line of lane n's output.
func (d *Display) Line(n int, text string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	prefix := Prefix(n, d.color)
	if !d.live {
		line := prefix + text
		if d.color && strings.Contains(text, "\x1b[") {
			line += ui.Reset // don't let a lane's colour bleed into the next line
		}
		fmt.Fprintln(d.out, line)
		return
	}
	// Live: wrap to one column short of the edge so the terminal never
	// wraps (or holds a pending wrap) itself; continuation lines keep the
	// lane prefix.
	prefixW := len(fmt.Sprintf("[%d] ", n))
	var b strings.Builder
	for _, part := range WrapANSI(text, d.cols-1-prefixW) {
		b.WriteString(prefix + part)
		if strings.Contains(part, "\x1b[") {
			b.WriteString(ui.Reset)
		}
		b.WriteString("\n")
	}
	fmt.Fprint(d.out, b.String())
}

// Message prints a launcher line (not tied to a lane) in the output area.
func (d *Display) Message(text string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	fmt.Fprintln(d.out, text)
}

// Set updates lane n's view. In plain mode a changed state prints a line.
func (d *Display) Set(n int, fn func(*LaneView)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.views {
		if d.views[i].N != n {
			continue
		}
		before := d.views[i]
		fn(&d.views[i])
		after := d.views[i]
		if d.live {
			d.drawLocked()
		} else if msg := transition(before, after); msg != "" {
			p := ui.Painter{On: d.color}
			fmt.Fprintln(d.out, Prefix(n, d.color)+p.Paint(ui.StateColor(after.State), msg))
		}
		return
	}
}

// Views returns a copy of the current lane views.
func (d *Display) Views() []LaneView {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]LaneView(nil), d.views...)
}

// transition describes a state change for plain output.
func transition(a, b LaneView) string {
	if a.State == b.State && fmt.Sprint(a.WaitingOn) == fmt.Sprint(b.WaitingOn) {
		return ""
	}
	switch b.State {
	case Waiting:
		return WaitingText(b.WaitingOn)
	case Locked:
		if a.State == Locked && a.LockWait == b.LockWait {
			return ""
		}
		return "waiting for lock " + b.LockWait
	case Queued:
		if a.State == Queued {
			return "" // position changes aren't worth a line each
		}
		return fmt.Sprintf("queued (%d ahead)", b.QueuePos)
	case Running:
		return "started: " + b.Round
	case Passed:
		return "PASS (" + Elapsed(b.Finished.Sub(b.Started)) + ")"
	case Failed:
		return fmt.Sprintf("FAIL (exit %d, %s)", b.Exit, Elapsed(b.Finished.Sub(b.Started)))
	case Interrupted:
		return "INTERRUPTED (" + Elapsed(b.Finished.Sub(b.Started)) + ")"
	case Skipped:
		return "SKIP (" + b.Reason + ")"
	}
	return ""
}

// setRegionLocked confines scrolling to the rows below the panel and parks
// the cursor on the last row, where new output lines appear.
func (d *Display) setRegionLocked() {
	fmt.Fprintf(d.out, "\x1b[%d;%dr\x1b[%d;1H", d.panelH+1, d.rows, d.rows)
}

// drawLocked repaints the panel without moving the output cursor.
func (d *Display) drawLocked() {
	title := d.title(d.now())
	if d.cap > 0 {
		running, queued := 0, 0
		for _, v := range d.views {
			switch v.State {
			case Running:
				running++
			case Queued:
				queued++
			}
		}
		title += fmt.Sprintf(" · running %d/%d · queued %d", running, d.cap, queued)
	}
	lines := RenderPanel(title, d.views, d.cols, d.now(), d.color)
	var b strings.Builder
	b.WriteString("\x1b7") // save cursor
	for i, l := range lines {
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K%s", i+1, l)
	}
	b.WriteString("\x1b8") // restore cursor
	fmt.Fprint(d.out, b.String())
}
