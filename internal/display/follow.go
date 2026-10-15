package display

import (
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/gates-brightly/swimlane/internal/ui"
)

// FollowView is one way Follow shows what it follows: its lines, reloaded
// on every redraw.
type FollowView struct {
	Name  string
	Lines func() []string
}

// FileLines reads path's lines for a FollowView.
func FileLines(path string) func() []string {
	return func() []string {
		data, _ := os.ReadFile(path)
		if len(data) == 0 {
			return nil
		}
		return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	}
}

// Follow shows the end of the first view's lines under title, following
// them as they grow (tail -f), until Esc, q, Enter or Ctrl-C; v switches
// to the next view (an overview, then the verbose output) and back. done,
// if set, says what it follows has finished: the title then says so.
func Follow(in, out *os.File, title string, views []FollowView, done func() bool) error {
	fd := int(in.Fd())
	st, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	fmt.Fprint(out, "\x1b[?1049h\x1b[?25l")
	defer func() {
		fmt.Fprint(out, "\x1b[?25h\x1b[?1049l")
		term.Restore(fd, st)
	}()
	stop := make(chan struct{})
	keyCh := make(chan Key, 16)
	reading := make(chan struct{})
	go func() {
		defer close(reading)
		readKeys(fd, stop, func(k Key) {
			select {
			case keyCh <- k:
			case <-stop:
			}
		})
	}()
	defer func() {
		close(stop)
		<-reading
	}()
	color := ui.ColorEnabled(out)
	p := ui.Painter{On: color}
	cur := 0
	draw := func() {
		cols, rows, err := term.GetSize(int(out.Fd()))
		if err != nil || rows < 4 {
			cols, rows = 80, 24
		}
		lines := views[cur].Lines()
		if len(lines) == 0 {
			lines = []string{"(no output yet)"}
		}
		if n := rows - 2; len(lines) > n {
			lines = lines[len(lines)-n:]
		}
		head := title
		if len(views) > 1 {
			head += " · " + views[cur].Name
		}
		if done != nil && done() {
			head += " · finished"
		}
		var b strings.Builder
		b.WriteString("\x1b[H\x1b[2J")
		b.WriteString(p.Paint(ui.Bold, clip(head, cols)) + "\r\n")
		for _, l := range lines {
			b.WriteString(clip(l, cols) + "\r\n")
		}
		b.WriteString(fmt.Sprintf("\x1b[%d;1H", rows))
		hints := "esc back"
		if len(views) > 1 {
			hints = "v " + views[(cur+1)%len(views)].Name + "  esc back"
		}
		b.WriteString(p.Paint(ui.Dim, clip(hints, cols)))
		fmt.Fprint(out, b.String())
	}
	draw()
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case k := <-keyCh:
			switch {
			case k.Code == KeyEsc, k.Code == KeyEnter, k.Code == KeyCtrlC, k.Code == KeyRune && (k.Rune == 'q' || k.Rune == 'Q'):
				return nil
			case k.Code == KeyRune && (k.Rune == 'v' || k.Rune == 'V') && len(views) > 1:
				cur = (cur + 1) % len(views)
				draw()
			}
		case <-tick.C:
			draw()
		}
	}
}

// clip cuts s to n columns (runes; lane output is mostly ASCII).
func clip(s string, n int) string {
	r := []rune(s)
	if n > 0 && len(r) > n {
		return string(r[:n])
	}
	return s
}
