// Package changelog parses CHANGELOG.md into revisions and picks the ones
// `swim changelog` prints.
//
// A revision is a "## " section, newest first. Its id is the heading's first
// word: "Unreleased", a version ("2.20261009"), or a commit for revisions
// before versioning ("dd2db6f"). Text before the first "## " is the preamble.
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
	versionRE = regexp.MustCompile(`^(\d+)\.(\d{8})$`)
	tagRE     = regexp.MustCompile(`^v0\.(\d+)\.(\d{8})$`)
)

// version parses "2.20261009" or the tag form "v0.2.20261009".
func version(s string) (breaking, date int, ok bool) {
	m := versionRE.FindStringSubmatch(s)
	if m == nil {
		m = tagRE.FindStringSubmatch(s)
	}
	if m == nil {
		return 0, 0, false
	}
	b, _ := strconv.Atoi(m[1])
	d, _ := strconv.Atoi(m[2])
	return b, d, true
}

// Normalize turns a tag ("v0.2.20261009") into its version ("2.20261009");
// other ids are returned unchanged.
func Normalize(id string) string {
	if m := tagRE.FindStringSubmatch(id); m != nil {
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
		if e.ID == ref || (len(ref) >= 7 && strings.HasPrefix(e.ID, ref)) {
			return entries[:i], nil
		}
	}
	rb, rd, ok := version(ref)
	if !ok {
		return nil, fmt.Errorf("no revision %q in the changelog (known: %s)", ref, ids(entries))
	}
	var out []Entry
	for _, e := range entries {
		if e.ID == Unreleased {
			out = append(out, e)
			continue
		}
		if b, d, ok := version(e.ID); ok && (b > rb || (b == rb && d > rd)) {
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
