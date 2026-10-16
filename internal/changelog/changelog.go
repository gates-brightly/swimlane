// Package changelog parses CHANGELOG.md into revisions and picks the ones
// `swim changelog` prints.
//
// A revision is a "## " section, newest first. Its id is the heading's first
// word: "Unreleased", a version ("0.20261014.2", or before that scheme
// "7.20261013"), or a commit for revisions before versioning ("dd2db6f").
// Text before the first "## " is the preamble.
package changelog

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gates-brightly/swimlane/internal/chars"
	swimversion "github.com/gates-brightly/swimlane/internal/version"
)

// Unreleased is the id of the section for changes not yet in a release.
const Unreleased = "Unreleased"

// Entry is one revision.
type Entry struct {
	ID      string // first word of the heading
	Heading string // the heading without "## "
	Body    string // everything up to the next revision, trimmed
}

// Empty reports whether the revision has no content (e.g. a fresh
// "## Unreleased" placeholder after a release).
func (e Entry) Empty() bool { return strings.TrimSpace(e.Body) == "" }

// Parse splits a changelog into its revisions, in file order (newest first).
func Parse(md string) []Entry {
	var out []Entry
	var cur *Entry
	var body []string
	flush := func() {
		if cur != nil {
			cur.Body = strings.TrimSpace(strings.Join(body, "\n"))
			out = append(out, *cur)
		}
	}
	for _, line := range strings.Split(md, "\n") {
		if h, ok := strings.CutPrefix(line, "## "); ok {
			flush()
			h = strings.TrimSpace(h)
			id, _, _ := strings.Cut(h, " ")
			cur, body = &Entry{ID: id, Heading: h}, nil
			continue
		}
		if cur != nil {
			body = append(body, line)
		}
	}
	flush()
	return out
}

// Before 0.<date>.<index>, versions were <breaking>.<date>, tagged
// v0.<breaking>.<date>.
func oldVersion(s string) (breaking, date string, ok bool) {
	b, d, found := strings.Cut(s, ".")
	if !found || !chars.Digits(b) || len(d) != 8 || !chars.Digits(d) {
		return "", "", false
	}
	return b, d, true
}

func oldTag(s string) (breaking, date string, ok bool) {
	rest, isTag := strings.CutPrefix(s, "v0.")
	if !isTag {
		return "", "", false
	}
	b, d, ok := oldVersion(rest)
	if !ok || len(b) > 7 {
		return "", "", false
	}
	return b, d, true
}

// commitPrefix: a commit prefix (7+ hex digits) names a revision from
// before versioning.
func commitPrefix(s string) bool {
	return len(s) >= 7 && chars.All(s, chars.Or(chars.Digit, chars.In("abcdef")))
}

// order is a version's place in time: its date, then its release that day
// (the index; for the old scheme, the breaking version, which also rose
// with each release that day).
type order struct{ date, n int }

func (a order) after(b order) bool { return a.date > b.date || a.date == b.date && a.n > b.n }

// version parses a version or its tag, in either scheme.
func version(s string) (order, bool) {
	if d, i, ok := swimversion.ParseVersion(s); ok {
		return orderOf(d, i), true
	}
	if d, i, ok := swimversion.ParseTag(s); ok {
		return orderOf(d, i), true
	}
	if b, d, ok := oldVersion(s); ok {
		return orderOf(d, b), true
	}
	if b, d, ok := oldTag(s); ok {
		return orderOf(d, b), true
	}
	return order{}, false
}

func orderOf(date, n string) order {
	d, _ := strconv.Atoi(date)
	k, _ := strconv.Atoi(n)
	return order{d, k}
}

// Normalize turns a tag ("v0.20261014.2", or "v0.2.20261009") into its
// version ("0.20261014.2", "2.20261009"); other ids are returned unchanged.
func Normalize(id string) string {
	if d, i, ok := swimversion.ParseTag(id); ok {
		return "0." + d + "." + i
	}
	if b, d, ok := oldTag(id); ok {
		return b + "." + d
	}
	return id
}

// Since returns the revisions newer than ref, newest first. ref is a
// revision id, a tag, or a commit prefix (7+ characters) of a pre-versioning
// revision. A version that isn't in the changelog (e.g. a build between
// releases) selects every revision with a higher version, plus Unreleased.
func Since(entries []Entry, ref string) ([]Entry, error) {
	ref = Normalize(strings.TrimSpace(ref))
	for i, e := range entries {
		if e.ID == ref || (commitPrefix(ref) && strings.HasPrefix(e.ID, ref)) {
			return entries[:i], nil
		}
	}
	ro, ok := version(ref)
	if !ok {
		return nil, fmt.Errorf("no revision %q in the changelog (known: %s)", ref, ids(entries))
	}
	var out []Entry
	for _, e := range entries {
		if e.ID == Unreleased {
			out = append(out, e)
			continue
		}
		if o, ok := version(e.ID); ok && o.after(ro) {
			out = append(out, e)
		}
	}
	return out, nil
}

// NonEmpty drops revisions with no content.
func NonEmpty(entries []Entry) []Entry {
	var out []Entry
	for _, e := range entries {
		if !e.Empty() {
			out = append(out, e)
		}
	}
	return out
}

func ids(entries []Entry) string {
	s := make([]string, len(entries))
	for i, e := range entries {
		s[i] = e.ID
	}
	return strings.Join(s, ", ")
}
