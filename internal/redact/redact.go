// Package redact masks secret values before swim writes anything: logs,
// snapshots, the terminal stream, status.yml and .swim.log. It is a
// backstop, not permission: lanes must still never print secrets.
//
// Secrets are the values of environment variables named in config
// (secret_env), plus, with secret_env_auto, variables whose names look like
// secrets (*_TOKEN, *_SECRET, *_PASSWORD, *_API_KEY, ...), plus, with
// secret_patterns_auto, values that look like well-known credentials (AWS
// access key ids, GitHub and Slack tokens, JWTs) wherever they came from.
package redact

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Mask replaces a secret.
const Mask = "***"

// MinLen is the shortest value treated as a secret (shorter ones, like "1"
// or "true", would mask everything).
const MinLen = 6

// Options says which variables and value patterns count as secrets.
type Options struct {
	Names        []string // secret_env
	Auto         bool     // secret_env_auto: also names matching the built-in patterns
	Ignore       []string // secret_env_ignore: names exempt from Auto
	AutoPatterns bool     // secret_patterns_auto: also well-known credential shapes
	// Labeled are secrets known by name (swim secret): each is masked
	// exactly, as "***(name)", so a log says which secret it hid.
	Labeled []Labeled
	// Refresh, if set, updates Labeled as the masker is used.
	Refresh *Refresh
}

// Labeled is a named secret value.
type Labeled struct {
	Name, Value string
}

var autoNameRE = regexp.MustCompile(`(?i)(_TOKEN$|_SECRET$|_SECRET_|_PASSWORD$|_PASSWD$|_API_KEY$|_PRIVATE_KEY$|^AWS_SECRET_ACCESS_KEY$|^AWS_SESSION_TOKEN$)`)

// AutoName reports whether a variable name matches the built-in patterns.
func AutoName(name string) bool { return autoNameRE.MatchString(name) }

var valuePatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),                                            // AWS access key id
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`),                                  // GitHub tokens
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{22,}\b`),                                // GitHub fine-grained tokens
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`),                                // Slack tokens
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`), // JWTs
}

// Masker replaces secrets in text.
type Masker struct {
	names    []string  // variables whose values are masked (for the round header)
	literals []literal // values and their encoded forms, longest first
	patterns []*regexp.Regexp

	// With Options.Refresh: the env's literals, kept to rebuild literals
	// when the named secrets change.
	refresh *Refresh
	mu      sync.Mutex
	env     []literal
}

// Refresh lets a long-lived masker (a lane's wrapper, a step that runs a
// while) pick up named secrets that appear after it was made: a guarded
// secret released to the lane mid-run.
type Refresh struct {
	// Changed reports whether the named secrets may have changed since it
	// last returned true (checked before each String).
	Changed func() bool
	// Labeled are the named secrets now.
	Labeled func() []Labeled
}

// literal is one string to mask and what replaces it.
type literal struct {
	value, mask string
}

// literals collects each value and its encoded forms, skipping any in seen.
type literals struct {
	out  []literal
	seen map[string]bool
}

func (l *literals) add(v, mask string) {
	if len(v) >= MinLen && !l.seen[v] {
		l.seen[v] = true
		l.out = append(l.out, literal{v, mask})
	}
}

func (l *literals) addValue(value, mask string) {
	l.add(value, mask)
	l.add(base64.StdEncoding.EncodeToString([]byte(value)), mask)
	l.add(base64.RawStdEncoding.EncodeToString([]byte(value)), mask)
	l.add(base64.URLEncoding.EncodeToString([]byte(value)), mask)
	l.add(url.QueryEscape(value), mask)
	l.add(url.PathEscape(value), mask)
	// A multi-line value (a PEM key) is masked line by line too.
	for _, line := range strings.Split(value, "\n") {
		l.add(strings.TrimSpace(line), mask)
	}
}

func (l *literals) labeled(ls []Labeled) {
	for _, x := range ls {
		if len(x.Value) >= MinLen {
			l.addValue(x.Value, Mask+"("+x.Name+")")
		}
	}
}

func longestFirst(ls []literal) []literal {
	sort.SliceStable(ls, func(a, b int) bool { return len(ls[a].value) > len(ls[b].value) })
	return ls
}

// New collects the secret values from env ("NAME=value" entries).
func New(env []string, o Options) *Masker {
	listed := map[string]bool{}
	for _, n := range o.Names {
		listed[n] = true
	}
	ignored := map[string]bool{}
	for _, n := range o.Ignore {
		ignored[n] = true
	}
	m := &Masker{refresh: o.Refresh}
	// Named secrets first: a value both named and in env shows its name.
	all := &literals{seen: map[string]bool{}}
	all.labeled(o.Labeled)
	fromEnv := &literals{seen: map[string]bool{}}
	for _, kv := range env {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || len(value) < MinLen {
			continue
		}
		if !listed[name] && !(o.Auto && AutoName(name) && !ignored[name]) {
			continue
		}
		m.names = append(m.names, name)
		all.addValue(value, Mask)
		fromEnv.addValue(value, Mask)
	}
	sort.Strings(m.names)
	m.literals = longestFirst(all.out)
	m.env = fromEnv.out
	if o.AutoPatterns {
		m.patterns = valuePatterns
	}
	return m
}

// update rebuilds the literals when the named secrets changed.
func (m *Masker) update() {
	if m.refresh == nil || !m.refresh.Changed() {
		return
	}
	all := &literals{seen: map[string]bool{}}
	all.labeled(m.refresh.Labeled())
	for _, l := range m.env {
		all.add(l.value, l.mask)
	}
	m.literals = longestFirst(all.out)
}

// Names lists the variables whose values are masked.
func (m *Masker) Names() []string { return m.names }

// Active reports whether there is anything to mask (or may be, once a
// refresh brings named secrets).
func (m *Masker) Active() bool {
	return m != nil && (len(m.literals) > 0 || len(m.patterns) > 0 || m.refresh != nil)
}

// String masks every secret in s.
func (m *Masker) String(s string) string {
	if !m.Active() {
		return s
	}
	m.mu.Lock()
	m.update()
	lits := m.literals
	m.mu.Unlock()
	for _, l := range lits {
		if strings.Contains(s, l.value) {
			s = strings.ReplaceAll(s, l.value, l.mask)
		}
	}
	for _, re := range m.patterns {
		s = re.ReplaceAllString(s, Mask)
	}
	return s
}

// Value reports whether name's value would be masked, for printing
// "NAME=***(len N)" instead of the value.
func (m *Masker) Value(name string) bool {
	for _, n := range m.names {
		if n == name {
			return true
		}
	}
	return false
}

// Writer returns a writer that masks what passes through it. It works a line
// at a time (a line ends at \n or \r), so a secret split across writes is
// still caught; a partial line is held until it ends or Flush is called.
func (m *Masker) Writer(w io.Writer) *Writer {
	return &Writer{m: m, w: w}
}

// Writer is a masking writer; see Masker.Writer.
type Writer struct {
	mu  sync.Mutex
	m   *Masker
	w   io.Writer
	buf []byte
}

func (mw *Writer) Write(p []byte) (int, error) {
	mw.mu.Lock()
	defer mw.mu.Unlock()
	if !mw.m.Active() && len(mw.buf) == 0 {
		return mw.w.Write(p)
	}
	mw.buf = append(mw.buf, p...)
	for {
		i := bytes.IndexAny(mw.buf, "\n\r")
		if i < 0 {
			break
		}
		line := mw.m.String(string(mw.buf[:i+1]))
		mw.buf = mw.buf[i+1:]
		if _, err := io.WriteString(mw.w, line); err != nil {
			return 0, err
		}
	}
	// Don't hold an unbounded partial line (a binary blob, a huge JSON).
	if len(mw.buf) > 64*1024 {
		io.WriteString(mw.w, mw.m.String(string(mw.buf)))
		mw.buf = mw.buf[:0]
	}
	return len(p), nil
}

// SetMasker masks with m from the next line on: values the run learned
// since the writer was made (mod_env's, a released secret's).
func (mw *Writer) SetMasker(m *Masker) {
	mw.mu.Lock()
	defer mw.mu.Unlock()
	mw.m = m
}

// Flush writes any partial line.
func (mw *Writer) Flush() error {
	mw.mu.Lock()
	defer mw.mu.Unlock()
	if len(mw.buf) == 0 {
		return nil
	}
	_, err := io.WriteString(mw.w, mw.m.String(string(mw.buf)))
	mw.buf = mw.buf[:0]
	return err
}
