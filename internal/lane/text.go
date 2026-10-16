package lane

import (
	"strings"
	"unicode"

	"github.com/gates-brightly/swimlane/internal/chars"
)

// Reading a lane script without regular expressions: header lines are
// "# Key: value", taken apart with string functions, and the body is read
// a command at a time by a small quote-aware tokenizer (Commands). Nothing
// read from the body widens what a lane may do: its guards, the files it
// reads and the modules it calls are what its header declares, and the run
// refuses anything else. The body only tells swim where the round's stages
// and handlers are, and whether it shows evidence.

// headerKey reads a header line, "# Key: value", with at most one space or
// tab after the #. An indented comment ("#   After: ...") is prose, not a
// key. bare also accepts the line without its # ("Round: ..."). The key
// starts with a letter and holds letters, digits, spaces, _ and -.
func headerKey(line string, bare bool) (key, value string, ok bool) {
	rest, found := strings.CutPrefix(line, "#")
	if !found && !bare {
		return "", "", false
	}
	if strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, "\t") {
		rest = rest[1:]
	}
	k, v, found := strings.Cut(rest, ":")
	if !found || k == "" || !isLetter(rune(k[0])) {
		return "", "", false
	}
	for _, r := range k {
		if !isLetter(r) && !isDigit(r) && r != ' ' && r != '_' && r != '-' {
			return "", "", false
		}
	}
	return k, strings.TrimSpace(v), true
}

// splitList splits a header value at spaces and commas.
func splitList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == ',' })
}

// Character tests, from chars.
var (
	isLetter = chars.Letter
	isDigit  = chars.Digit
	isIdent  = chars.Ident
	allOf    = chars.All
	isDigits = chars.Digits
)

// Word is one word of a command: its value with quotes removed, whether
// any of it was quoted, and whether it expands ($, `), so its value is
// only known at run time.
type Word struct {
	Text    string
	Quoted  bool
	Expands bool
}

// Commands splits one line of bash into its simple commands' words:
// comments dropped, quotes honoured, commands split at ; & | ( ) { }, and
// leading keywords (if, then, else, elif, do, while, until, !) removed, so
// each command's first word is what it runs. It reads one line: a command
// continued onto the next isn't joined.
func Commands(line string) [][]Word {
	var out [][]Word
	var cmd []Word
	var cur strings.Builder
	quoted, expands, inWord := false, false, false
	var quote byte
	endWord := func() {
		if inWord {
			cmd = append(cmd, Word{Text: cur.String(), Quoted: quoted, Expands: expands})
		}
		cur.Reset()
		quoted, expands, inWord = false, false, false
	}
	endCmd := func() {
		endWord()
		for len(cmd) > 0 && !cmd[0].Quoted && keywords[cmd[0].Text] {
			cmd = cmd[1:]
		}
		if len(cmd) > 0 {
			out = append(out, cmd)
		}
		cmd = nil
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0 && c == quote:
			quote = 0
		case quote == '"' && c == '\\' && i+1 < len(line):
			i++
			cur.WriteByte(line[i])
		case quote != 0:
			if (c == '$' || c == '`') && quote == '"' {
				expands = true
			}
			cur.WriteByte(c)
		case c == '\'' || c == '"':
			quote, inWord, quoted = c, true, true
		case c == '\\' && i+1 < len(line):
			i++
			cur.WriteByte(line[i])
			inWord = true
		case c == '#' && !inWord:
			endCmd()
			return out
		case c == ' ' || c == '\t':
			endWord()
		case strings.IndexByte(";&|(){}", c) >= 0:
			endCmd()
		default:
			if c == '$' || c == '`' {
				expands = true
			}
			cur.WriteByte(c)
			inWord = true
		}
	}
	endCmd()
	return out
}

// keywords come before a command, not as one.
var keywords = map[string]bool{"if": true, "then": true, "else": true, "elif": true, "do": true, "while": true, "until": true, "!": true}

// swimCall reports whether cmd runs swim itself (swim, "$SWIM_BIN", a path
// ending in /swim) and returns the words after it.
func swimCall(cmd []Word) ([]Word, bool) {
	if len(cmd) == 0 {
		return nil, false
	}
	w := cmd[0].Text
	if w == "swim" || strings.HasSuffix(w, "/swim") || strings.Contains(w, "SWIM_BIN") {
		return cmd[1:], true
	}
	return nil, false
}

// quotedThen reports whether line holds name in quotes ("name" or 'name'),
// followed, past spaces, by the byte next: a call's arguments as a list,
// ["swim", "stage", "check"] or ["evidence", ...].
func quotedThen(line, name string, next byte) bool {
	for _, q := range []string{`"`, `'`} {
		pat := q + name + q
		for from := 0; ; {
			i := strings.Index(line[from:], pat)
			if i < 0 {
				break
			}
			rest := strings.TrimLeft(line[from+i+len(pat):], " \t")
			if rest != "" && rest[0] == next {
				return true
			}
			from += i + len(pat)
		}
	}
	return false
}

// quotedAfter returns the quoted words that follow name in quotes and a
// comma (["stage", "check"] gives "check").
func quotedAfter(line, name string) []string {
	var out []string
	for _, q := range []string{`"`, `'`} {
		pat := q + name + q
		for from := 0; ; {
			i := strings.Index(line[from:], pat)
			if i < 0 {
				break
			}
			from += i + len(pat)
			rest := strings.TrimLeft(line[from:], " \t")
			if !strings.HasPrefix(rest, ",") {
				continue
			}
			rest = strings.TrimLeft(rest[1:], " \t")
			if rest == "" || (rest[0] != '"' && rest[0] != '\'') {
				continue
			}
			if end := strings.IndexByte(rest[1:], rest[0]); end >= 0 {
				out = append(out, rest[1:1+end])
			}
		}
	}
	return out
}
