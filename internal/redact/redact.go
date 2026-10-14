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
}

// literal is one string to mask and what replaces it.
type literal struct {
	value, mask string
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
	m := &Masker{}
	seen := map[string]bool{}
	add := func(v, mask string) {
		if len(v) >= MinLen && !seen[v] {
			seen[v] = true
			m.literals = append(m.literals, literal{v, mask})
		}
	}
	addValue := func(value, mask string) {
		add(value, mask)
		add(base64.StdEncoding.EncodeToString([]byte(value)), mask)
		add(base64.RawStdEncoding.EncodeToString([]byte(value)), mask)
		add(base64.URLEncoding.EncodeToString([]byte(value)), mask)
		add(url.QueryEscape(value), mask)
		add(url.PathEscape(value), mask)
		// A multi-line value (a PEM key) is masked line by line too.
		for _, line := range strings.Split(value, "\n") {
			add(strings.TrimSpace(line), mask)
		}
	}
	// Named secrets first: a value both named and in env shows its name.
	for _, l := range o.Labeled {
		if len(l.Value) >= MinLen {
			addValue(l.Value, Mask+"("+l.Name+")")
		}
	}
	for _, kv := range env {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || len(value) < MinLen {
			continue
		}
		if !listed[name] && !(o.Auto && AutoName(name) && !ignored[name]) {
			continue
		}
		m.names = append(m.names, name)
		addValue(value, Mask)
	}
	sort.Strings(m.names)
	sort.SliceStable(m.literals, func(a, b int) bool { return len(m.literals[a].value) > len(m.literals[b].value) })
	if o.AutoPatterns {
		m.patterns = valuePatterns
	}
	return m
}

// Names lists the variables whose values are masked.
func (m *Masker) Names() []string { return m.names }

// Active reports whether there is anything to mask.
func (m *Masker) Active() bool { return m != nil && (len(m.literals) > 0 || len(m.patterns) > 0) }

// String masks every secret in s.
func (m *Masker) String(s string) string {
	if !m.Active() {
		return s
	}
	for _, l := range m.literals {
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
	if !mw.m.Active() {
		return mw.w.Write(p)
	}
	mw.mu.Lock()
	defer mw.mu.Unlock()
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
