package display

import (
	"bytes"
	"io"
	"os"
	"strings"

	"github.com/gates-brightly/swimlane/internal/logparse"
)

// tailWindow bounds how much of a log the lane view reads when it opens: the
// current round, or this many bytes of its end for an enormous round.
const tailWindow = 8 << 20

// logTail follows one lane log from the start of its current round, reading
// only what was added since the last poll.
type logTail struct {
	path    string
	off     int64 // bytes read so far
	started bool
	partial []byte // a line still being written
}

func newLogTail(path string) *logTail { return &logTail{path: path} }

// isRoundMark reports whether a log line starts a round (syntax 2, or 1).
func isRoundMark(l string) bool {
	return strings.HasPrefix(l, logparse.RoundMark) || strings.HasPrefix(l, "=== ROUND START")
}

// poll returns the complete lines added since the last poll. reset means
// the lines start a fresh view (first poll, a new round began, or the file
// was replaced) and earlier lines should be dropped.
func (t *logTail) poll() (lines []string, reset bool) {
	f, err := os.Open(t.path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, false
	}
	size := st.Size()
	if !t.started || size < t.off {
		t.off, t.partial, t.started, reset = roundStart(f, size), nil, true, true
	}
	if size <= t.off {
		return nil, reset
	}
	n := size - t.off
	if n > tailWindow {
		n = tailWindow
	}
	buf := make([]byte, n)
	got, _ := f.ReadAt(buf, t.off)
	t.off += int64(got)
	data := append(t.partial, buf[:got]...)
	t.partial = nil
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			t.partial = append([]byte(nil), data...)
			break
		}
		l := strings.TrimRight(string(data[:i]), "\r")
		data = data[i+1:]
		if isRoundMark(l) && (len(lines) > 0 || !reset) {
			lines, reset = lines[:0], true // a new round: show only it
		}
		lines = append(lines, l)
	}
	return lines, reset
}

// roundStart finds the offset of the last round mark in the file's last
// tailWindow bytes; without one it starts at the first line of that window.
func roundStart(f *os.File, size int64) int64 {
	from := size - tailWindow
	if from < 0 {
		from = 0
	}
	buf := make([]byte, size-from)
	n, err := f.ReadAt(buf, from)
	if err != nil && err != io.EOF {
		return size
	}
	buf = buf[:n]
	best := int64(-1)
	for _, mark := range []string{logparse.RoundMark, "=== ROUND START"} {
		if i := bytes.LastIndex(buf, []byte("\n"+mark)); i >= 0 && from+int64(i)+1 > best {
			best = from + int64(i) + 1
		}
		if from == 0 && bytes.HasPrefix(buf, []byte(mark)) && best < 0 {
			best = 0
		}
	}
	if best >= 0 {
		return best
	}
	if from == 0 {
		return 0
	}
	if i := bytes.IndexByte(buf, '\n'); i >= 0 {
		return from + int64(i) + 1
	}
	return size
}
