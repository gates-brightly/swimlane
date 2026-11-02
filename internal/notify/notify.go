// Package notify tells the operator's terminal what swim is doing while
// they look elsewhere: a desktop notification when a question waits on them
// or a run or batch ends, and a progress bar on the terminal's tab. Both are
// escape sequences the terminal reads (Ghostty shows them natively):
//
//	OSC 777 ; notify ; <title> ; <body> BEL   a notification
//	OSC 9 ; 4 ; <state> ; <percent> BEL       the progress bar
//
// A terminal that doesn't know them ignores them. swim sends them only when
// the notify setting is on and stdout is a terminal (config.NotifyOn).
package notify

import (
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/gates-brightly/swimlane/internal/ui"
)

// State is the progress bar's state (OSC 9;4's second field).
type State int

const (
	Hidden        State = 0 // no bar
	Normal        State = 1 // Percent done
	Error         State = 2 // Percent done, and something failed
	Indeterminate State = 3 // busy, how far unknown
	Paused        State = 4 // Percent done, waiting (on the operator)
)

// Progress is what the bar shows.
type Progress struct {
	State   State
	Percent int // 0-100; ignored when Hidden or Indeterminate
}

// Terminal writes notifications and the progress bar to the operator's
// terminal. A nil Terminal sends nothing, so callers needn't check.
type Terminal struct {
	mu   sync.Mutex
	w    io.Writer
	last string // the bar as last sent: a redraw sends it only when it changes
}

// New returns a Terminal writing to w (the terminal swim draws on).
func New(w io.Writer) *Terminal { return &Terminal{w: w} }

// Notify sends a desktop notification. Both texts are made safe to send
// (no escape sequences or control characters, so a lane's text can't end
// the sequence early); the title loses its ;, which would end it.
func (t *Terminal) Notify(title, body string) {
	if t == nil {
		return
	}
	title = strings.ReplaceAll(clean(title), ";", ",")
	t.write(fmt.Sprintf("\x1b]777;notify;%s;%s\x07", title, clean(body)))
}

// SetProgress shows p on the terminal's progress bar, if it changed.
func (t *Terminal) SetProgress(p Progress) {
	if t == nil {
		return
	}
	seq := progressSeq(p)
	t.mu.Lock()
	defer t.mu.Unlock()
	if seq == t.last {
		return
	}
	t.last = seq
	io.WriteString(t.w, seq)
}

// Done hides the progress bar: swim leaves none behind when it exits.
func (t *Terminal) Done() { t.SetProgress(Progress{State: Hidden}) }

func (t *Terminal) write(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	io.WriteString(t.w, s)
}

func progressSeq(p Progress) string {
	switch p.State {
	case Hidden, Indeterminate:
		return fmt.Sprintf("\x1b]9;4;%d\x07", p.State)
	}
	return fmt.Sprintf("\x1b]9;4;%d;%d\x07", p.State, min(max(p.Percent, 0), 100))
}

// Of is the progress of done of total, in state (Normal, Error or Paused);
// Indeterminate while nothing is done yet, so an empty bar never looks
// stuck.
func Of(done, total int, state State) Progress {
	if total <= 0 {
		return Progress{State: Hidden}
	}
	if done <= 0 && state == Normal {
		return Progress{State: Indeterminate}
	}
	return Progress{State: state, Percent: done * 100 / total}
}

// clean makes text safe inside the sequence: one line, no controls.
func clean(s string) string {
	s = ui.Safe(strings.Join(strings.Fields(s), " "))
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}
