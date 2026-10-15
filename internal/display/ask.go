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

// Ask puts lane n's question to the operator and waits for the answer: y
// approves, n declines. A close of cancel declines (the run is stopping:
// Ctrl-C).
//
// In the interactive view several lanes may wait at once: a banner names
// them, Enter opens the one waiting longest, and on a waiting lane's view
// its question sits under its log, where y or n answers it. Otherwise
// questions are asked one at a time on a line below the output (Enter or
// Esc also declines there, Ctrl-C declines and interrupts); lane output is
// held while it waits, so it can't scroll the question away, and printed
// after the answer.
func (d *Display) Ask(n int, question []string, cancel <-chan struct{}) bool {
	if d.in == nil {
		return false
	}
	question = safeLines(question)
	if d.tui != nil && d.tuiActive() {
		return d.askTUI(n, question, cancel)
	}
	d.askMu.Lock()
	defer d.askMu.Unlock()
	return d.askLine(question, cancel)
}

func (d *Display) tuiActive() bool {
	d.tui.ttyMu.Lock()
	defer d.tui.ttyMu.Unlock()
	return d.tui.active
}

func (d *Display) askTUI(n int, question []string, cancel <-chan struct{}) bool {
	ch := make(chan bool, 1)
	d.mu.Lock()
	d.tui.model.addPending(n, question)
	if d.tui.answers == nil {
		d.tui.answers = map[int]chan bool{}
	}
	d.tui.answers[n] = ch
	d.tui.dirty = true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.tui.model.removePending(n)
		delete(d.tui.answers, n)
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
	// Raw mode first, then the question: a key typed the moment the
	// question shows is read as the answer, not echoed by the terminal (an
	// Enter would print a newline before the answer).
	fd := int(d.in.Fd())
	st, rawErr := term.MakeRaw(fd)
	text := "\n" + p.Paint(ui.Yellow+ui.Bold, strings.Join(question, "\n")) + " "
	if rawErr == nil {
		text = strings.ReplaceAll(text, "\n", "\r\n") // raw output doesn't return the carriage
	}
	d.mu.Lock()
	d.asking = true
	fmt.Fprint(d.out, text)
	d.mu.Unlock()

	answer, interrupted := false, false
	if rawErr == nil {
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

// safeLines makes each line of a question safe to show (ui.Safe): the
// lane wrote it, and must not be able to rewrite what the operator is
// asked to approve.
func safeLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = ui.Safe(l)
	}
	return out
}
