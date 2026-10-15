package display

import (
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/gates-brightly/swimlane/internal/ui"
)

// Follow shows the end of the file at path under title, following it as
// it grows (tail -f), until Esc, q, Enter or Ctrl-C. done, if set, says the
// file is finished: the title then says so.
func Follow(in, out *os.File, title, path string, done func() bool) error {
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
	draw := func() {
		cols, rows, err := term.GetSize(int(out.Fd()))
		if err != nil || rows < 4 {
			cols, rows = 80, 24
		}
		data, _ := os.ReadFile(path)
		lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		if len(data) == 0 {
			lines = []string{"(no output yet)"}
		}
		if n := rows - 2; len(lines) > n {
			lines = lines[len(lines)-n:]
		}
		head := title
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
		b.WriteString(p.Paint(ui.Dim, clip("esc back", cols)))
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
