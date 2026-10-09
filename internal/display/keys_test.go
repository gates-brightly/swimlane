package display

import (
	"reflect"
	"testing"
)

func codes(keys []Key) []KeyCode {
	out := []KeyCode{}
	for _, k := range keys {
		out = append(out, k.Code)
	}
	return out
}

func TestKeyParserSequences(t *testing.T) {
	for _, c := range []struct {
		name string
		in   string
		want []KeyCode
	}{
		{"xterm arrows", "\x1b[A\x1b[B\x1b[C\x1b[D", []KeyCode{KeyUp, KeyDown, KeyRight, KeyLeft}},
		{"application-mode arrows (tmux, Terminal.app)", "\x1bOA\x1bOB\x1bOC\x1bOD", []KeyCode{KeyUp, KeyDown, KeyRight, KeyLeft}},
		{"modified arrows (iTerm2 Shift/Ctrl)", "\x1b[1;2A\x1b[1;5D", []KeyCode{KeyUp, KeyLeft}},
		{"page keys", "\x1b[5~\x1b[6~", []KeyCode{KeyPgUp, KeyPgDn}},
		{"home/end xterm", "\x1b[H\x1b[F", []KeyCode{KeyHome, KeyEnd}},
		{"home/end SS3", "\x1bOH\x1bOF", []KeyCode{KeyHome, KeyEnd}},
		{"home/end vt (tmux, Linux console)", "\x1b[1~\x1b[4~", []KeyCode{KeyHome, KeyEnd}},
		{"home/end rxvt", "\x1b[7~\x1b[8~", []KeyCode{KeyHome, KeyEnd}},
		{"shift-tab", "\x1b[Z", []KeyCode{KeyBackTab}},
		{"controls", "\t\r\n\x7f\x08\x03", []KeyCode{KeyTab, KeyEnter, KeyEnter, KeyBackspace, KeyBackspace, KeyCtrlC}},
		{"unknown CSI is ignored", "\x1b[2~\x1b[200~x", []KeyCode{KeyRune}},
		{"esc esc arrow", "\x1b\x1b[A", []KeyCode{KeyEsc, KeyUp}},
		{"alt-x", "\x1bx", []KeyCode{KeyEsc, KeyRune}},
	} {
		var p keyParser
		got := codes(p.feed([]byte(c.in)))
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
		if p.pending() {
			t.Errorf("%s: bytes left over", c.name)
		}
	}
}

func TestKeyParserRunes(t *testing.T) {
	var p keyParser
	keys := p.feed([]byte("j4é"))
	if len(keys) != 3 || keys[0].Rune != 'j' || keys[1].Rune != '4' || keys[2].Rune != 'é' {
		t.Fatalf("got %+v", keys)
	}
	// A rune split across reads.
	b := []byte("é")
	if keys := p.feed(b[:1]); len(keys) != 0 {
		t.Fatalf("half a rune gave %+v", keys)
	}
	if keys := p.feed(b[1:]); len(keys) != 1 || keys[0].Rune != 'é' {
		t.Fatalf("got %+v", keys)
	}
}

// A lone ESC waits (for escWait) in case it starts a sequence; a sequence
// split across reads still parses; flush makes a lone ESC the Esc key.
func TestKeyParserEscSplit(t *testing.T) {
	var p keyParser
	if keys := p.feed([]byte("\x1b")); len(keys) != 0 || !p.pending() {
		t.Fatalf("lone ESC: %v pending=%v", keys, p.pending())
	}
	if got := codes(p.feed([]byte("[C"))); !reflect.DeepEqual(got, []KeyCode{KeyRight}) {
		t.Fatalf("split arrow: %v", got)
	}
	p.feed([]byte("\x1b["))
	if got := codes(p.feed([]byte("5"))); len(got) != 0 {
		t.Fatalf("partial PgUp: %v", got)
	}
	if got := codes(p.feed([]byte("~"))); !reflect.DeepEqual(got, []KeyCode{KeyPgUp}) {
		t.Fatalf("split PgUp: %v", got)
	}

	p.feed([]byte("\x1b"))
	if got := codes(p.flush()); !reflect.DeepEqual(got, []KeyCode{KeyEsc}) || p.pending() {
		t.Fatalf("flushed ESC: %v", got)
	}
	// ESC then '[' and nothing more: Esc, then '[' as a key.
	p.feed([]byte("\x1b["))
	keys := p.flush()
	if len(keys) != 2 || keys[0].Code != KeyEsc || keys[1].Rune != '[' {
		t.Fatalf("flushed ESC [: %+v", keys)
	}
}
