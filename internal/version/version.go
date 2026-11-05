// Package version defines swim's version, 0.<YYYYMMDD>.<index>.
package version

import (
	"runtime/debug"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/chars"
)

// A release is tagged v0.<YYYYMMDD>.<index>: the day, and the release's
// number that day (1, 2, ...), e.g. v0.20261014.2, and reports itself as
// 0.20261014.2. Go needs a /vN module path for majors above 1, so the
// major stays 0. Compatibility isn't in the version: it's the syntax
// version (internal/syntax).

// Set at build time by the justfile:
//
//	-X github.com/gates-brightly/swimlane/internal/version.BuildDate=20261009
//	-X github.com/gates-brightly/swimlane/internal/version.Commit=64a0e07
//	-X github.com/gates-brightly/swimlane/internal/version.BuildTag=v0.20261009.1  (HEAD is a release)
//	-X github.com/gates-brightly/swimlane/internal/version.Recent=<base64>  (the last commits: package changelog, Commits)
var (
	BuildDate = ""
	Commit    = ""
	BuildTag  = ""
	Recent    = ""
)

// TestSyntax, set at build time (e.g. 3), builds a swim that says it
// reads up to that syntax instead of its own: the tests run it as a worker
// of an older build. A release never sets it.
var TestSyntax = ""

// InstallCmd is how users get a current swim.
const InstallCmd = "go install github.com/gates-brightly/swimlane/cmd/swim@latest"

// RepoWeb is swim's repository on the web.
const RepoWeb = "https://github.com/gates-brightly/swimlane"

// ParseTag splits a release tag ("v0.20261009.2") into its date and index.
func ParseTag(tag string) (date string, index string, ok bool) {
	v, isTag := strings.CutPrefix(tag, "v")
	if !isTag {
		return "", "", false
	}
	return ParseVersion(v)
}

// ParseVersion splits a release version ("0.20261009.2") into its date and
// index (no leading zero, 1 or more).
func ParseVersion(v string) (date string, index string, ok bool) {
	p := strings.Split(v, ".")
	if len(p) != 3 || p[0] != "0" || len(p[1]) != 8 || !chars.Digits(p[1]) || !chars.Digits(p[2]) || p[2][0] == '0' {
		return "", "", false
	}
	return p[1], p[2], true
}

// pseudoDate is the date of a Go pseudo-version
// (v0.0.0-20261015123456-abcdef123456), "" if it isn't one.
func pseudoDate(v string) string {
	p := strings.Split(v, "-")
	for i := 1; i+1 < len(p); i++ {
		if len(p[i]) == 14 && chars.Digits(p[i]) && len(p[i+1]) >= 12 && chars.All(p[i+1][:12], chars.Or(chars.Digit, chars.In("abcdef"))) {
			return p[i][:8]
		}
	}
	return ""
}

// release is the release tag this binary was built from: the justfile's
// BuildTag, or Go's build info (`go install ...@v0.20261009.2` or @latest).
func release() (date, index string, ok bool) {
	if d, i, ok := ParseTag(BuildTag); ok {
		return d, i, true
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		return ParseTag(bi.Main.Version)
	}
	return "", "", false
}

// ReleaseTag is the release tag this binary was built from
// ("v0.20261009.2"), or "" for a build between releases.
func ReleaseTag() string {
	if d, i, ok := release(); ok {
		return "v0." + d + "." + i
	}
	return ""
}

// Date returns the build date as YYYYMMDD: the release's, the justfile's,
// else from Go's build info (VCS time for a checkout build, or a
// pseudo-version's timestamp for an untagged commit), else "dev".
func Date() string {
	if d, _, ok := release(); ok {
		return d
	}
	if BuildDate != "" {
		return BuildDate
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.time" {
				if t, err := time.Parse(time.RFC3339, s.Value); err == nil {
					return t.UTC().Format("20060102")
				}
			}
		}
		if d := pseudoDate(bi.Main.Version); d != "" {
			return d
		}
	}
	return "dev"
}

// String is the version: "0.20261009.2" for a release, "0.20261009" for a
// build between releases.
func String() string {
	if d, i, ok := release(); ok {
		return "0." + d + "." + i
	}
	return "0." + Date()
}

// Long adds the commit when known, e.g. "0.20261009.2 (64a0e07)".
func Long() string {
	c := Commit
	if c == "" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, s := range bi.Settings {
				if s.Key == "vcs.revision" && len(s.Value) >= 7 {
					c = s.Value[:7]
				}
			}
		}
	}
	if c == "" {
		return String()
	}
	return String() + " (" + c + ")"
}
