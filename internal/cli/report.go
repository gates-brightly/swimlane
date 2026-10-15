package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/earmark"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/queue"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/step"
)

// swim report: what changed since a reader last looked (features/report.md
// shipped). The status lanes whose state or job changed, every .swim.log
// line and every lane-log line written since the reader's earmark,
// verbatim, and the steps running now. --earmark then moves the earmark to
// the end of what was printed. Nothing is ever cut from a log: the report
// is small because it is a delta.

type reportOpts struct {
	move, peekList, reset, yaml, noLogs, results, wait bool
	name, since                                        string
	every, timeout                                     time.Duration
	only                                               map[int]bool // narrowed to these lanes (nil: all)
}

func cmdReport(args []string) error {
	var o reportOpts
	var interval, timeout string
	rest, err := flags{bools: map[string]*bool{"earmark": &o.move, "earmarks": &o.peekList, "reset": &o.reset, "yaml": &o.yaml,
		"no-logs": &o.noLogs, "results": &o.results, "wait": &o.wait},
		strs: map[string]*string{"name": &o.name, "since": &o.since, "interval": &interval, "timeout": &timeout}}.parse(args)
	if err != nil {
		return err
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	if o.every, err = durationFlag("interval", interval, pollEvery, "2s"); err != nil {
		return err
	}
	if o.timeout, err = durationFlag("timeout", timeout, 0, "30m"); err != nil {
		return err
	}
	name, err := earmark.Name(o.name)
	if err != nil {
		return usagef("%v", err)
	}
	switch {
	case o.peekList:
		return listEarmarks(root)
	case o.reset:
		if !o.move {
			return usagef("--reset goes with --earmark: swim report --earmark --reset")
		}
		ok, err := earmark.Remove(root, name)
		if err != nil {
			return err
		}
		if ok {
			fmt.Printf("earmark %s removed: the next report starts with the work in progress\n", name)
		} else {
			fmt.Printf("no earmark %s\n", name)
		}
		return nil
	}
	if len(rest) > 0 {
		o.only = map[int]bool{}
		for _, ref := range rest {
			n, err := laneRef(root, cfg, ref, true)
			if err != nil {
				return err
			}
			o.only[n] = true
		}
	}
	cur, err := earmark.Load(root, name)
	if err != nil {
		return err
	}
	if o.since != "" {
		t, err := sinceTime(root, cfg, o.since)
		if err != nil {
			return err
		}
		// --since starts from that time, whatever the earmark says.
		cur = &earmark.Cursor{Name: name, At: t, Lanes: map[string]earmark.Pos{}}
	}
	start := time.Now()
	for {
		r, err := buildReport(root, cfg, name, cur, o)
		if err != nil {
			return err
		}
		if o.wait && !r.anyNew() {
			if o.timeout > 0 && time.Since(start) >= o.timeout {
				return fmt.Errorf("nothing new since %s after %s", or(r.since, "the work in progress"), o.timeout)
			}
			time.Sleep(o.every)
			continue
		}
		if o.yaml {
			err = r.writeYAML(os.Stdout)
		} else {
			r.writeText(os.Stdout)
		}
		if err != nil || !o.move {
			return err
		}
		return earmark.Save(root, r.next)
	}
}

// listEarmarks prints every reader's earmark: name, time, age.
func listEarmarks(root string) error {
	cs, err := earmark.List(root)
	if err != nil {
		return err
	}
	if len(cs) == 0 {
		fmt.Println("no earmarks (swim report --earmark sets one)")
		return nil
	}
	for _, c := range cs {
		age := ""
		if t, err := time.Parse(time.RFC3339, c.At); err == nil {
			age = display.Elapsed(time.Since(t)) + " ago"
		}
		fmt.Printf("%-24s %s  %s\n", c.Name, c.At, age)
	}
	return nil
}

var historyRunRE = regexp.MustCompile(`  run=(\S+)`)

// sinceTime resolves --since: a UTC time, a run id (its first .swim.log
// line) or a job (its first round's start).
func sinceTime(root string, cfg *config.Config, ref string) (string, error) {
	if t, err := time.Parse(time.RFC3339, ref); err == nil {
		return t.UTC().Format(time.RFC3339), nil
	}
	data, _ := os.ReadFile(history.Path(root))
	for _, l := range strings.Split(string(data), "\n") {
		if m := historyRunRE.FindStringSubmatch(l); m != nil && m[1] == ref {
			return strings.Fields(l)[0], nil
		}
	}
	for _, n := range lane.Associated(root) {
		for _, s := range laneStreams(root, n) {
			for _, rd := range s.rounds() {
				if m := roundJobRE.FindStringSubmatch(rd.lines[0]); m != nil && lane.MatchJob(m[1], ref) {
					return rd.start, nil
				}
			}
		}
	}
	return "", fmt.Errorf("--since %q: not a UTC time (2026-10-15T14:02:11Z), a run id or a job", ref)
}

// A stream is one lane log followed across archives: the lane's own, or
// one cell's (or group job's).
type stream struct {
	key     string // "1", or "1[cell]"
	lane    int
	cell    string
	files   []string // archives, oldest round first, then current
	current string
}

// logRound is one round of a lane log.
type logRound struct {
	start string // UTC, from its first line
	lines []string
}

// laneStreams lists lane n's streams: its log and each cell's, each with
// its archives.
func laneStreams(root string, n int) []*stream {
	dir := lane.LogDir(root)
	byKey := map[string]*stream{}
	get := func(cell string) *stream {
		key := strconv.Itoa(n)
		if cell != "" {
			key += "[" + cell + "]"
		}
		if byKey[key] == nil {
			byKey[key] = &stream{key: key, lane: n, cell: cell}
		}
		return byKey[key]
	}
	own := get("")
	own.current = filepath.Join(dir, fmt.Sprintf("agent%d.log", n))
	prefix := fmt.Sprintf("agent%d.", n)
	files, _ := filepath.Glob(filepath.Join(dir, prefix+"*.log"))
	for _, f := range files {
		mid := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), prefix), ".log")
		switch {
		case strings.HasSuffix(mid, ".check") || mid == "check":
		case strings.HasPrefix(mid, "prev-"):
			// agentN.prev-NAME.log, or a cell's agentN.prev-NAME.CELL.log
			s := own
			if i := strings.LastIndex(mid, "."); i > 0 {
				s = get(mid[i+1:])
			}
			s.files = append(s.files, f)
		default:
			get(mid).current = f
		}
	}
	var out []*stream
	for _, s := range byKey {
		sort.SliceStable(s.files, func(i, j int) bool { return firstRoundTime(s.files[i]) < firstRoundTime(s.files[j]) })
		if s.current != "" {
			s.files = append(s.files, s.current)
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

// rounds reads the stream's rounds, complete lines only: a line still
// being written is left for the next report. A round's trailing blank
// lines aren't its own (the next round's start adds one).
func (s *stream) rounds() []logRound {
	var out []logRound
	for _, f := range s.files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if i := bytes.LastIndexByte(data, '\n'); i >= 0 {
			data = data[:i+1]
		} else {
			data = nil
		}
		for _, l := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			if strings.HasPrefix(l, logparse.RoundMark) || strings.HasPrefix(l, "=== ROUND START") {
				out = append(out, logRound{start: roundStart(l)})
			}
			if len(out) > 0 {
				out[len(out)-1].lines = append(out[len(out)-1].lines, l)
			}
		}
	}
	for i := range out {
		ls := out[i].lines
		for len(ls) > 1 && strings.TrimSpace(ls[len(ls)-1]) == "" {
			ls = ls[:len(ls)-1]
		}
		out[i].lines = ls
	}
	return out
}

// roundStart is a round's start time from its first line.
func roundStart(line string) string {
	f := strings.Fields(line)
	switch {
	case strings.HasPrefix(line, logparse.RoundMark) && len(f) >= 3:
		return f[2]
	case len(f) >= 4:
		return f[3]
	}
	return ""
}

func lineSHA(l string) string {
	sum := sha256.Sum256([]byte(l))
	return hex.EncodeToString(sum[:8])
}

// laneSlice is a round, or the part of it read since the earmark.
type laneSlice struct {
	from  int // the first line printed (0: the whole round)
	round logRound
}

// delta is what of stream s is new since pos: the rest of the round pos
// is in, then each round after it. Without pos, the rounds started at or
// after since; with neither, the rounds of the current log (the work in
// progress). end is where the stream now ends.
func (s *stream) delta(pos *earmark.Pos, since string) (out []laneSlice, warn string, end earmark.Pos) {
	all := s.rounds()
	if len(all) > 0 {
		last := all[len(all)-1]
		end = earmark.Pos{Round: last.lines[0], Lines: len(last.lines), SHA: lineSHA(last.lines[len(last.lines)-1])}
	}
	after := func(t string) []laneSlice {
		var o []laneSlice
		for _, r := range all {
			if r.start >= t {
				o = append(o, laneSlice{round: r})
			}
		}
		return o
	}
	switch {
	case pos != nil && pos.Round != "":
		for i := len(all) - 1; i >= 0; i-- {
			if all[i].lines[0] != pos.Round {
				continue
			}
			r := all[i]
			if pos.Lines < 1 || pos.Lines > len(r.lines) || lineSHA(r.lines[pos.Lines-1]) != pos.SHA {
				return after(since), fmt.Sprintf("earmark lost its place in swim %s's log (the round was rewritten); showing rounds since %s", s.key, since), end
			}
			if pos.Lines < len(r.lines) {
				out = append(out, laneSlice{from: pos.Lines, round: r})
			}
			for _, later := range all[i+1:] {
				out = append(out, laneSlice{round: later})
			}
			return out, "", end
		}
		return after(since), fmt.Sprintf("earmark lost its place in swim %s's log (its round %s is gone); showing rounds since %s", s.key, roundStart(pos.Round), since), end
	case since != "":
		return after(since), "", end
	}
	// The work in progress: the current log's rounds.
	cur := s.current
	if cur == "" {
		return nil, "", end
	}
	cs := (&stream{files: []string{cur}}).rounds()
	for _, r := range cs {
		out = append(out, laneSlice{round: r})
	}
	return out, "", end
}

// report is one report, built before anything is printed.
type report struct {
	root, name, branch, head string
	since                    string // the time it starts from ("" for the work in progress)
	cursor                   *earmark.Cursor
	now                      time.Time
	changed                  []statusChange
	unchanged                []string
	locks                    []string
	queue                    string
	history                  []string
	historyWarn              string
	lanes                    []laneReport
	inFlight                 [][]string
	next                     *earmark.Cursor // where the earmark goes
	opts                     reportOpts
}

type statusChange struct {
	Lane        int      `yaml:"lane"`
	Was         string   `yaml:"was,omitempty"`
	Now         string   `yaml:"now"`
	Round       string   `yaml:"round,omitempty"`
	Job         string   `yaml:"job,omitempty"`
	FailedSteps []string `yaml:"failed_steps,omitempty"`
	Reason      string   `yaml:"reason,omitempty"`
	DryRun      []string `yaml:"dry_run,omitempty"`
	line, note  string
}

type laneReport struct {
	key    string
	lane   int
	cell   string
	slices []laneSlice
	warn   string
}

func (r *report) anyNew() bool {
	if len(r.changed) > 0 || len(r.history) > 0 {
		return true
	}
	for _, l := range r.lanes {
		if len(l.slices) > 0 || l.warn != "" {
			return true
		}
	}
	return false
}

var historyLaneRE = regexp.MustCompile(`  swim (\d+)(  |$)`)

func buildReport(root string, cfg *config.Config, name string, cur *earmark.Cursor, o reportOpts) (*report, error) {
	r := &report{root: root, name: name, cursor: cur, now: time.Now().UTC(), opts: o}
	r.next = &earmark.Cursor{Name: name, At: r.now.Format(time.RFC3339), Lanes: map[string]earmark.Pos{}}
	if cur != nil {
		r.since = cur.At
		r.next.History = cur.History
		for k, v := range cur.Lanes {
			r.next.Lanes[k] = v
		}
	}
	want := func(n int) bool { return o.only == nil || o.only[n] }

	// Status, refreshed as swim status does.
	if err := refreshStatus(root, cfg); err != nil {
		return nil, err
	}
	f, err := status.Load(root)
	if err != nil {
		return nil, err
	}
	f.Normalize(cfg.Lanes)
	r.branch = f.Branch
	r.head = gitRevParse(root, "--short", "HEAD")
	for _, h := range lane.HeldLocks(root) {
		r.locks = append(r.locks, h.Name+" held by "+h.By)
	}
	for i := range f.Lanes {
		l := &f.Lanes[i]
		if !cfg.IsActive(l.Lane) || !want(l.Lane) {
			continue
		}
		key := strconv.Itoa(l.Lane)
		state := effectiveState(l)
		word := stateWord(l, state, r.now)
		round := l.Round
		if state == status.Idle {
			round = ""
		}
		p := r.next.Lanes[key]
		p.State, p.Job = l.State, l.Job
		r.next.Lanes[key] = p
		var prev *earmark.Pos
		if cur != nil {
			if pp, ok := cur.Lanes[key]; ok {
				prev = &pp
			}
		}
		if prev != nil && prev.State == l.State && prev.Job == l.Job {
			r.unchanged = append(r.unchanged, fmt.Sprintf("swim %d %s", l.Lane, word))
			continue
		}
		c := statusChange{Lane: l.Lane, Now: l.State, Round: round, Job: l.Job, FailedSteps: l.FailedSteps, Reason: l.Reason, DryRun: l.DryRun}
		c.line = fmt.Sprintf("swim %-2d %-22s %s", l.Lane, word, round)
		switch {
		case cur == nil:
		case prev == nil || prev.State == "":
			c.note = "(new)"
		case prev.Job != l.Job:
			c.Was, c.note = prev.State, "(new round)"
		default:
			c.Was, c.note = prev.State, "(was "+prev.State+")"
		}
		r.changed = append(r.changed, c)
	}
	if entries, err := queue.List(root, false); err == nil {
		counts := map[string]int{}
		var waiting []string
		for _, e := range entries {
			counts[e.State]++
			if e.State == queue.Queued {
				waiting = append(waiting, fmt.Sprintf("#%d", e.Seq))
			}
		}
		var parts []string
		if n := counts[queue.Queued]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d waiting (%s)", n, strings.Join(waiting, " ")))
		}
		if n := counts[queue.Running]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d running", n))
		}
		if n := counts[queue.Orphaned]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d orphaned", n))
		}
		r.queue = strings.Join(parts, ", ")
	}

	// Lane logs (read with --no-logs too: the earmark moves past them).
	earliest := ""
	{
		for _, n := range lane.Associated(root) {
			if !want(n) {
				continue
			}
			for _, s := range laneStreams(root, n) {
				var pos *earmark.Pos
				if cur != nil {
					if p, ok := cur.Lanes[s.key]; ok && p.Round != "" {
						pos = &p
					}
				}
				slices, warn, end := s.delta(pos, r.since)
				if end.Round != "" {
					p := r.next.Lanes[s.key]
					p.Round, p.Lines, p.SHA = end.Round, end.Lines, end.SHA
					r.next.Lanes[s.key] = p
				}
				for _, sl := range slices {
					if earliest == "" || sl.round.start < earliest {
						earliest = sl.round.start
					}
				}
				if len(slices) > 0 || warn != "" {
					r.lanes = append(r.lanes, laneReport{key: s.key, lane: n, cell: s.cell, slices: slices, warn: warn})
				}
				if s.current != "" {
					if sp, ok := step.ReadSpool(s.current); ok {
						why := "cut off: swim step stopped; recorded as FAIL when the lane next runs"
						if _, running := lane.Running(root, n); running {
							why = "in progress"
						}
						block := sp.Block(why)
						for i := range block {
							block[i] = "[" + s.key + "] " + block[i]
						}
						r.inFlight = append(r.inFlight, block)
					}
				}
			}
		}
	}

	// .swim.log.
	since := r.since
	if since == "" {
		since = earliest
	}
	var hpos *earmark.HistoryPos
	if cur != nil && cur.History.Offset > 0 {
		hpos = &cur.History
	}
	lines, end, warn := historyDelta(root, hpos, since)
	r.historyWarn = warn
	for _, l := range lines {
		if o.only != nil {
			if m := historyLaneRE.FindStringSubmatch(l); m != nil {
				if n, _ := strconv.Atoi(m[1]); !o.only[n] {
					continue
				}
			}
		}
		r.history = append(r.history, l)
	}
	if o.only == nil {
		// Narrowed, the next unnarrowed report still shows the other
		// lanes' lines: the earmark doesn't move past them.
		r.next.History = end
	}
	if r.since == "" {
		r.since = since
	}
	return r, nil
}

// historyDelta returns .swim.log's lines after pos (complete lines only)
// and where it now ends. A file that doesn't match pos (replaced, edited)
// falls back to the lines stamped at or after since.
func historyDelta(root string, pos *earmark.HistoryPos, since string) (lines []string, end earmark.HistoryPos, warn string) {
	data, _ := os.ReadFile(history.Path(root))
	if i := bytes.LastIndexByte(data, '\n'); i >= 0 {
		data = data[:i+1]
	} else {
		data = nil
	}
	end.Offset = int64(len(data))
	if all := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n"); len(data) > 0 {
		end.SHA = lineSHA(all[len(all)-1])
	}
	from := []byte(nil)
	switch {
	case pos != nil && pos.Offset <= int64(len(data)) && lineSHA(lastLine(data[:pos.Offset])) == pos.SHA:
		from = data[pos.Offset:]
		since = ""
	case pos != nil:
		warn = fmt.Sprintf("earmark lost its place in .swim.log (replaced or edited); showing lines since %s", since)
		from = data
	default:
		from = data
	}
	for _, l := range strings.Split(strings.TrimSuffix(string(from), "\n"), "\n") {
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if since != "" {
			if f := strings.Fields(l); len(f) == 0 || f[0] < since {
				continue
			}
		}
		lines = append(lines, l)
	}
	return lines, end, warn
}

// lastLine is data's last line (data ends with a newline).
func lastLine(data []byte) string {
	s := strings.TrimSuffix(string(data), "\n")
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// resultsOnly drops step commands and output (the "        $" and
// "        |" lines), saying after each step how many it dropped.
func resultsOnly(lines []string, ref string) []string {
	var out []string
	dropped := 0
	flush := func() {
		if dropped > 0 {
			out = append(out, fmt.Sprintf("%s(%d lines of command and output: swim log %s)", logparse.Indent, dropped, ref))
			dropped = 0
		}
	}
	for _, l := range lines {
		if strings.HasPrefix(l, logparse.Indent+"$") || strings.HasPrefix(l, logparse.Indent+"|") {
			dropped++
			continue
		}
		flush()
		out = append(out, l)
	}
	flush()
	return out
}

func (r *report) header() string {
	h := "swim report · " + filepath.Base(r.root)
	if r.branch == "-" {
		r.branch = ""
	}
	if r.branch != "" || r.head != "" {
		h += " · " + r.branch
		if r.head != "" {
			h += "@" + r.head
		}
	}
	switch {
	case r.cursor != nil && r.opts.since != "":
		h += " · since " + r.since + " (--since " + r.opts.since + ")"
	case r.cursor != nil:
		h += " · since " + r.since + " (earmark " + r.name + ")"
		if t, err := time.Parse(time.RFC3339, r.cursor.At); err == nil {
			h += " · " + display.Elapsed(r.now.Sub(t)) + " ago"
		}
	default:
		h += " · the work in progress (no earmark " + r.name + " yet)"
	}
	return h
}

func (r *report) writeText(w io.Writer) {
	if !r.anyNew() {
		fmt.Fprintln(w, r.header()+" · nothing new")
	} else {
		fmt.Fprintln(w, r.header())
	}
	fmt.Fprintln(w)
	label := "status"
	for _, c := range r.changed {
		line := c.line
		if c.note != "" {
			line = fmt.Sprintf("%-60s %s", line, c.note)
		}
		fmt.Fprintf(w, "%-8s %s\n", label, strings.TrimRight(line, " "))
		label = ""
		for _, fs := range c.FailedSteps {
			fmt.Fprintf(w, "%-8s   %s\n", "", fs)
		}
		if c.Reason != "" {
			fmt.Fprintf(w, "%-8s   reason: %s\n", "", c.Reason)
		}
		if len(c.DryRun) > 0 {
			fmt.Fprintf(w, "%-8s   %s dry run (not approved): %s\n", "", display.DryMark, strings.Join(c.DryRun, ", "))
		}
	}
	if len(r.unchanged) > 0 {
		fmt.Fprintf(w, "%-8s unchanged: %s\n", label, strings.Join(r.unchanged, ", "))
		label = ""
	}
	for _, l := range r.locks {
		fmt.Fprintf(w, "%-8s lock %s\n", "locks", l)
	}
	if r.queue != "" {
		fmt.Fprintf(w, "%-8s %s\n", "queue", r.queue)
	}
	if r.historyWarn != "" {
		fmt.Fprintln(w, "\nWARN  "+r.historyWarn)
	}
	if len(r.history) > 0 {
		fmt.Fprintf(w, "\n.swim.log  %d new lines\n", len(r.history))
		for _, l := range r.history {
			fmt.Fprintln(w, l)
		}
	}
	if !r.opts.noLogs {
		for _, l := range r.lanes {
			if l.warn != "" {
				fmt.Fprintln(w, "\nWARN  "+l.warn)
			}
			for _, sl := range l.slices {
				head := "==> swim " + l.key
				if m := roundJobRE.FindStringSubmatch(sl.round.lines[0]); m != nil {
					head += " · job=" + lane.ShortJob(m[1])
				}
				lines := sl.round.lines
				if sl.from > 0 {
					head += fmt.Sprintf(" · continues from line %d of the round", sl.from+1)
					lines = lines[sl.from:]
				} else {
					head += " · round " + sl.round.start
				}
				if r.opts.results {
					lines = resultsOnly(lines, l.key)
				}
				fmt.Fprintln(w, "\n"+head)
				for _, line := range lines {
					fmt.Fprintln(w, line)
				}
			}
		}
		for _, b := range r.inFlight {
			fmt.Fprintln(w)
			for _, line := range b {
				fmt.Fprintln(w, line)
			}
		}
	}
	fmt.Fprintln(w)
	if r.opts.move {
		fmt.Fprintf(w, "earmark %s moved to %s (swim report --earmark)\n", r.name, r.next.At)
	} else {
		fmt.Fprintf(w, "earmark %s not moved (peek; swim report --earmark moves it)\n", r.name)
	}
}

// reportDoc is swim report --yaml: swim.report/v1.
type reportDoc struct {
	Schema  string `yaml:"schema"`
	Earmark struct {
		Name    string `yaml:"name"`
		Since   string `yaml:"since,omitempty"`
		MovedTo string `yaml:"moved_to,omitempty"`
	} `yaml:"earmark"`
	Status struct {
		Changed   []statusChange `yaml:"changed"`
		Unchanged []int          `yaml:"unchanged"`
		Queue     string         `yaml:"queue,omitempty"`
		Locks     []string       `yaml:"locks,omitempty"`
	} `yaml:"status"`
	History  []string        `yaml:"history"`
	Warnings []string        `yaml:"warnings,omitempty"`
	Lanes    []laneReportDoc `yaml:"lanes"`
	InFlight [][]string      `yaml:"in_flight,omitempty"`
}

type laneReportDoc struct {
	Lane   int              `yaml:"lane"`
	Cell   string           `yaml:"cell,omitempty"`
	Rounds []roundReportDoc `yaml:"rounds"`
}

type roundReportDoc struct {
	ContinuesFrom int          `yaml:"continues_from"` // 0: a round started since the earmark
	Round         logparse.Doc `yaml:"round"`
}

func (r *report) writeYAML(w io.Writer) error {
	var d reportDoc
	d.Schema = "swim.report/v1"
	d.Earmark.Name, d.Earmark.Since = r.name, r.since
	if r.opts.move {
		d.Earmark.MovedTo = r.next.At
	}
	d.Status.Changed = r.changed
	if d.Status.Changed == nil {
		d.Status.Changed = []statusChange{}
	}
	for _, u := range r.unchanged {
		n, _ := strconv.Atoi(strings.Fields(u)[1])
		d.Status.Unchanged = append(d.Status.Unchanged, n)
	}
	d.Status.Queue, d.Status.Locks = r.queue, r.locks
	d.History = r.history
	if d.History == nil {
		d.History = []string{}
	}
	if r.historyWarn != "" {
		d.Warnings = append(d.Warnings, r.historyWarn)
	}
	d.Lanes = []laneReportDoc{}
	if !r.opts.noLogs {
		for _, l := range r.lanes {
			if l.warn != "" {
				d.Warnings = append(d.Warnings, l.warn)
			}
			ld := laneReportDoc{Lane: l.lane, Cell: l.cell}
			for _, sl := range l.slices {
				// The round's header lines, then only what's new: its
				// results since the earmark.
				lines := sl.round.lines
				if sl.from > 0 {
					head := 1
					for head < len(lines) && strings.HasPrefix(lines[head], "   ") && !strings.HasPrefix(lines[head], logparse.Indent) {
						head++
					}
					lines = append(append([]string(nil), lines[:head]...), lines[max(sl.from, head):]...)
				}
				pr, err := logparse.ParseRound(lines)
				if err != nil {
					return err
				}
				ld.Rounds = append(ld.Rounds, roundReportDoc{ContinuesFrom: sl.from, Round: pr.Doc(l.lane)})
			}
			d.Lanes = append(d.Lanes, ld)
		}
		d.InFlight = r.inFlight
	}
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if err := enc.Encode(d); err != nil {
		return err
	}
	return enc.Close()
}
