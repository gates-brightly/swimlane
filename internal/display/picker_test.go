package display

import (
	"strings"
	"testing"
)

func pickRows() []PickRow {
	return []PickRow{
		{Text: "Jobs"},
		{ID: "lane:1", Text: "swim 1  first"},
		{ID: "lane:2", Text: "swim 2  second"},
		{Text: "Queue"},
		{ID: "queue:a", Text: "#1 queued"},
	}
}

func TestPickerMoves(t *testing.T) {
	p := &Picker{}
	rows := pickRows()
	p.Sel = pickSel(rows, "", 0)
	if p.Sel != "lane:1" {
		t.Fatalf("first item: %q", p.Sel)
	}
	steps := []struct {
		k    Key
		want string
	}{
		{Key{Code: KeyUp}, "lane:1"}, // stays at the top
		{Key{Code: KeyDown}, "lane:2"},
		{Key{Code: KeyDown}, "queue:a"}, // headings are skipped
		{Key{Code: KeyDown}, "queue:a"}, // and at the bottom
		{Key{Code: KeyRune, Rune: 'k'}, "lane:2"},
		{Key{Code: KeyHome}, "lane:1"},
		{Key{Code: KeyEnd}, "queue:a"},
		{Key{Code: KeyRune, Rune: 'j'}, "queue:a"},
	}
	for i, s := range steps {
		if _, done := p.key(s.k, rows, "rq"); done {
			t.Fatalf("step %d ended the pick", i)
		}
		if p.Sel != s.want {
			t.Fatalf("step %d: selected %q, want %q", i, p.Sel, s.want)
		}
	}
	res, done := p.key(Key{Code: KeyRune, Rune: 'r'}, rows, "rq")
	if !done || res.ID != "queue:a" || res.Key != 'r' {
		t.Fatalf("r: %+v %v", res, done)
	}
	if _, done := p.key(Key{Code: KeyRune, Rune: 'x'}, rows, "rq"); done {
		t.Error("x isn't an action key")
	}
	if res, done := p.key(Key{Code: KeyCtrlC}, rows, "rq"); !done || !res.Quit {
		t.Errorf("ctrl-c: %+v %v", res, done)
	}
	// A selection that disappears falls back to the first item.
	if got := pickSel(rows[:3], "queue:a", 0); got != "lane:1" {
		t.Errorf("gone: %q", got)
	}
	empty := &Picker{}
	if _, done := empty.key(Key{Code: KeyRune, Rune: 'r'}, []PickRow{{Text: "Jobs"}}, "rq"); done || empty.Note == "" {
		t.Errorf("r with nothing selected: done=%v note=%q", done, empty.Note)
	}
}

func TestPickerRender(t *testing.T) {
	p := &Picker{Sel: "lane:2", Note: "queued swim 2"}
	out := p.render("swim watch", "↑ ↓ pick", pickRows(), 40, 10)
	if len(out) != 10 {
		t.Fatalf("%d lines, want 10:\n%s", len(out), strings.Join(out, "\n"))
	}
	if !strings.HasPrefix(out[0], "swim watch") || !strings.HasSuffix(out[0], "↑ ↓ pick") {
		t.Errorf("title: %q", out[0])
	}
	if out[3] != "  swim 1  first" || out[4] != "▸ swim 2  second" {
		t.Errorf("rows:\n%s", strings.Join(out, "\n"))
	}
	if out[9] != "queued swim 2" {
		t.Errorf("note: %q", out[9])
	}
	// Scrolled to keep the selection on a short screen.
	p = &Picker{Sel: "queue:a"}
	out = p.render("t", "", pickRows(), 40, 5)
	if !strings.Contains(strings.Join(out, "\n"), "▸ #1 queued") {
		t.Errorf("selection scrolled away:\n%s", strings.Join(out, "\n"))
	}
	// Long rows are cut to the width.
	p = &Picker{Sel: "x"}
	out = p.render("t", "", []PickRow{{ID: "x", Text: strings.Repeat("a", 100)}}, 30, 6)
	if n := len([]rune(out[2])); n != 30 {
		t.Errorf("row is %d runes wide, want 30", n)
	}
}

func TestPickerEnterEsc(t *testing.T) {
	p := &Picker{Sel: "lane:2"}
	if res, done := p.key(Key{Code: KeyEnter}, pickRows(), "q"); done {
		t.Errorf("enter isn't an action unless asked: %+v", res)
	}
	if res, done := p.key(Key{Code: KeyEnter}, pickRows(), "q\r\x1b"); !done || res.Key != PickEnter || res.ID != "lane:2" {
		t.Errorf("enter: %+v %v", res, done)
	}
	if res, done := (&Picker{}).key(Key{Code: KeyEsc}, nil, "\x1b"); !done || res.Key != PickEsc {
		t.Errorf("esc with nothing selected: %+v %v", res, done)
	}
}
