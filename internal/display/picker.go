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
}

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
	rows := load()
	draw := func() {
		cols, h, err := term.GetSize(int(p.Out.Fd()))
		if err != nil {
			cols, h = 80, 24
		}
		p.Sel = pickSel(rows, p.Sel, 0)
		fmt.Fprint(p.Out, "\x1b[H"+strings.Join(p.render(title, hints, rows, cols, h), "\x1b[K\r\n")+"\x1b[K\x1b[J")
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
	head := title
	if gap := cols - utf8.RuneCountInString(title) - utf8.RuneCountInString(hints); gap >= 2 {
		head = pt.Paint(ui.Bold, title) + strings.Repeat(" ", gap) + pt.Paint(ui.Dim, hints)
	} else {
		head = pt.Paint(ui.Bold, truncate(title, cols))
	}
	out := []string{head, ""}
	body := h - 3 // title, blank, note
	first := 0
	for i, r := range rows {
		if r.ID != "" && r.ID == p.Sel && i >= body {
			first = i - body + 1
		}
	}
	for i := first; i < len(rows) && len(out) < body+2; i++ {
		r := rows[i]
		mark := "  "
		if r.ID != "" && r.ID == p.Sel {
			mark = "▸ "
		}
		text := truncate(mark+r.Text, cols)
		switch {
		case r.ID != "" && r.ID == p.Sel && p.Color:
			out = append(out, "\x1b[7m"+pad(text, cols)+ui.Reset)
		case r.ID == "" && r.Color == "":
			out = append(out, pt.Paint(ui.Bold, text))
		default:
			out = append(out, pt.Paint(r.Color, text))
		}
	}
	for len(out) < body+2 {
		out = append(out, "")
	}
	return append(out, pt.Paint(ui.Yellow, truncate(p.Note, cols)))
}
