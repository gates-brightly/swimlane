package display

import (
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

// Key is one key press read from the terminal in raw mode.
type Key struct {
	Code KeyCode
	Rune rune // for KeyRune
}

// KeyCode names the keys the interactive view understands.
type KeyCode int

const (
	KeyRune KeyCode = iota // a printable character, in Rune
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyPgUp
	KeyPgDn
	KeyHome
	KeyEnd
	KeyEnter
	KeyEsc
	KeyTab
	KeyBackTab // Shift-Tab
	KeyBackspace
	KeyCtrlC
)

// escWait is how long a lone ESC waits for the rest of a sequence (ESC [ A)
// before it counts as the Esc key.
const escWait = 30 * time.Millisecond

// keyParser turns raw terminal bytes into keys. Sequences may arrive split
// across reads: an incomplete one stays buffered until more bytes come, or
// until flush (called when escWait passes with nothing more).
type keyParser struct {
	buf []byte
}

// pending reports whether bytes are buffered waiting for the rest of a
// sequence.
func (p *keyParser) pending() bool { return len(p.buf) > 0 }

// feed adds bytes and returns the complete keys they finish.
func (p *keyParser) feed(b []byte) []Key {
	p.buf = append(p.buf, b...)
	var keys []Key
	for len(p.buf) > 0 {
		k, n := parseKey(p.buf, false)
		if n == 0 {
			break // incomplete: wait for more
		}
		p.buf = p.buf[n:]
		if k != nil {
			keys = append(keys, *k)
		}
	}
	return keys
}

// flush gives up waiting: a buffered ESC is the Esc key, and whatever
// follows it is read as ordinary keys.
func (p *keyParser) flush() []Key {
	var keys []Key
	for len(p.buf) > 0 {
		k, n := parseKey(p.buf, true)
		if n == 0 {
			n = 1
		}
		p.buf = p.buf[n:]
		if k != nil {
			keys = append(keys, *k)
		}
	}
	return keys
}

// parseKey reads one key from the front of b and returns it with the bytes
// it used. n == 0 means b holds only the start of a key; with final set it
// never returns 0. A nil key with n > 0 is a sequence swim ignores.
func parseKey(b []byte, final bool) (*Key, int) {
	key := func(c KeyCode) *Key { return &Key{Code: c} }
	switch c := b[0]; {
	case c == 0x1b:
		if len(b) == 1 {
			if final {
				return key(KeyEsc), 1
			}
			return nil, 0
		}
		switch b[1] {
		case '[':
			return parseCSI(b, final)
		case 'O': // SS3: application-mode arrows and Home/End
			if len(b) == 2 {
				if final {
					return key(KeyEsc), 1
				}
				return nil, 0
			}
			if c, ok := ss3Keys[b[2]]; ok {
				return key(c), 3
			}
			return nil, 3
		}
		return key(KeyEsc), 1 // ESC then another key (Alt-x, or Esc Esc)
	case c == 0x03:
		return key(KeyCtrlC), 1
	case c == '\r' || c == '\n':
		return key(KeyEnter), 1
	case c == '\t':
		return key(KeyTab), 1
	case c == 0x7f || c == 0x08:
		return key(KeyBackspace), 1
	case c < 0x20:
		return nil, 1 // other control keys
	}
	if !utf8.FullRune(b) && !final {
		return nil, 0
	}
	r, n := utf8.DecodeRune(b)
	return &Key{Code: KeyRune, Rune: r}, n
}

var ss3Keys = map[byte]KeyCode{'A': KeyUp, 'B': KeyDown, 'C': KeyRight, 'D': KeyLeft, 'H': KeyHome, 'F': KeyEnd}

// csiKeys maps a CSI sequence's final byte (ESC [ 1 ; 5 A too: modifiers
// are ignored) to a key.
var csiKeys = map[byte]KeyCode{'A': KeyUp, 'B': KeyDown, 'C': KeyRight, 'D': KeyLeft, 'H': KeyHome, 'F': KeyEnd, 'Z': KeyBackTab}

// tildeKeys maps ESC [ n ~ to a key: xterm, rxvt, the Linux console and
// tmux disagree on Home/End, so every spelling counts.
var tildeKeys = map[string]KeyCode{"1": KeyHome, "7": KeyHome, "4": KeyEnd, "8": KeyEnd, "5": KeyPgUp, "6": KeyPgDn}

func parseCSI(b []byte, final bool) (*Key, int) {
	for i := 2; i < len(b); i++ {
		c := b[i]
		if c >= 0x40 && c <= 0x7e {
			if c == '~' {
				param := string(b[2:i])
				if j := indexByte([]byte(param), ';'); j >= 0 {
					param = param[:j]
				}
				if k, ok := tildeKeys[param]; ok {
					return &Key{Code: k}, i + 1
				}
				return nil, i + 1
			}
			if k, ok := csiKeys[c]; ok {
				return &Key{Code: k}, i + 1
			}
			return nil, i + 1
		}
		if c < 0x20 || i > 16 { // not a CSI after all
			return &Key{Code: KeyEsc}, 1
		}
	}
	if final {
		return &Key{Code: KeyEsc}, 1
	}
	return nil, 0
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

// readable waits up to d for fd to have input. select(2) works on
// terminals on both Linux and macOS (poll(2) on macOS doesn't for devices).
func readable(fd int, d time.Duration) (bool, error) {
	var set unix.FdSet
	set.Zero()
	set.Set(fd)
	tv := unix.NsecToTimeval(d.Nanoseconds())
	n, err := unix.Select(fd+1, &set, nil, nil, &tv)
	if err == unix.EINTR {
		return false, nil
	}
	return n > 0, err
}

// readKeys reads keys from fd until stop closes or input ends, passing
// each to handle. It never blocks in read(2) for longer than a poll, so
// it stops promptly when the view closes.
func readKeys(fd int, stop <-chan struct{}, handle func(Key)) {
	var p keyParser
	buf := make([]byte, 256)
	for {
		select {
		case <-stop:
			return
		default:
		}
		wait := 50 * time.Millisecond
		if p.pending() {
			wait = escWait
		}
		ok, err := readable(fd, wait)
		if err != nil {
			return
		}
		if !ok {
			for _, k := range p.flush() {
				handle(k)
			}
			continue
		}
		n, err := unix.Read(fd, buf)
		if err == unix.EINTR || err == unix.EAGAIN {
			continue
		}
		if n <= 0 || err != nil {
			return
		}
		for _, k := range p.feed(buf[:n]) {
			handle(k)
		}
	}
}
