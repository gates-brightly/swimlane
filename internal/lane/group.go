package lane

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/gates-brightly/swimlane/internal/syntax"
)

// Jobs: any bash lane script can be split into jobs, numbered under the
// lane: 1.1, 1.2, and nested to any depth (1.5, 1.5.9, 1.5.9.9). Before the
// first job comes the lane's header (Round:, Job:, After:, Locks:, ...) and
// code every job shares; each job starts at a "# swim job N.K[.K...]" line,
// then its own header (Round:, Job:, Always:, Timeout:, ...) and steps:
//
//	#!/usr/bin/env bash
//	# swim: syntax 2
//	# Round: CI
//
//	# swim job 1.1 --------------------------------------------------------
//	# Round:  fmt
//	# Always: yes
//	gate "gofmt -s" test -z "$(gofmt -s -l .)"
//
//	# swim job 1.2 --------------------------------------------------------
//	# Round:  tests
//
//	# swim job 1.2.1 ------------------------------------------------------
//	# Round:  unit
//	run "go test" go test ./...
//
// A lane.N.sh with job markers is such a lane. So is a group file,
// lanes.<name>.sh at the repo root: a lane of jobs with a name rather than
// a number (it's the lane its jobs name), for standing jobs (fmt, lint,
// test) worth committing.
//
// A job's id says where it sits: 1.2.1 is under 1.2. A job with steps runs
// as one of the lane's cells (matrix.go), in its own process with its own
// log (agent1.2.1.log), status entry and job id; one with only a header (or
// no section at all) groups the jobs under it, and passes its Always: on
// to them. Jobs run one at a time in file order, and the first failure
// skips the rest of the lane. A job id names it and every job under it:
// `swim run 1.2` runs them all, `After: 1.2` waits for them all to pass.
// Always jobs are setup: every other lane in the run waits for the lane's
// Always jobs (not the rest of it), and swim all runs them every time,
// plus the jobs that haven't passed.
//
// swim loads its library, calls lane_init and summary for a job that
// doesn't, and runs the script it generates at .swim/groups/lane.N.<job>.sh.
// The lane's hash (approvals, queue pins, the chain) is its file's.
//
// A lane in another language (lane.N.py, .ts, .go, ...) marks its jobs the
// same way, in its own comments ("# swim job 1.2", "// swim job 1.2"), but
// isn't split: each job runs the whole program, with SWIM_CELL set to its
// path, and the SDK's jobs() calls that job's function. Every job with no
// job under it runs; one with jobs under it only groups them.

// IsGroupFile reports whether name is a group file's: lanes.<name>.sh,
// the name letters, digits, _ and -.
func IsGroupFile(name string) bool {
	mid, ok := strings.CutPrefix(name, "lanes.")
	if !ok {
		return false
	}
	mid, ok = strings.CutSuffix(mid, ".sh")
	return ok && mid != "" && allOf(mid, func(r rune) bool { return isLetter(r) || isDigit(r) || r == '_' || r == '-' })
}

// jobMark reads a job's marker line (trimmed): "# swim job 1.2", with
// "finally" after it for a finally job ("# swim job 1.9 finally"). n is the
// lane, path the job's place under it (2, 5.9).
func jobMark(line string) (n int, path string, finally, ok bool) {
	rest, found := strings.CutPrefix(line, "#")
	if !found {
		return 0, "", false, false
	}
	rest, found = strings.CutPrefix(strings.TrimLeft(rest, " \t"), "swim job")
	if !found || rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
		return 0, "", false, false
	}
	rest = strings.TrimLeft(rest, " \t")
	end := 0
	for end < len(rest) && (isDigit(rune(rest[end])) || rest[end] == '.') {
		end++
	}
	id, after := rest[:end], rest[end:]
	first, sub, dotted := strings.Cut(id, ".")
	if !dotted || !isDigits(first) || !IsJobPath(sub) {
		return 0, "", false, false
	}
	n, _ = strconv.Atoi(first)
	if t := strings.TrimLeft(after, " \t"); len(t) < len(after) && len(t) >= len("finally") && strings.EqualFold(t[:len("finally")], "finally") {
		next := strings.TrimPrefix(strings.ToLower(t), "finally")
		finally = next == "" || !(isLetter(rune(next[0])) || isDigit(rune(next[0])) || next[0] == '_')
	}
	return n, sub, finally, true
}

// isJobMark reports whether a (trimmed) line is a job's marker.
func isJobMark(line string) bool {
	_, _, _, ok := jobMark(line)
	return ok
}

var (
	groupLibLines = []string{`_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"`}
	// Keys that belong to the lane (before the first job), and to a job.
	laneOnlyKeys = map[string]bool{"after": true, "finally": true, "locks": true, "rerun-children": true, "includes": true, "cache": true,
		"matrix": true, "matrix-exclude": true, "matrix-parallel": true, "matrix-fail": true}
	subOnlyKeys = map[string]bool{"always": true}
)

// IsJobPath reports whether s is a job's place under its lane: "2",
// "5.9.9".
func IsJobPath(s string) bool {
	for _, part := range strings.Split(s, ".") {
		if !isDigits(part) {
			return false
		}
	}
	return true
}

// HasJobs reports whether a lane script's source is split into jobs.
func HasJobs(src string) bool {
	for _, l := range strings.Split(src, "\n") {
		if isJobMark(strings.TrimSpace(l)) {
			return true
		}
	}
	return false
}

// Group is a lane split into jobs: its file, header and runnable jobs.
type Group struct {
	N    int
	File string // lanes.<name>.sh, lane.N.sh, or lane.N.<ext> (Native)
	// Native: a lane in another language; its jobs run the whole program
	// (the SDK picks the job's function), so GroupSub.Src is empty.
	Native   bool
	Info     Info // the lane: its header, with Subs filled in
	Subs     []GroupSub
	Problems []GroupProblem
}

// GroupSub is one runnable job (one with steps), in file order.
type GroupSub struct {
	Path string // "5.9" for job 1.5.9
	Line int    // its "# swim job" line in the file (1-based)
	// Src is the script swim generates for it; Lines maps each of its lines
	// (0-based) to a line of the file (1-based; 0 for a line swim added).
	Src   string
	Lines []int
}

// Name is the job's cell name: its path under the lane ("5.9").
func (s GroupSub) Name() string { return s.Path }

// GroupLine is the file line that line (1-based) of the generated script
// came from: the job's marker for a line swim added.
func (s GroupSub) GroupLine(line int) int {
	if line >= 1 && line <= len(s.Lines) && s.Lines[line-1] > 0 {
		return s.Lines[line-1]
	}
	return s.Line
}

// Sub finds a runnable job by its path ("5.9").
func (g Group) Sub(name string) (GroupSub, bool) {
	for _, s := range g.Subs {
		if s.Path == name {
			return s, true
		}
	}
	return GroupSub{}, false
}

// SubJob is a runnable job as the lane's Info carries it.
type SubJob struct {
	Name   string // "5.9" for job 1.5.9
	Info   Info   // its own header (Round, Job, Timeout, ...; Always inherited)
	Native bool   // it runs the lane's whole program (Group.Native)
}

// IsGroup reports whether the lane is split into jobs.
func (i Info) IsGroup() bool { return len(i.Subs) > 0 }

// NativeJobs reports whether the lane's jobs each run its whole program
// (a lane in another language; its SDK dispatches to the job).
func (i Info) NativeJobs() bool { return i.IsGroup() && i.Subs[0].Native }

// AlwaysCells are a lane's Always jobs (their own Always:, or an
// enclosing job's), by path.
func (i Info) AlwaysCells() []string {
	var out []string
	for _, s := range i.Subs {
		if s.Info.Always {
			out = append(out, s.Name)
		}
	}
	return out
}

// CellsUnder is the cells a reference names: for a lane of jobs, the job
// and every job under it ("5" covers 5, 5.1, 5.9.9); for a matrix, the
// cell (Cell). Empty if it names none.
func (i Info) CellsUnder(ref string) []Cell {
	if !i.IsGroup() {
		if c, ok := i.Cell(ref); ok {
			return []Cell{c}
		}
		return nil
	}
	var out []Cell
	for _, s := range i.Subs {
		if PathUnder(s.Name, ref) {
			out = append(out, Cell{Name: s.Name})
		}
	}
	return out
}

// PathUnder reports whether job path p is ref or under it ("5.9" is under
// "5"; "51" isn't).
func PathUnder(p, ref string) bool { return p == ref || strings.HasPrefix(p, ref+".") }

// SubLabel is how a cell of lane n reads in messages: "1.5.9" for a job,
// "3[orders]" for a matrix cell.
func (i Info) SubLabel(n int, cell string) string {
	if i.IsGroup() {
		return fmt.Sprintf("%d.%s", n, cell)
	}
	return fmt.Sprintf("%d[%s]", n, cell)
}

// GroupProblem is something wrong with a lane's jobs.
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
		if !e.IsDir() && IsGroupFile(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// Groups parses root's group files: the lanes they define, and what's wrong
// with them (a lane two files define keeps the first).
func Groups(root string) (map[int]Group, []GroupProblem) {
	lanes := map[int]Group{}
	var problems []GroupProblem
	for _, name := range GroupFiles(root) {
		g, ok := parseJobsFile(filepath.Join(root, name), 0)
		problems = append(problems, g.Problems...)
		if !ok {
			continue
		}
		if prev, dup := lanes[g.N]; dup {
			problems = append(problems, GroupProblem{name, g.Subs[0].Line, fmt.Sprintf("swim %d is already %s; a lane is defined once", g.N, prev.File)})
			continue
		}
		lanes[g.N] = g
	}
	return lanes, problems
}

// GroupOf is the group file that defines lane n, if one does.
func GroupOf(root string, n int) (Group, bool) {
	gs, _ := Groups(root)
	g, ok := gs[n]
	return g, ok
}

// LaneJobs is lane n's jobs, if its script is split into them: its group
// file, or a lane.N.sh with job markers.
func LaneJobs(root string, n int) (Group, bool) {
	files := scriptFiles(root, n)
	if len(files) == 0 {
		return GroupOf(root, n)
	}
	if len(files) > 1 {
		return Group{}, false
	}
	return parseJobsFile(files[0], n)
}

// SubScript is where swim writes job <path> of lane n.
func SubScript(root string, n int, path string) string {
	return filepath.Join(root, ".swim", "groups", fmt.Sprintf("lane.%d.%s.sh", n, path))
}

// SubScriptFor is the script a cell of lane n runs, brought up to date with
// the lane's file ("", false if lane n has no such job).
func SubScriptFor(root string, n int, cell string) (string, bool) {
	g, ok := LaneJobs(root, n)
	if !ok || g.Native {
		return "", false // a native lane's jobs run its own program
	}
	s, ok := g.Sub(cell)
	if !ok {
		return "", false
	}
	return writeSubScript(root, n, s), true
}

// writeSubScript brings a job's generated script up to date. A running
// lane's scripts are left alone (never edit a running lane); replacing one
// goes through a rename, so a reader never sees half.
func writeSubScript(root string, n int, s GroupSub) string {
	dst := SubScript(root, n, s.Path)
	if old, err := os.ReadFile(dst); err == nil && bytes.Equal(old, []byte(s.Src)) {
		return dst
	}
	if _, running := Running(root, n); running {
		return dst
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return dst
	}
	tmp := fmt.Sprintf("%s.%d.tmp", dst, os.Getpid())
	if err := os.WriteFile(tmp, []byte(s.Src), 0o755); err == nil {
		if os.Rename(tmp, dst) != nil {
			os.Remove(tmp)
		}
	}
	return dst
}

type groupCacheEntry struct {
	size, mod int64
	lane      int
	g         Group
	ok        bool
}

var (
	groupCacheMu sync.Mutex
	groupCache   = map[string]groupCacheEntry{}
)

// parseJobsFile reads a lane file's jobs (cached while it's unchanged);
// lane is its number, or 0 for a group file (its jobs name it). ok is
// false for a file without jobs.
func parseJobsFile(path string, lane int) (Group, bool) {
	st, err := os.Stat(path)
	if err != nil {
		return Group{}, false
	}
	groupCacheMu.Lock()
	c, hit := groupCache[path]
	groupCacheMu.Unlock()
	if hit && c.lane == lane && c.size == st.Size() && c.mod == st.ModTime().UnixNano() {
		return c.g, c.ok
	}
	name := filepath.Base(path)
	data, err := os.ReadFile(path)
	if err != nil {
		return Group{File: name, Problems: []GroupProblem{{name, 0, err.Error()}}}, false
	}
	var g Group
	ok := false
	if IsBinary(data) {
		// A compiled lane: no comments to mark jobs in.
	} else if lane == 0 || HasJobs(commentsAsHash(string(data))) {
		g, ok = ParseGroup(name, string(data), lane)
		if ok && g.Info.Created == "" {
			g.Info.Created = st.ModTime().Format("2006-01-02")
		}
	}
	groupCacheMu.Lock()
	groupCache[path] = groupCacheEntry{st.Size(), st.ModTime().UnixNano(), lane, g, ok}
	groupCacheMu.Unlock()
	return g, ok
}

// ParseGroup reads a lane file split into jobs: the lane's header and its
// jobs. lane is the file's lane number, or 0 for a group file (its jobs
// name it). ok is false when it has no runnable job.
func ParseGroup(name, src string, lane int) (Group, bool) {
	g := Group{File: name, N: lane, Native: !strings.HasSuffix(name, ".sh")}
	if g.Native {
		src = commentsAsHash(src) // "// swim job 1.2" reads as "# swim job 1.2"
	}
	lines := strings.Split(strings.TrimSuffix(src, "\n"), "\n")
	var marks []int // 0-based lines of the markers
	for i, l := range lines {
		if isJobMark(strings.TrimSpace(l)) {
			marks = append(marks, i)
		}
	}
	if len(marks) == 0 {
		g.Problems = append(g.Problems, GroupProblem{name, 0, `no jobs: start each one with a "# swim job N.K" line (N the lane, K the job)`})
		return g, false
	}
	syn := max(syntax.OfScript(src), syntax.Current)

	// The lane: header comments before the first job, and code every job
	// shares (run after lane_init).
	var header []string
	var shared []int
	for i := 0; i < marks[0]; i++ {
		t := strings.TrimSpace(lines[i])
		switch {
		case t == "" || i == 0 && strings.HasPrefix(t, "#!"):
		case strings.HasPrefix(t, "#"):
			header = append(header, lines[i])
			if k, _, ok := headerKey(t, false); ok && subOnlyKeys[strings.ToLower(k)] {
				g.Problems = append(g.Problems, GroupProblem{name, i + 1, fmt.Sprintf("`%s:` belongs on a job (under its \"# swim job\" line), not on the lane", k)})
			}
		default:
			shared = append(shared, i)
		}
	}
	laneInfo := ParseScript(strings.Join(header, "\n") + "\n")
	laneInfo.Syntax = syntax.OfScript(src)
	laneInfo.RerunChildren = false
	var problems []string
	for _, p := range laneInfo.Problems {
		if !strings.HasPrefix(p, "Rerun-Children:") {
			problems = append(problems, p)
		}
	}
	laneInfo.Problems = problems
	for _, h := range header {
		if k, v, ok := headerKey(strings.TrimSpace(h), false); ok && k == "Rerun-Children" {
			laneInfo.RerunChildren = laneInfo.setBool(v, "Rerun-Children:")
		}
	}
	laneInfo.Matrix, laneInfo.MatrixExclude = nil, nil
	laneInfo.MatrixParallel, laneInfo.MatrixFail = 1, "stop"
	if laneInfo.Round == "" && IsGroupFile(name) {
		laneInfo.Round = strings.TrimSuffix(strings.TrimPrefix(name, "lanes."), ".sh")
	}

	// Every job's section, then the runnable ones (with steps) in file order.
	type section struct {
		path       string
		line, body int // marker, first line of code (0-based)
		end        int
		info       Info
		steps      bool
	}
	var secs []section
	seen := map[string]int{}
	for k, m := range marks {
		end := len(lines)
		if k+1 < len(marks) {
			end = marks[k+1]
		}
		n, path, finally, _ := jobMark(strings.TrimSpace(lines[m]))
		id := fmt.Sprintf("%d.%s", n, path)
		if n <= 0 || zeroPart(path) {
			g.Problems = append(g.Problems, GroupProblem{name, m + 1, fmt.Sprintf("swim job %s: lanes and jobs are numbered from 1", id)})
			continue
		}
		if g.N == 0 {
			g.N = n
		} else if n != g.N {
			g.Problems = append(g.Problems, GroupProblem{name, m + 1, fmt.Sprintf("swim job %s: this file is lane %d, so its jobs are %d.K", id, g.N, g.N)})
			continue
		}
		if prev, dup := seen[path]; dup {
			g.Problems = append(g.Problems, GroupProblem{name, m + 1, fmt.Sprintf("swim job %s is already defined at line %d; a job is defined once", id, prev)})
			continue
		}
		seen[path] = m + 1
		// The job's header: its comment (and blank) lines up to its first
		// line of code.
		body := m + 1
		for body < end {
			t := strings.TrimSpace(lines[body])
			if t != "" && !strings.HasPrefix(t, "#") {
				break
			}
			if k, _, ok := headerKey(t, false); ok && laneOnlyKeys[strings.ToLower(k)] {
				g.Problems = append(g.Problems, GroupProblem{name, body + 1, fmt.Sprintf("`%s:` belongs on the lane (before the first \"# swim job\" line); jobs run in file order", k)})
			}
			body++
		}
		si := ParseScript(strings.Join(lines[m+1:body], "\n") + "\n")
		// "# swim job 1.9 finally": it runs after its earlier siblings end.
		si.FinallyJob = finally
		secs = append(secs, section{path: path, line: m, body: body, end: end, info: si, steps: body < end})
	}
	if g.Native {
		// The program's code isn't the job's: a job runs unless it has jobs
		// under it.
		for k := range secs {
			secs[k].steps = true
			for _, o := range secs {
				if o.path != secs[k].path && PathUnder(o.path, secs[k].path) {
					secs[k].steps = false
				}
			}
		}
	}
	always := map[string]bool{}
	for _, sec := range secs {
		if sec.info.Always {
			always[sec.path] = true
		}
	}
	hasChild := func(p string) bool {
		for _, sec := range secs {
			if sec.path != p && PathUnder(sec.path, p) {
				return true
			}
		}
		return false
	}
	for _, sec := range secs {
		id := fmt.Sprintf("%d.%s", g.N, sec.path)
		if !sec.steps {
			if !hasChild(sec.path) {
				g.Problems = append(g.Problems, GroupProblem{name, sec.line + 1, fmt.Sprintf("swim job %s has no steps and no jobs under it", id)})
			}
			continue
		}
		if g.Native {
			g.Subs = append(g.Subs, GroupSub{Path: sec.path, Line: sec.line + 1})
			si := sec.info
			si.Exists, si.Syntax = true, max(syntax.OfScript(src), syntax.Current)
			si.After, si.Locks, si.Matrix, si.RerunChildren = nil, nil, nil, false
			for p := range always {
				si.Always = si.Always || PathUnder(sec.path, p)
			}
			if len(si.Observe) == 0 {
				si.Observe = laneInfo.Observe // the lane's probes, unless the job has its own
			}
			laneInfo.Subs = append(laneInfo.Subs, SubJob{Name: sec.path, Info: si, Native: true})
			continue
		}
		s := GroupSub{Path: sec.path, Line: sec.line + 1}
		add := func(text string, from int) {
			s.Src += text + "\n"
			s.Lines = append(s.Lines, from)
		}
		add("#!/usr/bin/env bash", 0)
		add(fmt.Sprintf("# swim: syntax %d", syn), 0)
		for i := sec.line + 1; i < sec.body; i++ {
			add(lines[i], i+1)
		}
		add(fmt.Sprintf("# Generated by swim from %s (swim job %s): edit that file, not this one.", name, id), 0)
		if !runsCommand(lines[sec.line+1:sec.end], "lane_init") {
			for _, l := range groupLibLines {
				add(l, 0)
			}
			add(fmt.Sprintf("lane_init %d", g.N), 0)
		}
		for _, i := range shared {
			add(lines[i], i+1)
		}
		for i := sec.body; i < sec.end; i++ {
			add(lines[i], i+1)
		}
		if !runsCommand(lines[sec.line+1:sec.end], "summary") {
			add("summary", 0)
		}
		g.Subs = append(g.Subs, s)
		si := ParseScript(s.Src)
		si.After, si.Locks, si.Matrix, si.RerunChildren = nil, nil, nil, false
		si.FinallyJob = sec.info.FinallyJob
		// Always: its own, or a job it sits under.
		for p := range always {
			si.Always = si.Always || PathUnder(sec.path, p)
		}
		if len(si.Observe) == 0 {
			si.Observe = laneInfo.Observe
		}
		laneInfo.Subs = append(laneInfo.Subs, SubJob{Name: sec.path, Info: si})
	}
	if len(g.Subs) == 0 {
		if len(g.Problems) == 0 {
			g.Problems = append(g.Problems, GroupProblem{name, 0, "no job has steps"})
		}
		return g, false
	}
	// The lane is Always if a job is; its guards, stages and evidence are
	// its jobs'.
	listed := map[string]bool{}
	for _, gd := range laneInfo.Guards {
		listed[gd.Flag] = true
	}
	for _, s := range laneInfo.Subs {
		laneInfo.Always = laneInfo.Always || s.Info.Always
		laneInfo.Staged = laneInfo.Staged || s.Info.Staged
		laneInfo.UsesEvidence = laneInfo.UsesEvidence || s.Info.UsesEvidence
		laneInfo.DynamicGuards += s.Info.DynamicGuards
		for _, gd := range s.Info.Guards {
			if !listed[gd.Flag] {
				listed[gd.Flag] = true
				laneInfo.Guards = append(laneInfo.Guards, gd)
			}
		}
	}
	laneInfo.checkAlways("Rerun-Children:")
	g.Info = laneInfo
	return g, true
}

// commentsAsHash rewrites "//" comment lines as "#" ones, as the header of
// a lane in another language reads (ParseScript does the same).
func commentsAsHash(src string) string {
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		if t := strings.TrimLeft(l, " \t"); strings.HasPrefix(t, "//") {
			lines[i] = "#" + strings.TrimPrefix(t, "//")
		}
	}
	return strings.Join(lines, "\n")
}

// zeroPart reports whether a job path has a 0 in it ("5.0").
func zeroPart(path string) bool {
	for _, p := range strings.Split(path, ".") {
		if n, err := strconv.Atoi(p); err != nil || n <= 0 {
			return true
		}
	}
	return false
}

// runsCommand reports whether any line runs the command name (a
// simple command whose first word it is; comments and quoted text aside).
func runsCommand(lines []string, name string) bool {
	for _, l := range lines {
		for _, c := range Commands(l) {
			if c[0].Text == name {
				return true
			}
		}
	}
	return false
}

// JobsIn reads the jobs of lane n's file called name from its content
// (a staged copy, not the working tree): ok is false for a file without
// jobs.
func JobsIn(name string, data []byte, n int) (Group, bool) {
	if IsBinary(data) {
		return Group{}, false
	}
	if IsGroupFile(name) {
		return ParseGroup(name, string(data), 0)
	}
	if !HasJobs(commentsAsHash(string(data))) {
		return Group{}, false
	}
	return ParseGroup(name, string(data), n)
}
