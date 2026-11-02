package display

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/gates-brightly/swimlane/internal/ui"
)

// PickRow is one row of a Picker: an item the cursor can rest on (ID set)
// or a heading or note (ID "").
type PickRow struct {
	ID    string
	Text  string
	Color string // ui colour for the row ("" for none)
	// Styled, if set, is Text already coloured (SGR codes only), shown
	// instead of Text when colour is on and the row isn't selected: a row
	// in several colours, as swim run's panel draws a lane.
	Styled string
	// Rule draws the row as a section rule, as swim run's view divides its
	// panel from the output: "── Text ───── Right ──", dim, or in Color
	// when set (a section that needs the operator, in yellow).
	Rule  bool
	Right string // a Rule's note at its right end
}

// Picker is a full-screen list the keyboard moves through: swim watch's
// jobs and queue. ↑ ↓ (or k j, Home End) move between items; the caller's
// action keys pick one. The selection and note last across calls to Pick,
// so the screen comes back where the operator left it.
type Picker struct {
	In, Out *os.File
	Color   bool
	Sel     string // the selected item's ID
	Note    string // shown on the bottom line until the next key
	// Title, if set, is the title line on every redraw (a live count),
	// instead of Pick's title.
	Title func() string
	// Tail, if set, fills the screen under the rows on every redraw: live
	// lines (swim watch's running output), at most n of them. While it has
	// any it gets the room the rows leave, and never less than a third of
	// the screen; the rows scroll above it.
	Tail func(n int) []PickRow
	// Redraw, if set, reloads the rows and redraws this often, apart from
	// Pick's refresh (which then only paces until): live output as quick
	// as swim run's view, without checking until as often.
	Redraw time.Duration
	// Spin, if set, redraws this often without reloading, so a SpinMark in
	// the rows turns as swim run's throbber does whatever the reload pace.
	Spin time.Duration
	// HintsBelow keeps the hints on the bottom line (while there's no
	// note), leaving the title line to the title and Status.
	HintsBelow bool
	// Status, if set, is shown dim at the right of the title line on
	// every redraw: a live summary, as swim run's title carries its clock.
	Status func() string
}

// SpinMark in a row's text is drawn as the throbber's frame at the time of
// the draw: one column, as the frame is.
const SpinMark = "\uE000"

// Action keys that aren't printable: put these runes in Pick's keys to
// have Enter (on an item) and Esc (any time) end it.
const (
	PickEnter = '\r'
	PickEsc   = '\x1b'
)

// PickResult is how Pick ended.
type PickResult struct {
	ID   string // the item an action key was pressed on
	Key  rune   // the action key
	Quit bool   // Ctrl-C
}

// Pick shows the rows load returns (reloaded every refresh) under title
// and hints, until an action key in keys is pressed on an item, Ctrl-C is
// pressed, or until() is true (a zero PickResult).
func (p *Picker) Pick(title, hints, keys string, load func() []PickRow, refresh time.Duration, until func() bool) (PickResult, error) {
	fd := int(p.In.Fd())
	st, err := term.MakeRaw(fd)
	if err != nil {
		return PickResult{}, err
	}
	fmt.Fprint(p.Out, "\x1b[?1049h\x1b[?25l\x1b[2J")
	defer func() {
		fmt.Fprint(p.Out, "\x1b[?25h\x1b[?1049l")
		term.Restore(fd, st)
	}()
	stop := make(chan struct{})
	keyCh := make(chan Key, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		readKeys(fd, stop, func(k Key) {
			select {
			case keyCh <- k:
			case <-stop:
			}
		})
	}()
	defer func() {
		close(stop)
		<-done
	}()
	tick := time.NewTicker(refresh)
	defer tick.Stop()
	var redraw <-chan time.Time // nil: the rows reload with refresh
	if p.Redraw > 0 && p.Redraw < refresh {
		t := time.NewTicker(p.Redraw)
		defer t.Stop()
		redraw = t.C
	}
	var spin <-chan time.Time // nil: SpinMark turns only on a reload
	if p.Spin > 0 {
		t := time.NewTicker(p.Spin)
		defer t.Stop()
		spin = t.C
	}
	rows := load()
	last := ""
	draw := func() {
		cols, h, err := term.GetSize(int(p.Out.Fd()))
		if err != nil {
			cols, h = 80, 24
		}
		p.Sel = pickSel(rows, p.Sel, 0)
		t := title
		if p.Title != nil {
			t = p.Title()
		}
		frame := "\x1b[H" + strings.Join(p.render(t, hints, rows, cols, h), "\x1b[K\r\n") + "\x1b[K\x1b[J"
		frame = strings.ReplaceAll(frame, SpinMark, Throbber(time.Now()))
		if frame == last {
			return // nothing changed: the terminal keeps what it shows
		}
		last = frame
		fmt.Fprint(p.Out, frame)
	}
	draw()
	for {
		select {
		case <-tick.C:
			if until != nil && until() {
				return PickResult{}, nil
			}
			rows = load()
			draw()
		case <-redraw:
			rows = load()
			draw()
		case <-spin:
			draw() // only the throbber (and clocks) moved
		case k := <-keyCh:
			if res, ok := p.key(k, rows, keys); ok {
				return res, nil
			}
			draw()
		}
	}
}

// key applies one key press: it moves the selection, or ends Pick.
func (p *Picker) key(k Key, rows []PickRow, keys string) (PickResult, bool) {
	p.Note = ""
	switch {
	case k.Code == KeyCtrlC:
		return PickResult{Quit: true}, true
	case k.Code == KeyUp || k.Code == KeyRune && k.Rune == 'k':
		p.Sel = pickSel(rows, p.Sel, -1)
	case k.Code == KeyDown || k.Code == KeyRune && k.Rune == 'j':
		p.Sel = pickSel(rows, p.Sel, 1)
	case k.Code == KeyHome:
		p.Sel = pickSel(rows, "", 0)
	case k.Code == KeyEnd:
		for i := len(rows) - 1; i >= 0; i-- {
			if rows[i].ID != "" {
				p.Sel = rows[i].ID
				break
			}
		}
	case k.Code == KeyEsc && strings.ContainsRune(keys, PickEsc):
		return PickResult{ID: p.Sel, Key: PickEsc}, true
	case k.Code == KeyEnter && strings.ContainsRune(keys, PickEnter):
		if p.Sel == "" {
			p.Note = "nothing selected"
			return PickResult{}, false
		}
		return PickResult{ID: p.Sel, Key: PickEnter}, true
	case k.Code == KeyRune && strings.ContainsRune(keys, k.Rune):
		if p.Sel == "" {
			p.Note = "nothing selected"
			return PickResult{}, false
		}
		return PickResult{ID: p.Sel, Key: k.Rune}, true
	}
	return PickResult{}, false
}

// pickSel is the item d items away from sel (sel itself for 0, or the first
// item when sel is gone), staying on the ends.
func pickSel(rows []PickRow, sel string, d int) string {
	var ids []string
	cur := -1
	for _, r := range rows {
		if r.ID == "" {
			continue
		}
		if r.ID == sel {
			cur = len(ids)
		}
		ids = append(ids, r.ID)
	}
	if len(ids) == 0 {
		return ""
	}
	if cur < 0 {
		return ids[0]
	}
	cur += d
	if cur < 0 {
		cur = 0
	}
	if cur >= len(ids) {
		cur = len(ids) - 1
	}
	return ids[cur]
}

// render is the screen: the title and hints, the rows (scrolled to keep
// the selection in view), and the note at the bottom.
func (p *Picker) render(title, hints string, rows []PickRow, cols, h int) []string {
	pt := ui.Painter{On: p.Color}
	if cols < 20 {
		cols = 20
	}
	if h < 5 {
		h = 5
	}
	right := hints
	if p.HintsBelow {
		right = ""
		if p.Status != nil {
			right = p.Status()
		}
	}
	head := pt.Paint(ui.Bold, truncate(title, cols))
	gap := cols - utf8.RuneCountInString(title) - utf8.RuneCountInString(right)
	if gap >= 2 {
		head = pt.Paint(ui.Bold, title) + strings.Repeat(" ", gap) + pt.Paint(ui.Dim, right)
	}
	bottom := pt.Paint(ui.Yellow, truncate(p.Note, cols))
	if p.Note == "" && (p.HintsBelow || gap < 2) {
		// The hints' place is the bottom line, or there's no room beside
		// the title: there, while there's no note.
		bottom = pt.Paint(ui.Dim, truncate(hints, cols))
	}
	out := []string{head, ""}
	body := h - 3 // title, blank, note
	var tail []PickRow
	if p.Tail != nil {
		room := max(body-len(rows), body/3) - 1 // a blank line above it
		if room >= 2 {
			if tail = p.Tail(room); len(tail) > room {
				tail = tail[len(tail)-room:]
			}
		}
		if len(tail) > 0 {
			body -= len(tail) + 1
		}
	}
	first := 0
	for i, r := range rows {
		if r.ID != "" && r.ID == p.Sel && i >= body {
			first = i - body + 1
		}
	}
	for i := first; i < len(rows) && len(out) < body+2; i++ {
		r := rows[i]
		sel := r.ID != "" && r.ID == p.Sel
		mark := "  "
		if sel {
			mark = "▸ "
		}
		switch {
		case r.Rule:
			out = append(out, p.rule(r, cols))
		case sel && p.Color:
			out = append(out, "\x1b[7m"+pad(truncate(mark+r.Text, cols), cols)+ui.Reset)
		case r.Styled != "" && p.Color:
			out = append(out, mark+truncateANSI(r.Styled, cols-2)+ui.Reset)
		case r.ID == "" && r.Color == "":
			out = append(out, pt.Paint(ui.Bold, truncate(mark+r.Text, cols)))
		default:
			out = append(out, pt.Paint(r.Color, truncate(mark+r.Text, cols)))
		}
	}
	for len(out) < body+2 {
		out = append(out, "")
	}
	if len(tail) > 0 {
		out = append(out, "")
		for _, r := range tail {
			switch {
			case r.Rule:
				out = append(out, p.rule(r, cols))
			case r.Styled != "" && p.Color:
				out = append(out, "  "+truncateANSI(r.Styled, cols-2)+ui.Reset)
			case r.Color == "":
				out = append(out, truncate("  "+r.Text, cols))
			default:
				out = append(out, pt.Paint(r.Color, truncate("  "+r.Text, cols)))
			}
		}
	}
	return append(out, bottom)
}

// rule is a section rule cols wide: "── Jobs ───────── 2 running ──", the
// line dim, the label bold.
func (p *Picker) rule(r PickRow, cols int) string {
	pt := ui.Painter{On: p.Color}
	label := truncate(r.Text, max(cols-6, 1))
	right := ""
	if r.Right != "" {
		right = " " + r.Right + " ──"
	}
	fill := cols - 3 - utf8.RuneCountInString(label) - 1 - utf8.RuneCountInString(right)
	if fill < 2 {
		right, fill = "", cols-3-utf8.RuneCountInString(label)-1
	}
	if fill < 0 {
		fill = 0
	}
	line, text := ui.Dim, ui.Bold
	if r.Color != "" {
		line, text = r.Color, r.Color+ui.Bold
	}
	return pt.Paint(line, "── ") + pt.Paint(text, label) + " " + pt.Paint(line, strings.Repeat("─", fill)+right)
}

// truncateANSI cuts s, coloured with SGR codes, to w visible columns,
// marking the cut with "…"; the codes take no room.
func truncateANSI(s string, w int) string {
	if textWidth(s) <= w {
		return s
	}
	if w <= 0 {
		return ""
	}
	var b strings.Builder
	n := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := ui.EscEnd(s, i)
			b.WriteString(s[i:j])
			i = j
			continue
		}
		if n == w-1 {
			b.WriteString("…")
			break
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		b.WriteString(s[i : i+size])
		i += size
		n++
	}
	return b.String()
}
