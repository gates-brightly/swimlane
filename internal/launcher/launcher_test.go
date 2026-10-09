package launcher

import (
	"reflect"
	"testing"
)

func TestLineWriterSplitsPartialLines(t *testing.T) {
	var got []string
	w := &lineWriter{emit: func(s string) { got = append(got, s) }}
	for _, chunk := range []string{"hel", "lo\nwor", "ld\r\n", "\n", "tail"} {
		w.Write([]byte(chunk))
	}
	if want := []string{"hello", "world", ""}; !reflect.DeepEqual(got, want) {
		t.Fatalf("before flush: %q", got)
	}
	w.Flush()
	if want := []string{"hello", "world", "", "tail"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after flush: %q", got)
	}
}
