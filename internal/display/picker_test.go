package display

import (
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/ui"
)

func pickRows() []PickRow {
	return []PickRow{
		{Text: "Jobs"},
		{ID: "lane:1", Text: "lane 1  first"},
		{ID: "lane:2", Text: "lane 2  second"},
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
	p := &Picker{Sel: "lane:2", Note: "queued lane 2"}
	out := p.render("swim watch", "↑ ↓ pick", pickRows(), 40, 10)
	if len(out) != 10 {
		t.Fatalf("%d lines, want 10:\n%s", len(out), strings.Join(out, "\n"))
	}
	if !strings.HasPrefix(out[0], "swim watch") || !strings.HasSuffix(out[0], "↑ ↓ pick") {
		t.Errorf("title: %q", out[0])
	}
	if out[3] != "  lane 1  first" || out[4] != "▸ lane 2  second" {
		t.Errorf("rows:\n%s", strings.Join(out, "\n"))
	}
	if out[9] != "queued lane 2" {
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

func TestPickerHintsMoveDown(t *testing.T) {
	p := &Picker{Sel: "lane:1"}
	out := p.render("a long title that fills the line", "↑ ↓ pick  r run", pickRows(), 36, 8)
	if !strings.HasPrefix(out[0], "a long title") || out[7] != "↑ ↓ pick  r run" {
		t.Errorf("hints should move to the bottom:\n%s", strings.Join(out, "\n"))
	}
	p.Note = "a note wins"
	if out := p.render("a long title that fills the line", "↑ ↓ pick  r run", pickRows(), 36, 8); out[7] != "a note wins" {
		t.Errorf("the note should show: %q", out[7])
	}
}

func TestPickerTail(t *testing.T) {
	var asked int
	p := &Picker{Tail: func(n int) []PickRow {
		asked = n
		var out []PickRow
		for i := 0; i < 50; i++ {
			out = append(out, PickRow{Text: "out " + string(rune('a'+i%26))})
		}
		return out
	}}
	p.Sel = "lane:1"
	// Room to spare: the tail gets what the rows leave.
	out := p.render("t", "", pickRows(), 40, 20)
	if len(out) != 20 || asked != 20-3-len(pickRows())-1 || !strings.Contains(out[len(out)-2], "out") || out[2+len(pickRows())] != "" {
		t.Fatalf("asked %d:\n%s", asked, strings.Join(out, "\n"))
	}
	// Many rows: the tail still gets a third, and the selection stays in view.
	var many []PickRow
	for i := 0; i < 40; i++ {
		many = append(many, PickRow{ID: "lane:" + string(rune('A'+i)), Text: "swim " + string(rune('A'+i))})
	}
	p.Sel = many[39].ID
	out = p.render("t", "", many, 40, 15)
	if len(out) != 15 || asked != 12/3-1 {
		t.Fatalf("asked %d, %d lines", asked, len(out))
	}
	if !strings.Contains(strings.Join(out, "\n"), "▸ swim "+string(rune('A'+39))) {
		t.Errorf("selection scrolled out:\n%s", strings.Join(out, "\n"))
	}
	// Nothing to show: the rows keep the whole screen.
	p.Tail = func(int) []PickRow { return nil }
	if out := p.render("t", "", pickRows(), 40, 10); strings.Contains(strings.Join(out, ""), "out") || len(out) != 10 {
		t.Errorf("empty tail:\n%s", strings.Join(out, "\n"))
	}
}

// Swim watch's look, as swim run's: section rules, rows in several colours
// (cut to the width without counting their codes), the title's live status
// at the right and the keys on the bottom line until a note takes it.
func TestPickerStyled(t *testing.T) {
	p := &Picker{Color: true, Sel: "lane:2", HintsBelow: true, Status: func() string { return "2 running · 0:42" }}
	styled := "\x1b[36m\x1b[1mswim 1\x1b[0m  \x1b[36mrunning\x1b[0m " + strings.Repeat("x", 80)
	rows := []PickRow{
		{Text: "Jobs", Rule: true, Right: "o hides"},
		{ID: "lane:1", Text: "lane 1  running " + strings.Repeat("x", 80), Styled: styled},
		{ID: "lane:2", Text: "lane 2  passed", Styled: "\x1b[35mswim 2\x1b[0m  passed"},
	}
	out := p.render("swim watch · repo", "↑ ↓ pick  r run", rows, 40, 8)
	plain := func(s string) string {
		var b strings.Builder
		for i := 0; i < len(s); {
			if s[i] == 0x1b {
				i = ui.EscEnd(s, i)
				continue
			}
			b.WriteByte(s[i])
			i++
		}
		return b.String()
	}
	if got := plain(out[0]); !strings.HasPrefix(got, "swim watch · repo") || !strings.HasSuffix(got, "2 running · 0:42") {
		t.Errorf("title: %q", got)
	}
	if got := plain(out[2]); !strings.HasPrefix(got, "── Jobs ─") || !strings.HasSuffix(got, " o hides ──") || len([]rune(got)) != 40 {
		t.Errorf("rule: %q (%d wide)", got, len([]rune(got)))
	}
	if !strings.Contains(out[3], "\x1b[36m\x1b[1mswim 1") || textWidth(out[3]) != 40 || !strings.HasSuffix(plain(out[3]), "…") {
		t.Errorf("styled row: %q (%d wide)", out[3], textWidth(out[3]))
	}
	if !strings.HasPrefix(out[4], "\x1b[7m▸ lane 2  passed") {
		t.Errorf("the selected row is drawn plain, reversed: %q", out[4])
	}
	if got := plain(out[7]); got != "↑ ↓ pick  r run" {
		t.Errorf("bottom line: %q", got)
	}
	p.Note = "queued lane 2"
	if got := plain(p.render("t", "↑ ↓ pick", rows, 40, 8)[7]); got != "queued lane 2" {
		t.Errorf("a note takes the bottom line: %q", got)
	}
}

func TestColorLaneLogLine(t *testing.T) {
	stage := "-- stage change  12:00:01"
	if got, want := ColorLaneLogLine(stage, 3, true), (ui.Painter{On: true}).Paint(ui.LaneColor(3)+ui.Bold, stage); got != want {
		t.Errorf("stage line: %q, want %q", got, want)
	}
	if got := ColorLaneLogLine(stage, 3, false); got != stage {
		t.Errorf("without colour: %q", got)
	}
	if got, want := ColorLaneLogLine("  PASS  build", 3, true), colorLogLine("  PASS  build", true); got != want {
		t.Errorf("other lines are coloured as swim log colours them: %q", got)
	}
}

// The follow views read lane output back as text: each line is coloured
// as swim run's live view shows it.
func TestStyleOutputLine(t *testing.T) {
	p := ui.Painter{On: true}
	for line, want := range map[string]string{
		"[2] ==> migrate":               JobPrefix(2, "2", true) + p.Paint(ui.LaneColor(2)+ui.Bold, "==> migrate"),
		"[2]   ==> hold (running)":      JobPrefix(2, "2", true) + "  " + p.Paint(ui.LaneColor(2)+ui.Bold, "==> hold (running)"),
		"[3] $ go test ./...":           JobPrefix(3, "3", true) + p.Paint(ui.Dim, "$ go test ./..."),
		"[3] PASS  build  0.4s":         JobPrefix(3, "3", true) + p.Paint(ui.StateColor("PASS")+ui.Bold, "PASS") + "  build  0.4s",
		"[1] -- stage change  12:00:01": JobPrefix(1, "1", true) + p.Paint(ui.LaneColor(1)+ui.Bold, "-- stage change  12:00:01"),
		"[1]   FAIL  check (exit 1)":    JobPrefix(1, "1", true) + colorLogLine("  FAIL  check (exit 1)", true),
		"[1] plain output":              JobPrefix(1, "1", true) + "plain output",
		"swim watch: a note":            "swim watch: a note",
	} {
		if got := StyleOutputLine(line); got != want {
			t.Errorf("%q:\n got %q\nwant %q", line, got, want)
		}
	}
	// Already coloured: as it is. Other escapes are removed.
	if got := StyleOutputLine("\x1b[32mok\x1b[0m"); got != "\x1b[32mok\x1b[0m" {
		t.Errorf("coloured line: %q", got)
	}
	if got := StyleOutputLine("[1] a\x1b[2Jb"); strings.Contains(got, "\x1b[2J") {
		t.Errorf("a screen clear got through: %q", got)
	}
}

// A rule with a colour is drawn in it, line and label: a section that needs
// the operator (swim watch's questions) in yellow.
func TestPickerColouredRule(t *testing.T) {
	p := &Picker{Color: true}
	got := p.rule(PickRow{Text: "⚠ Waiting for you (2)", Rule: true, Color: ui.Yellow, Right: "y approve"}, 50)
	pt := ui.Painter{On: true}
	if !strings.HasPrefix(got, pt.Paint(ui.Yellow, "── ")+pt.Paint(ui.Yellow+ui.Bold, "⚠ Waiting for you (2)")) || strings.Contains(got, ui.Dim) {
		t.Errorf("yellow rule: %q", got)
	}
	if textWidth(got) != 50 || !strings.Contains(got, ui.Yellow+"──") || !strings.HasSuffix(got, " y approve ──"+ui.Reset) {
		t.Errorf("yellow rule's fill: %q (%d wide)", got, textWidth(got))
	}
	if plain := p.rule(PickRow{Text: "Jobs", Rule: true}, 20); !strings.HasPrefix(plain, pt.Paint(ui.Dim, "── ")) {
		t.Errorf("a rule without a colour stays dim: %q", plain)
	}
}

func TestPickerField(t *testing.T) {
	rows := pickRows()
	p := &Picker{Sel: "lane:2", Field: &PickField{Prompt: "grace: ", Text: "5s", Hint: "Esc cancels"}}
	for _, k := range []Key{{Code: KeyBackspace}, {Code: KeyBackspace}, {Code: KeyRune, Rune: 'j'}, {Code: KeyRune, Rune: '7'}} {
		if _, done := p.key(k, rows, "q"); done {
			t.Fatalf("%+v ended Pick", k)
		}
	}
	if p.Sel != "lane:2" || p.Field.Text != "j7" {
		t.Errorf("j moved or wasn't typed: sel %q, text %q", p.Sel, p.Field.Text)
	}
	if got := p.render("t", "h", rows, 40, 10)[9]; got != "grace: j7_   Esc cancels" {
		t.Errorf("field line: %q", got)
	}
	if res, done := p.key(Key{Code: KeyEnter}, rows, "q"); !done || res.Key != PickEnter || res.Text != "j7" || res.ID != "lane:2" {
		t.Errorf("enter: %+v %v", res, done)
	}
	if res, done := p.key(Key{Code: KeyEsc}, rows, "q"); !done || res.Key != PickEsc {
		t.Errorf("esc: %+v %v", res, done)
	}
	p.Field = nil
	if res, done := p.key(Key{Code: KeyLeft}, rows, string(PickLeft)); !done || res.Key != PickLeft {
		t.Errorf("←: %+v %v", res, done)
	}
	if _, done := p.key(Key{Code: KeyRight}, rows, string(PickLeft)); done {
		t.Error("→ ended Pick without being an action key")
	}
}
