package display

import (
	"strings"
	"unicode/utf8"

	"github.com/gates-brightly/swimlane/internal/ui"
)

// Scrollback is how many lines the interactive view keeps for the combined
// output (and for one lane's log). Older lines are only in the logs.
const Scrollback = 20000

// vline is one logical line: a prefix repeated on every wrapped row ("[3] "
// in the all view) and the text.
type vline struct {
	prefix  string // may hold colour codes
	prefixW int    // its visible width
	text    string
}

// viewport is a scrollable window onto a growing list of lines. Following,
// it shows the newest lines; scrolled up it is paused and stays put while
// lines arrive, counting them. Positions are absolute line numbers, so
// dropping the oldest lines (over max) doesn't move a paused view.
type viewport struct {
	lines    []vline
	base     int // absolute number of lines[0]
	max      int
	top      int // absolute number of the first line shown, when paused
	follow   bool
	newLines int // arrived while paused
}

func newViewport(max int) *viewport { return &viewport{max: max, follow: true} }

// end is the absolute number one past the last line.
func (v *viewport) end() int { return v.base + len(v.lines) }

func (v *viewport) add(l vline) {
	v.lines = append(v.lines, l)
	if !v.follow {
		v.newLines++
	}
	// Drop in batches so adding stays cheap.
	if over := len(v.lines) - v.max; over > 0 && over >= v.max/8 {
		v.lines = append([]vline(nil), v.lines[over:]...)
		v.base += over
		if v.top < v.base {
			v.top = v.base
		}
	}
}

// clear empties the viewport and makes it follow.
func (v *viewport) clear() {
	v.lines, v.base, v.top, v.follow, v.newLines = nil, 0, 0, true, 0
}

// rows wraps line i (absolute) to width w.
func (v *viewport) rows(i, w int) []string {
	l := v.lines[i-v.base]
	parts := WrapANSI(l.text, w-l.prefixW)
	out := make([]string, len(parts))
	for j, p := range parts {
		out[j] = l.prefix + p
		if strings.Contains(p, "\x1b[") {
			out[j] += ui.Reset
		}
	}
	return out
}

// rowsFrom counts the rows from line i to the end, stopping once past h.
func (v *viewport) rowsFrom(i, h, w int) int {
	n := 0
	for ; i < v.end() && n <= h; i++ {
		n += len(v.rows(i, w))
	}
	return n
}

// firstShown is the first line (at least partly) on screen while following.
func (v *viewport) firstShown(h, w int) int {
	n, i := 0, v.end()
	for i > v.base && n < h {
		i--
		n += len(v.rows(i, w))
	}
	return i
}

// render returns exactly h rows of width w.
func (v *viewport) render(h, w int) []string {
	if h <= 0 {
		return nil
	}
	var out []string
	if v.follow {
		for i := v.end() - 1; i >= v.base && len(out) < h; i-- {
			r := v.rows(i, w)
			for j := len(r) - 1; j >= 0 && len(out) < h; j-- {
				out = append(out, r[j])
			}
		}
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	} else {
		for i := v.top; i < v.end() && len(out) < h; i++ {
			for _, r := range v.rows(i, w) {
				if len(out) < h {
					out = append(out, r)
				}
			}
		}
	}
	for len(out) < h {
		out = append(out, "")
	}
	return out
}

// up scrolls k lines towards the oldest. Following, it pauses first.
func (v *viewport) up(k, h, w int) {
	if v.follow {
		first := v.firstShown(h, w)
		if first == v.base && v.rowsFrom(v.base, h, w) <= h {
			return // everything fits: nothing to scroll
		}
		v.follow, v.newLines, v.top = false, 0, first
	}
	v.top -= k
	if v.top < v.base {
		v.top = v.base
	}
}

// down scrolls k lines towards the newest; reaching the bottom follows again.
func (v *viewport) down(k, h, w int) {
	if v.follow {
		return
	}
	v.top += k
	if v.top >= v.end() || v.rowsFrom(v.top, h, w) <= h {
		v.toEnd()
	}
}

// home shows the oldest line kept.
func (v *viewport) home(h, w int) {
	if v.rowsFrom(v.base, h, w) <= h {
		return
	}
	if v.follow {
		v.follow, v.newLines = false, 0
	}
	v.top = v.base
}

// toEnd jumps to the newest line and follows.
func (v *viewport) toEnd() { v.follow, v.newLines = true, 0 }

// textWidth is the visible width of s (ANSI codes excluded).
func textWidth(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i = escEnd(s, i)
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		n++
	}
	return n
}
