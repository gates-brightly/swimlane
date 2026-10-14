package lane

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/gates-brightly/swimlane/internal/syntax"
)

// A group file, lanes.<name>.sh at the repo root, holds several bash lanes
// in one file: standing jobs (fmt, lint, test) that would otherwise each
// take a lane.N.sh. Each lane is a section that starts at a marker line,
// "# swim lane N", followed by its header (Round:, Job:, Always:, After:,
// ...) and its steps:
//
//	#!/usr/bin/env bash
//	# swim: syntax 2
//	# What the group is for (comments before the first lane are prose;
//	# code there is shared by every lane, and runs after lane_init).
//
//	# swim lane 1 ---------------------------------------------------------
//	# Round:  CI: fmt
//	# Always: yes
//	gate "gofmt -s" test -z "$(gofmt -s -l .)"
//
//	# swim lane 2 ---------------------------------------------------------
//	# Round:  CI: lint
//	...
//
// swim loads its library, calls lane_init and summary for a section that
// doesn't, and writes each lane's script to .swim/groups/lane.N.sh, which
// is what runs, is hashed for approvals and is kept in the chain; everything
// else about the lane is the same as a lane.N.sh. Edit the group file, never
// the generated script; swim leaves a running lane's script alone.

// GroupFileRE matches a group file's name.
var GroupFileRE = regexp.MustCompile(`^lanes\.([A-Za-z0-9_-]+)\.sh$`)

var (
	groupMarkRE   = regexp.MustCompile(`^#[ \t]*swim lane[ \t]+([0-9]+)\b`)
	sectionLibRE  = regexp.MustCompile(`(?:^|[\s;&|(])lane_init\b`)
	sectionSumRE  = regexp.MustCompile(`(?:^|[\s;&|(])summary(?:$|[\s;&|)])`)
	groupLibLines = []string{`_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"`}
)

// GroupLane is one lane of a group file.
type GroupLane struct {
	N    int
	File string // the group file's name, lanes.<name>.sh
	Line int    // its marker's line in File (1-based)
	// Src is the lane script swim generates from the section; Lines maps
	// each of its lines (0-based) to a line of File (1-based; 0 for a line
	// swim added).
	Src   string
	Lines []int
}

// GroupLine is the line of the group file that line (1-based) of the
// generated script came from: the section's marker for a line swim added.
func (g GroupLane) GroupLine(line int) int {
	if line >= 1 && line <= len(g.Lines) && g.Lines[line-1] > 0 {
		return g.Lines[line-1]
	}
	return g.Line
}

// GroupProblem is something wrong with a group file as a whole.
type GroupProblem struct {
	File    string
	Line    int
	Message string
}

func (p GroupProblem) Error() string {
	if p.Line > 0 {
		return fmt.Sprintf("%s:%d: %s", p.File, p.Line, p.Message)
	}
	return fmt.Sprintf("%s: %s", p.File, p.Message)
}

// GroupFiles lists root's group files, by name.
func GroupFiles(root string) []string {
	entries, _ := os.ReadDir(root)
	var out []string
	for _, e := range entries {
		if !e.IsDir() && GroupFileRE.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// Groups parses root's group files: the lanes they define, and what's wrong
// with them (a lane defined twice; it keeps its first definition).
func Groups(root string) (map[int]GroupLane, []GroupProblem) {
	lanes := map[int]GroupLane{}
	var problems []GroupProblem
	for _, name := range GroupFiles(root) {
		gls, ps := parseGroupFile(filepath.Join(root, name))
		problems = append(problems, ps...)
		for _, g := range gls {
			if prev, dup := lanes[g.N]; dup {
				problems = append(problems, GroupProblem{name, g.Line, fmt.Sprintf("swim %d is already defined at %s:%d; a lane is defined once", g.N, prev.File, prev.Line)})
				continue
			}
			lanes[g.N] = g
		}
	}
	return lanes, problems
}

// GroupOf is lane n's section, if a group file defines it.
func GroupOf(root string, n int) (GroupLane, bool) {
	gs, _ := Groups(root)
	g, ok := gs[n]
	return g, ok
}

// GroupScript is where swim writes a grouped lane's script.
func GroupScript(root string, n int) string {
	return filepath.Join(root, ".swim", "groups", fmt.Sprintf("lane.%d.sh", n))
}

// writeGroupScript brings lane n's generated script up to date with its
// section. A running lane's script is left alone (never edit a running
// lane); replacing it goes through a rename, so a reader never sees half.
func writeGroupScript(root string, g GroupLane) string {
	dst := GroupScript(root, g.N)
	if old, err := os.ReadFile(dst); err == nil && bytes.Equal(old, []byte(g.Src)) {
		return dst
	}
	if _, running := Running(root, g.N); running {
		return dst
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return dst
	}
	tmp := fmt.Sprintf("%s.%d.tmp", dst, os.Getpid())
	if err := os.WriteFile(tmp, []byte(g.Src), 0o755); err == nil {
		if os.Rename(tmp, dst) != nil {
			os.Remove(tmp)
		}
	}
	return dst
}

type groupCacheEntry struct {
	size  int64
	mod   int64
	lanes []GroupLane
	probs []GroupProblem
}

var (
	groupCacheMu sync.Mutex
	groupCache   = map[string]groupCacheEntry{}
)

// parseGroupFile reads one group file (cached while it's unchanged).
func parseGroupFile(path string) ([]GroupLane, []GroupProblem) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, nil
	}
	groupCacheMu.Lock()
	c, ok := groupCache[path]
	groupCacheMu.Unlock()
	if ok && c.size == st.Size() && c.mod == st.ModTime().UnixNano() {
		return c.lanes, c.probs
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, []GroupProblem{{filepath.Base(path), 0, err.Error()}}
	}
	lanes, probs := ParseGroup(filepath.Base(path), string(data))
	groupCacheMu.Lock()
	groupCache[path] = groupCacheEntry{st.Size(), st.ModTime().UnixNano(), lanes, probs}
	groupCacheMu.Unlock()
	return lanes, probs
}

// ParseGroup splits a group file's source into its lanes.
func ParseGroup(name, src string) ([]GroupLane, []GroupProblem) {
	lines := strings.Split(strings.TrimSuffix(src, "\n"), "\n")
	var marks []int // 0-based lines of the markers
	for i, l := range lines {
		if groupMarkRE.MatchString(strings.TrimSpace(l)) {
			marks = append(marks, i)
		}
	}
	var probs []GroupProblem
	if len(marks) == 0 {
		return nil, []GroupProblem{{name, 0, `no lanes: start each one with a "# swim lane N" line`}}
	}
	syn := max(syntax.OfScript(src), syntax.Current)

	// Before the first lane: prose, the syntax line, and code every lane
	// shares. Header keys there would belong to no lane.
	var shared []int
	for i := 0; i < marks[0]; i++ {
		t := strings.TrimSpace(lines[i])
		switch {
		case t == "" || i == 0 && strings.HasPrefix(t, "#!"):
		case strings.HasPrefix(t, "#"):
			if m := keyRE.FindStringSubmatch(t); m != nil && knownKeys[strings.ToLower(m[1])] && m[1] != "swim" {
				probs = append(probs, GroupProblem{name, i + 1, fmt.Sprintf("`%s:` before the first lane belongs to no lane; move it under a \"# swim lane N\" line", m[1])})
			}
		default:
			shared = append(shared, i)
		}
	}

	var out []GroupLane
	seen := map[int]int{}
	for k, m := range marks {
		end := len(lines)
		if k+1 < len(marks) {
			end = marks[k+1]
		}
		n, _ := strconv.Atoi(groupMarkRE.FindStringSubmatch(strings.TrimSpace(lines[m]))[1])
		if n <= 0 {
			probs = append(probs, GroupProblem{name, m + 1, "lanes are numbered from 1"})
			continue
		}
		if prev, dup := seen[n]; dup {
			probs = append(probs, GroupProblem{name, m + 1, fmt.Sprintf("swim %d is already defined at line %d; a lane is defined once", n, prev)})
			continue
		}
		seen[n] = m + 1
		// The section's header: the comment lines (and blank lines) up to
		// its first line of code.
		body := m + 1
		for body < end {
			t := strings.TrimSpace(lines[body])
			if t != "" && !strings.HasPrefix(t, "#") {
				break
			}
			body++
		}
		g := GroupLane{N: n, File: name, Line: m + 1}
		add := func(text string, from int) {
			g.Src += text + "\n"
			g.Lines = append(g.Lines, from)
		}
		add("#!/usr/bin/env bash", 0)
		add(fmt.Sprintf("# swim: syntax %d", syn), 0)
		for i := m + 1; i < body; i++ {
			add(lines[i], i+1)
		}
		add(fmt.Sprintf("# Generated by swim from %s (swim lane %d): edit that file, not this one.", name, n), 0)
		if !codeMatches(lines[m+1:end], sectionLibRE) {
			for _, l := range groupLibLines {
				add(l, 0)
			}
			add(fmt.Sprintf("lane_init %d", n), 0)
		}
		for _, i := range shared {
			add(lines[i], i+1)
		}
		for i := body; i < end; i++ {
			add(lines[i], i+1)
		}
		if !codeMatches(lines[m+1:end], sectionSumRE) {
			add("summary", 0)
		}
		out = append(out, g)
	}
	return out, probs
}

// codeMatches reports whether a line of code (not a comment) matches re.
func codeMatches(lines []string, re *regexp.Regexp) bool {
	for _, l := range lines {
		if t := strings.TrimSpace(l); !strings.HasPrefix(t, "#") && re.MatchString(t) {
			return true
		}
	}
	return false
}
