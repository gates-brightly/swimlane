package lint

import (
	"regexp"
	"strings"
)

// view is a lane script split into lines, three ways, so checks can tell
// code from comments and quoted text without a full bash parser:
//
//	raw   the line as written
//	code  comments blanked; quoted strings kept
//	bare  comments and the contents of quoted strings blanked (the quotes
//	      stay, and so do ${...} expansions inside double quotes, which
//	      bash still evaluates)
//
// Heredoc bodies are kept in code, blanked in bare, and flagged in doc.
// Blanking keeps every line's length, so columns and line numbers match.
type view struct {
	raw, code, bare []string
	doc             []bool
}

var heredocRE = regexp.MustCompile(`^<<(-?)[ \t]*(['"]?)([A-Za-z_][A-Za-z0-9_]*)['"]?`)

func scan(src string) *view {
	v := &view{raw: strings.Split(src, "\n")}
	code := []byte(src)
	bare := []byte(src)
	blank := func(i int) {
		if code[i] != '\n' {
			bare[i] = ' '
		}
	}
	const (
		normal = iota
		single
		double
		heredoc
	)
	state := normal
	braces := 0 // ${ depth inside double quotes
	var pending []struct {
		word string
		tabs bool
	}
	lineStart := func(i int) bool { return i == 0 || src[i-1] == '\n' }
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch state {
		case heredoc:
			// i is at the start of a body line.
			end := strings.IndexByte(src[i:], '\n')
			if end < 0 {
				end = len(src) - i
			}
			line := src[i : i+end]
			for k := i; k < i+end; k++ {
				blank(k)
			}
			p := pending[0]
			check := line
			if p.tabs {
				check = strings.TrimLeft(check, "\t")
			}
			if check == p.word {
				pending = pending[1:]
				if len(pending) == 0 {
					state = normal
				}
			}
			i += end // at the newline (or the end)
		case single:
			if c == '\'' {
				state = normal
			} else {
				blank(i)
			}
		case double:
			switch {
			case c == '\\' && i+1 < len(src):
				if braces == 0 {
					blank(i)
					if src[i+1] != '\n' {
						blank(i + 1)
					}
				}
				i++
			case c == '$' && i+1 < len(src) && src[i+1] == '{':
				braces++
				i++
			case c == '}' && braces > 0:
				braces--
			case c == '"' && braces == 0:
				state = normal
			default:
				if braces == 0 {
					blank(i)
				}
			}
		default:
			switch {
			case c == '\\' && i+1 < len(src):
				i++
			case c == '\'':
				state = single
			case c == '"':
				state, braces = double, 0
			case c == '#' && (lineStart(i) || strings.IndexByte(" \t;&|(", src[i-1]) >= 0):
				end := strings.IndexByte(src[i:], '\n')
				if end < 0 {
					end = len(src) - i
				}
				for k := i; k < i+end; k++ {
					code[k], bare[k] = ' ', ' '
				}
				i += end - 1
			case c == '<' && strings.HasPrefix(src[i:], "<<") && !strings.HasPrefix(src[i:], "<<<"):
				if m := heredocRE.FindStringSubmatch(src[i:]); m != nil {
					pending = append(pending, struct {
						word string
						tabs bool
					}{m[3], m[1] == "-"})
					i += len(m[0]) - 1
				}
			case c == '\n' && len(pending) > 0:
				state = heredoc
			}
		}
	}
	v.code = strings.Split(string(code), "\n")
	v.bare = strings.Split(string(bare), "\n")
	v.doc = make([]bool, len(v.raw))
	// Mark heredoc bodies: lines whose bare text was blanked but whose code
	// wasn't, and that aren't inside a multi-line quoted string.
	state, pending = normal, nil
	for i, raw := range v.raw {
		if state == heredoc {
			v.doc[i] = true
			p := pending[0]
			check := raw
			if p.tabs {
				check = strings.TrimLeft(check, "\t")
			}
			if check == p.word {
				pending = pending[1:]
				if len(pending) == 0 {
					state = normal
				}
			}
			continue
		}
		line := v.bare[i]
		for k := 0; k+1 < len(line); k++ {
			if line[k] == '<' && line[k+1] == '<' && (k+2 >= len(line) || line[k+2] != '<') && (k == 0 || line[k-1] != '<') {
				if m := heredocRE.FindStringSubmatch(v.code[i][k:]); m != nil {
					pending = append(pending, struct {
						word string
						tabs bool
					}{m[3], m[1] == "-"})
				}
			}
		}
		if len(pending) > 0 {
			state = heredoc
		}
	}
	return v
}

// isCode reports whether line i has any code (not blank, not only a comment,
// not a heredoc body).
func (v *view) isCode(i int) bool {
	return !v.doc[i] && strings.TrimSpace(v.code[i]) != ""
}

// lastCode returns the index of the last line with code, or -1.
func (v *view) lastCode() int {
	for i := len(v.code) - 1; i >= 0; i-- {
		if v.isCode(i) {
			return i
		}
	}
	return -1
}
