package changelog

import (
	"regexp"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane"
)

const sample = `# Changelog

Preamble, not a revision.

## Unreleased

- new thing

## 2.20261012 (v0.2.20261012, abc1234, 2026-10-12)

- second release

## 2.20261009 (v0.2.20261009, c6a87b0, 2026-10-09)

- first release

## dd2db6f (pre-release, 2026-10-09)

- before versioning
`

func idList(es []Entry) string {
	s := make([]string, len(es))
	for i, e := range es {
		s[i] = e.ID
	}
	return strings.Join(s, " ")
}

func TestParse(t *testing.T) {
	es := Parse(sample)
	if got, want := idList(es), "Unreleased 2.20261012 2.20261009 dd2db6f"; got != want {
		t.Fatalf("ids = %q, want %q", got, want)
	}
	if es[1].Heading != "2.20261012 (v0.2.20261012, abc1234, 2026-10-12)" || es[1].Body != "- second release" {
		t.Errorf("entry 1 = %+v", es[1])
	}
	if strings.Contains(es[0].Body, "Preamble") {
		t.Error("preamble leaked into the first revision")
	}
}

func TestNonEmptyDropsPlaceholder(t *testing.T) {
	es := NonEmpty(Parse("## Unreleased\n\n## 2.20261009 (x)\n\n- a\n"))
	if got := idList(es); got != "2.20261009" {
		t.Errorf("ids = %q", got)
	}
}

func TestSince(t *testing.T) {
	es := Parse(sample)
	for _, tc := range []struct{ ref, want string }{
		{"2.20261009", "Unreleased 2.20261012"},
		{"v0.2.20261009", "Unreleased 2.20261012"},      // tag form
		{"2.20261012", "Unreleased"},                    // newest release
		{"dd2db6f", "Unreleased 2.20261012 2.20261009"}, // commit id
		{"dd2db6f0123", ""},                             // longer than the id: no prefix match
		{"2.20261010", "Unreleased 2.20261012"},         // a build between releases
		{"1.20300101", "Unreleased 2.20261012 2.20261009"},
		{"Unreleased", ""},
	} {
		got, err := Since(es, tc.ref)
		if tc.ref == "dd2db6f0123" {
			if err == nil {
				t.Errorf("Since(%q) = %q, want an error", tc.ref, idList(got))
			}
			continue
		}
		if err != nil {
			t.Errorf("Since(%q): %v", tc.ref, err)
			continue
		}
		if idList(got) != tc.want {
			t.Errorf("Since(%q) = %q, want %q", tc.ref, idList(got), tc.want)
		}
	}
	if _, err := Since(es, "nope"); err == nil || !strings.Contains(err.Error(), "known: Unreleased, 2.20261012") {
		t.Errorf("Since(nope) error = %v", err)
	}
}

// TestRepoChangelog keeps CHANGELOG.md parseable, so a release edit can't
// break `swim changelog`: unique ids, Unreleased only at the top, release
// headings in the documented form, versions newest first.
func TestRepoChangelog(t *testing.T) {
	es := Parse(swimlane.Changelog)
	if len(NonEmpty(es)) == 0 {
		t.Fatal("CHANGELOG.md has no revisions")
	}
	heading := regexp.MustCompile(`^(\d+\.\d{8}) \(v0\.\d+\.\d{8}, ([0-9a-f]{7,}, )?\d{4}-\d{2}-\d{2}\)$|^(\d+\.\d{8}) \(pre-release, [0-9a-f]{7,}, \d{4}-\d{2}-\d{2}\)$|^([0-9a-f]{7,}) \(pre-release, \d{4}-\d{2}-\d{2}\)$`)
	seen := map[string]bool{}
	lastB, lastD := 1<<31, 1<<31
	for i, e := range es {
		if seen[e.ID] {
			t.Errorf("revision %q appears twice; a second release on the same day bumps Breaking (internal/version)", e.ID)
		}
		seen[e.ID] = true
		if e.ID == Unreleased {
			if i != 0 {
				t.Errorf("## Unreleased must be the first revision, found at %d", i)
			}
			continue
		}
		if !heading.MatchString(e.Heading) {
			t.Errorf("heading %q isn't `<version> (v0.<b>.<date>[, <commit>], <date>)`, `<version> (pre-release, <commit>, <date>)` or `<commit> (pre-release, <date>)`", e.Heading)
		}
		if b, d, ok := version(e.ID); ok {
			if b > lastB || (b == lastB && d >= lastD) {
				t.Errorf("revision %s is out of order (newest first)", e.ID)
			}
			lastB, lastD = b, d
			if m := regexp.MustCompile(`\(v0\.(\d+)\.(\d{8}),`).FindStringSubmatch(e.Heading); m != nil && m[1]+"."+m[2] != e.ID {
				t.Errorf("revision %s names tag v0.%s.%s", e.ID, m[1], m[2])
			}
		}
		if e.Empty() {
			t.Errorf("revision %s is empty", e.ID)
		}
	}
}
