package notify

import (
	"bytes"
	"testing"
)

func TestNotify(t *testing.T) {
	var b bytes.Buffer
	n := New(&b)
	n.Notify("swim: #12 passed", "swim 1,2 · 4m12s")
	if got, want := b.String(), "\x1b]777;notify;swim: #12 passed;swim 1,2 · 4m12s\x07"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// A lane's text can't end the sequence or start another; the title
	// can't add a field.
	b.Reset()
	n.Notify("a;b", "x\x07\x1b]777;notify;forged;y\nz")
	if got := b.String(); got != "\x1b]777;notify;a,b;x\x07" { // the forged sequence is dropped whole
		t.Errorf("hostile text: %q", got)
	}
	var none *Terminal
	none.Notify("t", "b") // a nil Terminal sends nothing
	none.SetProgress(Progress{State: Normal, Percent: 5})
	none.Done()
}

func TestProgress(t *testing.T) {
	var b bytes.Buffer
	n := New(&b)
	n.SetProgress(Of(1, 4, Normal))
	n.SetProgress(Of(1, 4, Normal)) // unchanged: not sent again
	n.SetProgress(Of(0, 4, Normal))
	n.SetProgress(Of(2, 4, Paused))
	n.SetProgress(Of(3, 4, Error))
	n.SetProgress(Progress{State: Normal, Percent: 250})
	n.Done()
	want := "\x1b]9;4;1;25\x07" + "\x1b]9;4;3\x07" + "\x1b]9;4;4;50\x07" + "\x1b]9;4;2;75\x07" + "\x1b]9;4;1;100\x07" + "\x1b]9;4;0\x07"
	if got := b.String(); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if p := Of(0, 0, Normal); p.State != Hidden {
		t.Errorf("nothing to do: %+v", p)
	}
}
