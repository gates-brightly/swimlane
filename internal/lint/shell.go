package lint

import (
	"regexp"
	"strings"

	"github.com/gates-brightly/swimlane/internal/lane"
)

// view is a lane script split into lines, three ways, so checks can tell
// code from comments and quoted text without a full bash parser:
//
//	raw   the line as written
//	code  comments blanked; quoted strings kept
//	bare  comments and the contents of quoted strings blanked (the quotes
//	      stay, and so do ${...} expansions inside double quotes, which
//	      bash still evaluates)
//	exp   what bash expands: comments, single-quoted text and heredoc
//	      bodies blanked; double-quoted text kept, $(...) and all
//
// Heredoc bodies are kept in code, blanked in bare, and flagged in doc.
// Blanking keeps every line's length, so columns and line numbers match.
type view struct {
	raw, code, bare, exp []string
	doc                  []bool
	// inQuote: the line starts inside a quoted string that began on an
	// earlier line, so it holds no command of its own.
	inQuote []bool
}

// heredocRE: a heredoc's start and delimiter (a regular expression, kept:
// lint's views follow lane's textLines, which reads the same by hand).
var heredocRE = regexp.MustCompile(`^<<(-?)[ \t]*(['"]?)([A-Za-z_][A-Za-z0-9_]*)['"]?`)

func scan(src string) *view {
	v := &view{raw: strings.Split(src, "\n")}
	code := []byte(src)
	bare := []byte(src)
	exp := []byte(src)
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
	quotedNL := map[int]bool{} // newlines inside a quoted string

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
				exp[k] = ' '
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
			if c == '\n' {
				quotedNL[i] = true
			}
			if c == '\'' {
				state = normal
			} else {
				blank(i)
				if c != '\n' {
					exp[i] = ' '
				}
			}
		case double:
			if c == '\n' {
				quotedNL[i] = true
			}
			switch {
			case c == '\\' && i+1 < len(src):
				if braces == 0 {
					blank(i)
					if src[i+1] != '\n' {
						blank(i + 1)
					}
				}
				if src[i+1] == '\n' {
					quotedNL[i+1] = true
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
					code[k], bare[k], exp[k] = ' ', ' ', ' '
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
	v.inQuote = make([]bool, len(v.raw))
	for i, line := 0, 0; i < len(src); i++ {
		if src[i] == '\n' {
			line++
			v.inQuote[line] = quotedNL[i]
		}
	}
	v.code = strings.Split(string(code), "\n")
	v.bare = strings.Split(string(bare), "\n")
	v.exp = strings.Split(string(exp), "\n")
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

// A lineCmd is one simple command of a bash script and the line it's on.
// Sub: it runs in a shell of its own (a bash -c body), where the lane
// library's functions (stage, guard, mod, ...) aren't defined.
type lineCmd struct {
	Line  int
	Words []lane.Word
	Sub   bool
}

// commands lists the simple commands a bash script runs, read with the
// quote-aware tokenizer (lane.Commands): never a word in quoted text, a
// comment or a heredoc body. The commands inside a $(...) are listed too,
// quoted or not, and those of a bash -c (or sh -c) body, marked Sub. A
// command continued onto the next lines (a trailing \) is read whole, on
// its first line.
func (v *view) commands() []lineCmd {
	var out []lineCmd
	for i := 0; i < len(v.code); i++ {
		if !v.isCode(i) || v.inQuote[i] {
			continue
		}
		first, line := i, v.code[i]
		for continued(line) && i+1 < len(v.code) && !v.doc[i+1] {
			i++
			line = strings.TrimSuffix(strings.TrimRight(line, " \t"), "\\") + " " + v.code[i]
		}
		subCommands(lane.Commands(line), false, func(c []lane.Word, sub bool) {
			out = append(out, lineCmd{Line: first, Words: c, Sub: sub})
		})
	}
	return out
}

// continued reports whether a line of code ends with a \ that continues
// it (an odd number of them, outside quotes: the code view keeps quotes,
// so a \ at the end of a quoted line's text isn't one).
func continued(line string) bool {
	t := strings.TrimRight(line, " \t")
	n := len(t) - len(strings.TrimRight(t, "\\"))
	return n%2 == 1
}

// subCommands calls fn with each of cmds, then the commands inside its
// words' $(...) and its bash -c body (sub: in a shell of their own).
func subCommands(cmds [][]lane.Word, sub bool, fn func([]lane.Word, bool)) {
	for _, c := range cmds {
		fn(c, sub)
		for k, w := range c {
			if w.Expands {
				for _, inner := range substitutions(w.Text) {
					subCommands(lane.Commands(inner), sub, fn)
				}
			}
			if k > 0 && w.Text == "-c" && k+1 < len(c) && isShell(c[k-1].Text) {
				for _, line := range strings.Split(c[k+1].Text, "\n") {
					subCommands(lane.Commands(line), true, fn)
				}
			}
		}
	}
}

// isShell reports whether a command word runs a POSIX shell.
func isShell(word string) bool {
	switch word[strings.LastIndexByte(word, '/')+1:] {
	case "bash", "sh", "zsh", "dash":
		return true
	}
	return false
}

// substitutions are the texts of the outermost $(...) in s.
func substitutions(s string) []string {
	var out []string
	for {
		at := strings.Index(s, "$(")
		if at < 0 {
			return out
		}
		depth, end := 0, -1
		for k := at + 1; k < len(s) && end < 0; k++ {
			switch s[k] {
			case '(':
				depth++
			case ')':
				if depth--; depth == 0 {
					end = k
				}
			}
		}
		if end < 0 {
			return append(out, s[at+2:])
		}
		out = append(out, s[at+2:end])
		s = s[end+1:]
	}
}

// isSwim reports whether a command's first word runs swim: swim,
// "$SWIM_BIN" (or ${SWIM_BIN:-swim}), or a path ending in /swim.
func isSwim(w lane.Word) bool {
	return w.Text == "swim" || strings.HasSuffix(w.Text, "/swim") || strings.Contains(w.Text, "SWIM_BIN")
}

// literal reports whether a word's value is known before the run.
func literal(w lane.Word) bool { return !w.Expands }
