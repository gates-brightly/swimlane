package step

import "io"

// StripWriter removes ANSI escape sequences (CSI, OSC and two-byte ESC
// sequences) from everything written through it. It keeps state across
// writes, so a sequence split between two reads is still removed.
type StripWriter struct {
	W     io.Writer
	state int
}

const (
	stNormal = iota
	stEsc
	stCSI
	stOSC
	stOSCEsc
	stEscInter
)

func (s *StripWriter) Write(p []byte) (int, error) {
	out := make([]byte, 0, len(p))
	for _, b := range p {
		switch s.state {
		case stNormal:
			if b == 0x1b {
				s.state = stEsc
			} else {
				out = append(out, b)
			}
		case stEsc:
			switch b {
			case '[':
				s.state = stCSI
			case ']':
				s.state = stOSC
			default:
				if b >= 0x20 && b <= 0x2f {
					// Intermediate byte, as in ESC ( B: wait for the final byte.
					s.state = stEscInter
				} else {
					// Two-byte sequence such as ESC 7: drop the final byte.
					s.state = stNormal
				}
			}
		case stEscInter:
			if b >= 0x30 && b <= 0x7e {
				s.state = stNormal
			}
		case stCSI:
			// Parameters and intermediates are 0x20-0x3f; a final byte 0x40-0x7e ends it.
			if b >= 0x40 && b <= 0x7e {
				s.state = stNormal
			}
		case stOSC:
			switch b {
			case 0x07:
				s.state = stNormal
			case 0x1b:
				s.state = stOSCEsc
			}
		case stOSCEsc:
			// ESC \ terminates OSC; anything else keeps us inside it.
			if b == '\\' {
				s.state = stNormal
			} else {
				s.state = stOSC
			}
		}
	}
	if len(out) > 0 {
		if _, err := s.W.Write(out); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}
