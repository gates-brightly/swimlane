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

## 0.20261014.2 (v0.20261014.2, 2026-10-14)

- second release that day

## 0.20261014.1 (v0.20261014.1, 2026-10-14)

- first release in the 0.<date>.<index> scheme

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
	if got, want := idList(es), "Unreleased 0.20261014.2 0.20261014.1 2.20261012 2.20261009 dd2db6f"; got != want {
		t.Fatalf("ids = %q, want %q", got, want)
	}
	if es[3].Heading != "2.20261012 (v0.2.20261012, abc1234, 2026-10-12)" || es[3].Body != "- second release" {
		t.Errorf("entry 3 = %+v", es[3])
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
		{"0.20261014.1", "Unreleased 0.20261014.2"},
		{"v0.20261014.1", "Unreleased 0.20261014.2"},                              // tag form
		{"0.20261014", "Unreleased 0.20261014.2 0.20261014.1"},                    // a build between releases
		{"2.20261009", "Unreleased 0.20261014.2 0.20261014.1 2.20261012"},         // the old scheme
		{"v0.2.20261009", "Unreleased 0.20261014.2 0.20261014.1 2.20261012"},      // its tag form
		{"2.20261012", "Unreleased 0.20261014.2 0.20261014.1"},                    // into the new scheme
		{"0.20261014.2", "Unreleased"},                                            // newest release
		{"dd2db6f", "Unreleased 0.20261014.2 0.20261014.1 2.20261012 2.20261009"}, // commit id
		{"dd2db6f0123", ""}, // longer than the id: no prefix match
		{"2.20261010", "Unreleased 0.20261014.2 0.20261014.1 2.20261012"}, // an old build between releases
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
	if _, err := Since(es, "nope"); err == nil || !strings.Contains(err.Error(), "known: Unreleased, 0.20261014.2") {
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
	heading := regexp.MustCompile(`^(0\.\d{8}\.[1-9]\d*) \(v0\.\d{8}\.[1-9]\d*, \d{4}-\d{2}-\d{2}\)$|^(\d+\.\d{8}) \(v0\.\d+\.\d{8}, ([0-9a-f]{7,}, )?\d{4}-\d{2}-\d{2}\)$|^(\d+\.\d{8}) \(pre-release, [0-9a-f]{7,}, \d{4}-\d{2}-\d{2}\)$|^([0-9a-f]{7,}) \(pre-release, \d{4}-\d{2}-\d{2}\)$`)
	seen := map[string]bool{}
	var last *order
	for i, e := range es {
		if seen[e.ID] {
			t.Errorf("revision %q appears twice; a second release on the same day takes the next index (0.<date>.<index>)", e.ID)
		}
		seen[e.ID] = true
		if e.ID == Unreleased {
			if i != 0 {
				t.Errorf("## Unreleased must be the first revision, found at %d", i)
			}
			continue
		}
		if !heading.MatchString(e.Heading) {
			t.Errorf("heading %q isn't `0.<date>.<index> (v0.<date>.<index>, <date>)`, an older `<b>.<date> (v0.<b>.<date>[, <commit>], <date>)`, `<version> (pre-release, <commit>, <date>)` or `<commit> (pre-release, <date>)`", e.Heading)
		}
		if o, ok := version(e.ID); ok {
			if last != nil && !last.after(o) {
				t.Errorf("revision %s is out of order (newest first)", e.ID)
			}
			last = &o
			if m := regexp.MustCompile(`\((v0\.\d+\.\d+),`).FindStringSubmatch(e.Heading); m != nil && Normalize(m[1]) != e.ID {
				t.Errorf("revision %s names tag %s", e.ID, m[1])
			}
		}
		if e.Empty() {
			t.Errorf("revision %s is empty", e.ID)
		}
	}
}
