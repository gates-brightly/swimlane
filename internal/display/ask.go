package display

import (
	"fmt"
	"strings"

	"golang.org/x/term"

	"github.com/gates-brightly/swimlane/internal/ui"
)

// CanAsk reports whether the operator can be asked a question: input and
// output are both a terminal.
func (d *Display) CanAsk() bool { return d.in != nil }

// Ask puts question to the operator and waits for a key: y approves; n,
// Enter or Esc declines (the default is no). Ctrl-C declines and runs the
// interrupt (as it would anywhere else). A close of cancel declines.
//
// In the interactive view the question is a box over the screen. Otherwise
// it is a line below the output; lane output is held while it waits, so it
// can't scroll the question away, and printed after the answer.
func (d *Display) Ask(question []string, cancel <-chan struct{}) bool {
	if d.in == nil {
		return false
	}
	if d.tui != nil && d.tuiActive() {
		return d.askTUI(question, cancel)
	}
	return d.askLine(question, cancel)
}

func (d *Display) tuiActive() bool {
	d.tui.ttyMu.Lock()
	defer d.tui.ttyMu.Unlock()
	return d.tui.active
}

func (d *Display) askTUI(question []string, cancel <-chan struct{}) bool {
	ch := make(chan bool, 1)
	d.mu.Lock()
	d.tui.model.ask = question
	d.tui.answer = ch
	d.tui.dirty = true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.tui.model.ask = nil
		d.tui.answer = nil
		d.tui.dirty = true
		d.mu.Unlock()
	}()
	select {
	case yes := <-ch:
		return yes
	case <-cancel:
		return false
	}
}

func (d *Display) askLine(question []string, cancel <-chan struct{}) bool {
	p := ui.Painter{On: d.color}
	d.mu.Lock()
	d.asking = true
	fmt.Fprint(d.out, "\n"+p.Paint(ui.Yellow+ui.Bold, strings.Join(question, "\n"))+" ")
	d.mu.Unlock()

	answer, interrupted := false, false
	fd := int(d.in.Fd())
	if st, err := term.MakeRaw(fd); err == nil {
		stop := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			readKeys(fd, stop, func(k Key) {
				switch {
				case k.Code == KeyRune && (k.Rune == 'y' || k.Rune == 'Y'):
					answer = true
				case k.Code == KeyCtrlC:
					interrupted = true
				case k.Code == KeyEnter || k.Code == KeyEsc || k.Code == KeyRune && (k.Rune == 'n' || k.Rune == 'N'):
				default:
					return
				}
				select {
				case <-stop:
				default:
					close(stop)
				}
			})
		}()
		go func() {
			select {
			case <-cancel:
				select {
				case <-stop:
				default:
					close(stop)
				}
			case <-done:
			}
		}()
		<-done
		term.Restore(fd, st)
	}

	d.mu.Lock()
	word := "no"
	if answer {
		word = "yes"
	}
	if interrupted {
		word = "no (Ctrl-C)"
	}
	select {
	case <-cancel:
		if !answer && !interrupted {
			word = "no (the run is stopping)"
		}
	default:
	}
	fmt.Fprintln(d.out, word)
	for _, l := range d.held {
		fmt.Fprintln(d.out, l)
	}
	d.held, d.asking = nil, false
	d.mu.Unlock()
	if interrupted && d.onInterrupt != nil {
		d.onInterrupt()
	}
	return answer
}
