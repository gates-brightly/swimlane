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
	"regexp"
	"strconv"
	"strings"
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

var (
	versionRE = regexp.MustCompile(`^0\.(\d{8})\.([1-9]\d*)$`)  // 0.20261014.2
	tagRE     = regexp.MustCompile(`^v0\.(\d{8})\.([1-9]\d*)$`) // v0.20261014.2
	// Before 0.<date>.<index>: <breaking>.<date>, tagged v0.<breaking>.<date>.
	oldVersionRE = regexp.MustCompile(`^(\d+)\.(\d{8})$`)
	// A commit prefix names a revision from before versioning.
	commitRE = regexp.MustCompile(`^[0-9a-f]{7,}$`)
	oldTagRE = regexp.MustCompile(`^v0\.(\d{1,7})\.(\d{8})$`)
)

// order is a version's place in time: its date, then its release that day
// (the index; for the old scheme, the breaking version, which also rose
// with each release that day).
type order struct{ date, n int }

func (a order) after(b order) bool { return a.date > b.date || a.date == b.date && a.n > b.n }

// version parses a version or its tag, in either scheme.
func version(s string) (order, bool) {
	if m := versionRE.FindStringSubmatch(s); m != nil {
		return orderOf(m[1], m[2]), true
	}
	if m := tagRE.FindStringSubmatch(s); m != nil {
		return orderOf(m[1], m[2]), true
	}
	for _, re := range []*regexp.Regexp{oldVersionRE, oldTagRE} {
		if m := re.FindStringSubmatch(s); m != nil {
			return orderOf(m[2], m[1]), true
		}
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
	if m := tagRE.FindStringSubmatch(id); m != nil {
		return "0." + m[1] + "." + m[2]
	}
	if m := oldTagRE.FindStringSubmatch(id); m != nil {
		return m[1] + "." + m[2]
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
		if e.ID == ref || (commitRE.MatchString(ref) && strings.HasPrefix(e.ID, ref)) {
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
