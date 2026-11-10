package cli

import (
	"bufio"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/step"
	"github.com/gates-brightly/swimlane/internal/syntax"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// swim log [N|ROUND-ID|RUN] [--all] [--full] [--raw] [--yaml] [--follow] [--failed]
// swim log --since <1h|TIME|RUN|ROUND-ID>
func cmdLog(args []string) error {
	var all, full, raw, asYAML, follow, failed bool
	var since string
	rest, err := flags{bools: map[string]*bool{"all": &all, "full": &full, "raw": &raw, "yaml": &asYAML,
		"follow": &follow, "failed": &failed}, strs: map[string]*string{"since": &since}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return usagef("usage: swim log [N|ROUND-ID|RUN] [--all] [--full] [--raw] [--yaml] [--follow] [--failed]\n       swim log --since <1h|TIME|RUN|ROUND-ID>")
	}
	if failed && (len(rest) == 0 || follow) {
		return usagef("--failed takes a lane or round id, and no --follow: swim log N --failed")
	}
	if since != "" && len(rest) > 0 {
		return usagef("--since reads the project log: swim log --since %s (swim log %s is all of it)", since, rest[0])
	}
	if asYAML && len(rest) == 0 {
		return usagef("--yaml needs a lane or round id: swim log N --yaml")
	}
	if follow && (len(rest) == 0 || asYAML || all) {
		return usagef("--follow takes one lane, and no --yaml or --all: swim log N --follow")
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	// On a terminal the log is rendered (colour, folded output, relative
	// times). Piped, it's the file's lines, with repeated output folded
	// (logparse.FoldRepeats); --raw is the file exactly, as cat shows it,
	// and --full keeps every output line.
	render := ui.ColorEnabled(os.Stdout) && !raw
	// --failed shows a failed step's output in full: that's what a fix needs.
	full = full || failed
	out := logWriter{w: os.Stdout, p: ui.Painter{On: render}, full: full, fold: !raw && !full && !follow, ref: strings.Join(rest, "")}

	if len(rest) == 0 {
		if all {
			return usagef("--all needs a lane: swim log N --all")
		}
		if since != "" {
			return logSince(root, since, out)
		}
		return out.file(history.Path(root), false)
	}

	ref, err := taskRef(root, cfg, rest[0])
	if err != nil {
		return err
	}
	if _, err := strconv.Atoi(ref); err == nil || follow {
		n, err := laneRef(root, cfg, ref, true)
		if err != nil {
			return err
		}
		if follow {
			return followLog(root, n, out)
		}
		files := []string{}
		if all {
			files = append(files, archives(root, n)...)
		}
		if _, err := os.Stat(lane.Log(root, n)); err == nil {
			files = append(files, lane.Log(root, n))
		}
		if len(files) == 0 {
			if all {
				return fmt.Errorf("lane %d has no logs yet", n)
			}
			msg := fmt.Sprintf("lane %d has no current log (.swim/logs/lane%d.log)", n, n)
			if len(archives(root, n)) > 0 {
				msg += "; its archived logs: swim log " + ref + " --all"
			}
			return errors.New(msg)
		}
		if asYAML || failed {
			// The latest round, or with --all every round, as data.
			var rounds [][]string
			for _, f := range files {
				rs, err := splitRounds(f)
				if err != nil {
					return err
				}
				rounds = append(rounds, rs...)
			}
			if !all && len(rounds) > 1 {
				rounds = rounds[len(rounds)-1:]
			}
			sp, why := inFlight(root, n)
			if !asYAML {
				return out.failed(n, rounds)
			}
			return writeDocs(os.Stdout, n, rounds, sp, why, failed)
		}
		for _, f := range files {
			if err := out.file(f, len(files) > 1); err != nil {
				return err
			}
		}
		// The step running now isn't in the log file until it ends: show it
		// from its spool (not with --raw, which is the file exactly).
		if sp, why := inFlight(root, n); sp != nil && !raw {
			out.lines(sp.Block(why))
		}
		return nil
	}

	// A job or run id: print its rounds from every lane's current or archived
	// logs, in lane order.
	if len(ref) < 8 && !lane.ValidJobID(ref) {
		return usagef("%q is neither a lane number nor a round id (round ids need 8+ characters)", ref)
	}
	found := 0
	for n := 1; n <= cfg.Lanes; n++ {
		files := append(archives(root, n), lane.Log(root, n))
		for _, f := range files {
			rounds, err := roundsOf(f, ref)
			if err != nil {
				return err
			}
			if len(rounds) == 0 {
				continue
			}
			found += len(rounds)
			// Is the last of them the round running now (the log's last)?
			var sp *step.Spool
			why := ""
			if f == lane.Log(root, n) {
				if all, _ := splitRounds(f); len(all) > 0 && all[len(all)-1][0] == rounds[len(rounds)-1][0] {
					sp, why = inFlight(root, n)
				}
			}
			if asYAML {
				if err := writeDocs(os.Stdout, n, rounds, sp, why, failed); err != nil {
					return err
				}
				continue
			}
			out.header(f)
			if failed {
				if err := out.failed(n, rounds); err != nil {
					return err
				}
				continue
			}
			for _, r := range rounds {
				out.lines(r)
			}
			if sp != nil && !raw {
				out.lines(sp.Block(why))
			}
		}
	}
	if found == 0 {
		return fmt.Errorf("no log holds round or run %s", ref)
	}
	return nil
}

// logSince prints the project log's lines from ref on: a time, a duration
// back from now, or a run's or job's first line. What happened since then;
// each round's detail is swim log ROUND-ID.
func logSince(root, ref string, out logWriter) error {
	data, err := os.ReadFile(history.Path(root))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s does not exist yet", history.FileName)
	}
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	from, at, err := sinceLine(ref, lines)
	if err != nil {
		return err
	}
	var show []string
	for _, l := range lines[at:] {
		if f := strings.Fields(l); len(f) > 0 && !strings.HasPrefix(l, "#") && f[0] >= from {
			show = append(show, l)
		}
	}
	if len(show) == 0 {
		fmt.Fprintf(out.w, "nothing in %s since %s\n", history.FileName, ref)
		return nil
	}
	out.lines(show)
	return nil
}

// sinceLine resolves --since: an RFC 3339 time or a duration back from now
// (from: the UTC time lines must be stamped at or after), or a run id or
// job (at: the index of its first line in the project log).
func sinceLine(ref string, lines []string) (from string, at int, err error) {
	if t, err := time.Parse(time.RFC3339, ref); err == nil {
		return t.UTC().Format(time.RFC3339), 0, nil
	}
	if d, ok := sinceAgo(ref); ok {
		return time.Now().Add(-d).UTC().Format(time.RFC3339), 0, nil
	}
	for i, l := range lines {
		if run := historyField(l, "run"); run != "" && run == ref || lane.MatchJob(or(historyField(l, "round-id"), historyField(l, "job")), ref) {
			return "", i, nil
		}
	}
	return "", 0, usagef("--since %q: not a UTC time (2026-10-15T14:02:11Z), a duration back from now (1h, 2d), or a run id or round id in %s", ref, history.FileName)
}

// sinceAgo reads a duration back from now: Go's (90m, 1h30m) or whole
// days (2d).
func sinceAgo(s string) (time.Duration, bool) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		if n, err := strconv.Atoi(days); err == nil && n > 0 {
			return time.Duration(n) * 24 * time.Hour, true
		}
		return 0, false
	}
	d, err := time.ParseDuration(s)
	return d, err == nil && d > 0
}

// historyField is the value of key=VALUE in a .swim.log line ("" if none).
func historyField(line, key string) string {
	i := strings.Index(line, "  "+key+"=")
	if i < 0 {
		return ""
	}
	v := line[i+len(key)+3:]
	if j := strings.IndexAny(v, " \t"); j >= 0 {
		v = v[:j]
	}
	return v
}

// archives lists lane n's archived logs, oldest round first.
func archives(root string, n int) []string {
	files, _ := filepath.Glob(filepath.Join(lane.LogDir(root), fmt.Sprintf("lane%d.prev-*.log", n)))
	type dated struct {
		path, key string
	}
	var ds []dated
	for _, f := range files {
		key := firstRoundTime(f)
		if key == "" {
			if st, err := os.Stat(f); err == nil {
				key = st.ModTime().UTC().Format("2006-01-02T15:04:05Z")
			}
		}
		ds = append(ds, dated{f, key})
	}
	sort.SliceStable(ds, func(a, b int) bool { return ds[a].key < ds[b].key })
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.path
	}
	return out
}

func firstRoundTime(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, logparse.RoundMark) {
			if fields := strings.Fields(line); len(fields) >= 3 {
				return fields[2]
			}
		}
		if strings.HasPrefix(line, "=== ROUND START") { // syntax 1
			if fields := strings.Fields(line); len(fields) >= 4 {
				return fields[3]
			}
		}
	}
	return ""
}

// roundsOf returns the rounds in path (each from its round start to the
// next) that belong to job ref or, given a run id, to that swim run.
func roundsOf(path, ref string) ([][]string, error) {
	all, err := splitRounds(path)
	var rounds [][]string
	for _, r := range all {
		if roundMatches(r, ref) {
			rounds = append(rounds, r)
		}
	}
	return rounds, err
}

// splitRounds returns every round in path, each from its round start to
// the next.
func splitRounds(path string) ([][]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var all [][]string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, logparse.RoundMark) || strings.HasPrefix(line, "=== ROUND START") {
			all = append(all, nil)
		}
		if len(all) > 0 {
			all[len(all)-1] = append(all[len(all)-1], line)
		}
	}
	return all, sc.Err()
}

// writeDocs prints rounds as YAML documents (swim log --yaml).
// The last round gets sp, the step running now, as a RUNNING result. With
// failed (--failed), each keeps only its failed results.
func writeDocs(w io.Writer, n int, rounds [][]string, sp *step.Spool, why string, failed bool) error {
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	for i, lines := range rounds {
		r, err := logparse.ParseRound(lines)
		if err != nil {
			return err
		}
		d := r.Doc(n)
		if sp != nil && i == len(rounds)-1 && !r.Finished {
			dur := sp.Elapsed().Seconds()
			output := append([]string(nil), sp.Output...)
			if sp.Partial != "" {
				output = append(output, sp.Partial)
			}
			d.Results = append(d.Results, logparse.ResultDoc{Result: step.Running, Label: sp.Label, Detail: why,
				Stage: r.Stage, Step: true, Duration: &dur, Clock: sp.Clock, Command: sp.Cmd, Output: output})
		}
		if failed {
			d = failedOnly(d)
		}
		if err := enc.Encode(d); err != nil {
			return err
		}
	}
	return enc.Close()
}

// inFlight is lane n's running step, from its spool, and why its block
// isn't in the log yet.
func inFlight(root string, n int) (*step.Spool, string) {
	sp, ok := step.ReadSpool(lane.Log(root, n))
	if !ok {
		return nil, ""
	}
	if _, running := lane.Running(root, n); running {
		return sp, "in progress"
	}
	return sp, "cut off: swim stopped mid-step; recorded as FAIL when the lane next runs"
}

// roundMatches reports whether a round's lines belong to job or run ref.
func roundMatches(r []string, ref string) bool {
	if job, ok := logparse.RoundJob(r[0]); ok && lane.MatchJob(job, ref) {
		return true
	}
	if len(r) > 1 && strings.HasPrefix(r[1], "   ") && !strings.HasPrefix(r[1], logparse.Indent) {
		if run := logparse.ParseContext(r[1])["run"]; run != "" && run == ref {
			return true
		}
	}
	return false
}

// logWriter prints logs. With colour on (a terminal) it renders them: the
// round banner and stage headers stand out, results are coloured, times show
// how far into the round they were, and long step output is folded unless
// full is set. Without colour it writes the file's lines unchanged.
type logWriter struct {
	w    io.Writer
	p    ui.Painter
	full bool
	fold bool   // fold repeated output (logparse.FoldRepeats)
	ref  string // lane or job as typed, for the "--full" hint
}

func (lw logWriter) header(path string) {
	fmt.Fprintln(lw.w, lw.p.Paint(ui.Bold, "==> "+filepath.Base(path)+" <=="))
}

func (lw logWriter) file(path string, withHeader bool) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s does not exist yet", filepath.Base(path))
	}
	if err != nil {
		return err
	}
	defer f.Close()
	if withHeader {
		lw.header(path)
	}
	if !lw.p.On && !lw.fold {
		_, err = io.Copy(lw.w, f)
		return err
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	lw.lines(lines)
	return sc.Err()
}

// Output folding: a step with more than foldOver lines of output shows its
// first and last foldKeep.
const (
	foldOver = 40
	foldKeep = 15
)

// lines writes log lines, rendered if colour is on.
func (lw logWriter) lines(lines []string) {
	if lw.fold {
		lines = logparse.FoldRepeats(lines)
	}
	if !lw.p.On {
		for _, l := range lines {
			fmt.Fprintln(lw.w, l)
		}
		return
	}
	p := lw.p
	var start time.Time // current round's start, for relative times
	rel := func(clock string) string {
		if start.IsZero() {
			return ""
		}
		t, err := time.Parse("15:04:05", clock)
		if err != nil {
			return ""
		}
		at := time.Date(start.Year(), start.Month(), start.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
		d := at.Sub(start)
		if d < 0 {
			d += 24 * time.Hour
		}
		return "  +" + display.Duration(d)
	}
	var out []string // the current step's output lines, for folding
	flush := func() {
		if len(out) > foldOver && !lw.full {
			for _, o := range out[:foldKeep] {
				fmt.Fprintln(lw.w, o)
			}
			hint := "swim log --full"
			if lw.ref != "" {
				hint = "swim log " + lw.ref + " --full"
			}
			fmt.Fprintln(lw.w, p.Paint(ui.Dim, fmt.Sprintf("%s… %d more lines (%s shows them)", logparse.OutPrefix, len(out)-2*foldKeep, hint)))
			for _, o := range out[len(out)-foldKeep:] {
				fmt.Fprintln(lw.w, o)
			}
		} else {
			for _, o := range out {
				fmt.Fprintln(lw.w, o)
			}
		}
		out = out[:0]
	}
	for _, l := range lines {
		if strings.HasPrefix(l, logparse.OutPrefix) {
			out = append(out, l)
			continue
		}
		flush()
		switch {
		case syntax.IsLogHeader(l):
			fmt.Fprintln(lw.w, p.Paint(ui.Dim, l))
		case strings.HasPrefix(l, logparse.RoundMark):
			if f := strings.Fields(l); len(f) >= 3 {
				start, _ = time.Parse(time.RFC3339, f[2])
			}
			fmt.Fprintln(lw.w, p.Paint(ui.Bold+ui.Cyan, l))
		case strings.HasPrefix(l, logparse.EndMark):
			state := ""
			if f := strings.Fields(l); len(f) >= 3 {
				state = f[2]
			}
			fmt.Fprintln(lw.w, p.Paint(ui.Bold+ui.StateColor(state), l))
		case strings.HasPrefix(l, "   stages: "):
			parts := strings.Split(strings.TrimPrefix(l, "   stages: "), " | ")
			for i, part := range parts {
				if name, st, ok := strings.Cut(part, " "); ok {
					parts[i] = name + " " + p.Paint(ui.StateColor(st), st)
				}
			}
			fmt.Fprintln(lw.w, "   stages: "+strings.Join(parts, " | "))
		case strings.HasPrefix(l, "   failed: "):
			fmt.Fprintln(lw.w, p.Paint(ui.Red, l))
		case strings.HasPrefix(l, logparse.StageMark):
			text := p.Paint(ui.Bold+ui.Blue, l)
			if f := strings.Fields(strings.TrimPrefix(l, logparse.StageMark)); len(f) > 1 {
				text += p.Paint(ui.Dim, rel(f[1]))
			}
			fmt.Fprintln(lw.w, text)
		case strings.HasPrefix(l, logparse.Indent):
			fmt.Fprintln(lw.w, p.Paint(ui.Dim, l))
		case strings.HasPrefix(l, "   "):
			fmt.Fprintln(lw.w, p.Paint(ui.Dim, l))
		default:
			kind, rest, ok := logparse.SplitResult(l, "RUNNING")
			if !ok {
				fmt.Fprintln(lw.w, l)
				continue
			}
			tail := ""
			if head, _, clock, ok := logparse.SplitTimed(rest); ok {
				rest, tail = head, p.Paint(ui.Dim, strings.TrimRight(rest, " ")[len(head):]+rel(clock))
			}
			fmt.Fprintln(lw.w, "  "+p.Paint(ui.StateColor(kind)+ui.Bold, kind)+"  "+p.Paint(ui.StateColor(kind), rest)+tail)
		}
	}
	flush()
}

// followLog prints lane n's current round and then follows it, like
// tail -f: lines as the log gains them, and the running step's output as
// its spool gains it (so it streams while the step runs, not only when it
// ends). It returns once the round has ended and the lane has stopped
// (whatever the round's result: swim wait N exits with that), or fails
// when the lane stopped without ending its round. With no round yet, it
// waits for one.
func followLog(root string, n int, out logWriter) error {
	logPath := lane.Log(root, n)
	var off int64
	var pending string // an incomplete last line of the log
	ended := false     // the last round has its END
	streamKey := ""    // the spooled step being streamed (label + clock)
	streamed := 0      // its output lines printed so far
	skipBlock := false // printing a streamed step's block: skip what streamed
	skipOut := 0       // its output lines that streamed (the rest didn't)

	emit := func(lines []string) {
		if len(lines) > 0 {
			out.lines(lines)
		}
	}
	// Start with the current (last) round so far.
	if rounds, _ := splitRounds(logPath); len(rounds) > 0 {
		last := rounds[len(rounds)-1]
		emit(last)
		for _, l := range last {
			if _, ok := logparse.EndExit(l); ok {
				ended = true
			}
		}
	} else {
		fmt.Fprintf(out.w, "(lane %d has no round yet; waiting)\n", n)
	}
	if st, err := os.Stat(logPath); err == nil {
		off = st.Size()
	}

	for {
		// New log lines.
		if f, err := os.Open(logPath); err == nil {
			if st, err := f.Stat(); err == nil && st.Size() < off {
				off, pending = 0, "" // archived or replaced: start over
			}
			f.Seek(off, io.SeekStart)
			data, _ := io.ReadAll(f)
			f.Close()
			off += int64(len(data))
			text := pending + string(data)
			lines := strings.Split(text, "\n")
			pending = lines[len(lines)-1]
			var show []string
			for _, l := range lines[:len(lines)-1] {
				if skipBlock {
					if strings.HasPrefix(l, logparse.Indent) {
						// The step's command and the output that streamed; output
						// written after the last look at the spool still shows.
						if strings.HasPrefix(l, logparse.Indent+"$ ") {
							continue
						}
						if skipOut > 0 {
							skipOut--
							continue
						}
						show = append(show, l)
						continue
					}
					skipBlock = false
				}
				switch {
				case streamKey != "" && strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, logparse.Indent) &&
					strings.Contains(l, strings.SplitN(streamKey, "\t", 2)[1]):
					// The streamed step's result line: show it, skip its block.
					show = append(show, l)
					streamKey, skipOut, streamed, skipBlock = "", streamed, 0, true
					continue
				case strings.HasPrefix(l, logparse.RoundMark):
					ended = false
				}
				if _, ok := logparse.EndExit(l); ok {
					ended = true
				}
				show = append(show, l)
			}
			emit(show)
		}
		// The running step's output so far.
		if sp, ok := step.ReadSpool(logPath); ok {
			key := sp.Label + "\t" + sp.Clock
			if key != streamKey {
				streamKey, streamed = key, 0
				emit(sp.Block("in progress")[:2]) // RUNNING line and its command
			}
			var show []string
			for _, l := range sp.Output[min(streamed, len(sp.Output)):] {
				if strings.HasPrefix(l, "-- ") {
					show = append(show, logparse.Indent+l)
				} else {
					show = append(show, logparse.OutPrefix+l)
				}
			}
			streamed = len(sp.Output)
			emit(show)
		}
		switch _, running := lane.Running(root, n); {
		case running, laneWaiting(root, n):
			// Running, or a swim run holds it waiting to start: its next
			// round is coming.
		case ended:
			return nil
		case off > 0:
			// A round with no END, and nothing running it: the lane
			// died (killed, machine went down).
			return fmt.Errorf("lane %d stopped without finishing its round (no END in its log)", n)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// laneWaiting reports whether a swim run holds lane n waiting to start
// (dependencies, a slot or a lock), so following it should keep waiting.
func laneWaiting(root string, n int) bool {
	st, _ := status.Load(root)
	if st == nil {
		return false
	}
	l := st.Get(n)
	if l == nil {
		return false
	}
	switch l.State {
	case status.Waiting, status.Queued, status.Running:
		return l.Alive()
	}
	return false
}
