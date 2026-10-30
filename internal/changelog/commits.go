package changelog

import (
	"encoding/base64"
	"sort"
	"strings"
)

// The justfile stamps a build with its last commits (version.Recent): git
// log -5 --no-merges --name-only --format='%x1e%h%x1f%as%x1f%s%x1f%b%x1f',
// base64. Each record is a commit's hash, date, subject, body and then the
// files it changed, one per line. A build without git (go build, the Nix
// flake) has none, and swim changelog shows CHANGELOG.md's newest revision
// instead.

// Commit is one commit a build was stamped with.
type Commit struct {
	Hash, Date, Subject string
	// Summary is the body's first paragraph (trailers such as
	// Co-Authored-By: dropped): what the commit is about, not all of it.
	Summary string
	// Areas are the parts of swim it changed, from its files' paths
	// (Area), with how many files in each, most first.
	Areas []AreaCount
}

// AreaCount is an area a commit changed and how many of its files.
type AreaCount struct {
	Area  string
	Files int
}

// Commits decodes a version.Recent stamp, newest first; nil if it's empty
// or isn't one.
func Commits(stamp string) []Commit {
	if stamp == "" {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(stamp)
	if err != nil {
		return nil
	}
	var out []Commit
	for _, rec := range strings.Split(string(raw), "\x1e") {
		f := strings.Split(rec, "\x1f")
		if len(f) < 4 || f[0] == "" {
			continue
		}
		c := Commit{Hash: f[0], Date: f[1], Subject: strings.TrimSpace(f[2]), Summary: summary(f[3])}
		if len(f) > 4 {
			c.Areas = areas(strings.Fields(f[4]))
		}
		out = append(out, c)
	}
	return out
}

// Area is the part of swim a file belongs to: its package under internal/
// ("internal/cli"), a skill ("skills/swim"), else its top directory
// ("docs") or, at the root, the file itself ("CHANGELOG.md").
func Area(path string) string {
	parts := strings.Split(path, "/")
	switch {
	case len(parts) == 1:
		return path
	case len(parts) > 2 && (parts[0] == "internal" || parts[0] == "skills" || parts[0] == "cmd"):
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}

func areas(files []string) []AreaCount {
	n := map[string]int{}
	for _, f := range files {
		n[Area(f)]++
	}
	var out []AreaCount
	for a, c := range n {
		out = append(out, AreaCount{a, c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Files != out[j].Files {
			return out[i].Files > out[j].Files
		}
		return out[i].Area < out[j].Area
	})
	return out
}

// summary is a commit body's first paragraph that isn't trailers.
func summary(body string) string {
	for _, para := range strings.Split(strings.TrimSpace(body), "\n\n") {
		var lines []string
		for _, l := range strings.Split(strings.TrimSpace(para), "\n") {
			if !trailer(l) {
				lines = append(lines, strings.TrimRight(l, " "))
			}
		}
		if len(lines) > 0 {
			return strings.Join(lines, "\n")
		}
	}
	return ""
}

func trailer(l string) bool {
	k, _, ok := strings.Cut(l, ": ")
	if !ok || k == "" || strings.ContainsAny(k, " \t") {
		return false
	}
	switch strings.ToLower(k) {
	case "co-authored-by", "signed-off-by", "reviewed-by", "change-id":
		return true
	}
	return false
}
