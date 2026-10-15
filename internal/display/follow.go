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

// FollowEnd is how what Follow shows ended, for the header it draws then:
// the result (passed, failed, interrupted, skipped, error), the exit code
// when known, how long it took, and details (failed steps, lanes skipped
// and why).
type FollowEnd struct {
	Result  string
	Exit    *int
	Elapsed time.Duration
	Details []string
}

// maxEndDetails is how many detail lines the header shows before "… N more".
const maxEndDetails = 5

// EndHeader is the header Follow draws once what it follows has ended: a
// full-width bar, green for passed, red for failed, yellow otherwise, then
// the details. Without color the bar is "== FAILED · exit 1 · 2:13 ==".
func EndHeader(e *FollowEnd, cols int, color bool) []string {
	if e == nil {
		return nil
	}
	word, bg := strings.ToUpper(e.Result), "\x1b[43;30m" // yellow
	switch e.Result {
	case "passed":
		word, bg = "✓ PASSED", "\x1b[42;30m"
	case "failed", "error":
		word, bg = "✗ "+strings.ToUpper(e.Result), "\x1b[41;97m"
	case "":
		word = "FINISHED"
	}
	parts := []string{word}
	if e.Exit != nil && (*e.Exit != 0 || e.Result != "passed") {
		parts = append(parts, fmt.Sprintf("exit %d", *e.Exit))
	}
	if e.Elapsed > 0 {
		parts = append(parts, Elapsed(e.Elapsed))
	}
	text := strings.Join(parts, " · ")
	var out []string
	if color {
		bar := " " + text
		if pad := cols - len([]rune(bar)); pad > 0 {
			bar += strings.Repeat(" ", pad)
		}
		out = append(out, bg+"\x1b[1m"+clip(bar, cols)+"\x1b[0m")
	} else {
		out = append(out, clip("== "+text+" ==", cols))
	}
	details := e.Details
	more := 0
	if len(details) > maxEndDetails {
		more = len(details) - maxEndDetails
		details = details[:maxEndDetails]
	}
	p := ui.Painter{On: color}
	for _, d := range details {
		c := ui.Yellow
		if e.Result == "failed" || e.Result == "error" {
			c = ui.Red
		}
		out = append(out, p.Paint(c, clip("  "+d, cols)))
	}
	if more > 0 {
		out = append(out, p.Paint(ui.Dim, clip(fmt.Sprintf("  … %d more", more), cols)))
	}
	return out
}

// Follow shows the end of the first view's lines under title, following
// them as they grow (tail -f), until Esc, q, Enter or Ctrl-C; v switches
// to the next view (an overview, then the verbose output) and back. end,
// if set, says how what it follows ended (nil while it runs): Follow then
// draws a header with the result under the title (EndHeader).
func Follow(in, out *os.File, title string, views []FollowView, end func() *FollowEnd) error {
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
		var header []string
		if end != nil {
			header = EndHeader(end(), cols, color)
		}
		lines := views[cur].Lines()
		if len(lines) == 0 {
			lines = []string{"(no output yet)"}
		}
		if n := rows - 2 - len(header); len(lines) > n {
			lines = lines[len(lines)-max(n, 0):]
		}
		head := title
		if len(views) > 1 {
			head += " · " + views[cur].Name
		}
		var b strings.Builder
		b.WriteString("\x1b[H\x1b[2J")
		b.WriteString(p.Paint(ui.Bold, clip(head, cols)) + "\r\n")
		for _, l := range header {
			b.WriteString(l + "\r\n")
		}
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
