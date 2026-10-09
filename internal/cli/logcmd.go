package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// swim log [N|JOB] [--all] [--full] [--raw]
func cmdLog(args []string) error {
	var all, full, raw bool
	rest, err := flags{bools: map[string]*bool{"all": &all, "full": &full, "raw": &raw}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return usagef("usage: swim log [N|JOB] [--all] [--full] [--raw]")
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	// On a terminal the log is rendered (colour, folded output, relative
	// times); piped or with --raw it is the file exactly, as cat shows it.
	render := ui.ColorEnabled(os.Stdout) && !raw
	out := logWriter{w: os.Stdout, p: ui.Painter{On: render}, full: full, ref: strings.Join(rest, "")}

	if len(rest) == 0 {
		if all {
			return usagef("--all needs a lane: swim log N --all")
		}
		return out.file(history.Path(root), false)
	}

	ref := rest[0]
	if _, err := strconv.Atoi(ref); err == nil {
		n, err := laneArg(cfg, ref)
		if err != nil {
			return err
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
				return fmt.Errorf("swim %d has no logs yet", n)
			}
			msg := fmt.Sprintf("swim %d has no current log (.swim/logs/agent%d.log)", n, n)
			if len(archives(root, n)) > 0 {
				msg += "; its archived logs: swim log " + ref + " --all"
			}
			return errors.New(msg)
		}
		for _, f := range files {
			if err := out.file(f, len(files) > 1); err != nil {
				return err
			}
		}
		return nil
	}

	// A job or run id: print its rounds from every lane's current or archived
	// logs, in lane order.
	if len(ref) < 8 && !lane.ValidJobID(ref) {
		return usagef("%q is neither a lane number nor a job id (job ids need 8+ characters)", ref)
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
			out.header(f)
			for _, r := range rounds {
				out.lines(r)
			}
			found += len(rounds)
		}
	}
	if found == 0 {
		return fmt.Errorf("no log holds a round of job or run %s", ref)
	}
	return nil
}

// archives lists lane n's archived logs, oldest round first.
func archives(root string, n int) []string {
	files, _ := filepath.Glob(filepath.Join(lane.LogDir(root), fmt.Sprintf("agent%d.prev-*.log", n)))
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

var roundJobRE = regexp.MustCompile(` job=(\S+)`)

// roundsOf returns the rounds in path (each from its round start to the
// next) that belong to job ref or, given a run id, to that swim run.
func roundsOf(path, ref string) ([][]string, error) {
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
	var rounds [][]string
	for _, r := range all {
		if roundMatches(r, ref) {
			rounds = append(rounds, r)
		}
	}
	return rounds, sc.Err()
}

// roundMatches reports whether a round's lines belong to job or run ref.
func roundMatches(r []string, ref string) bool {
	if m := roundJobRE.FindStringSubmatch(r[0]); m != nil && lane.MatchJob(m[1], ref) {
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
	if !lw.p.On {
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

var (
	resultKindRE = regexp.MustCompile(`^  (PASS|FAIL|SKIP|DRIFT|APPROVED|STOP|WARN)  (.*)$`)
	timedTailRE  = regexp.MustCompile(`^(.*?)(\s+\d+(?:\.\d+)?s\s+)(\d\d:\d\d:\d\d)$`)
	stageLineRE  = regexp.MustCompile(`^-- stage (\S+)(?:\s+(\d\d:\d\d:\d\d))?`)
	stageWordRE  = regexp.MustCompile(`(\w+) (PASS|FAIL|SKIP|none)`)
)

// lines writes log lines, rendered if colour is on.
func (lw logWriter) lines(lines []string) {
	if !lw.p.On {
		for _, l := range lines {
			fmt.Fprintln(lw.w, l)
		}
		return
	}
	if len(lines) > 0 && !strings.HasPrefix(lines[0], "# swim lane log") && syntaxOneLog(lines) {
		lw.linesV1(lines)
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
		return "  +" + display.Elapsed(d)
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
		case strings.HasPrefix(l, "# swim lane log"):
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
			fmt.Fprintln(lw.w, "   stages: "+stageWordRE.ReplaceAllStringFunc(strings.TrimPrefix(l, "   stages: "), func(m string) string {
				name, st, _ := strings.Cut(m, " ")
				return name + " " + p.Paint(ui.StateColor(st), st)
			}))
		case strings.HasPrefix(l, "   failed: "):
			fmt.Fprintln(lw.w, p.Paint(ui.Red, l))
		case strings.HasPrefix(l, logparse.StageMark):
			m := stageLineRE.FindStringSubmatch(l)
			text := p.Paint(ui.Bold+ui.Blue, l)
			if m != nil && m[2] != "" {
				text += p.Paint(ui.Dim, rel(m[2]))
			}
			fmt.Fprintln(lw.w, text)
		case strings.HasPrefix(l, logparse.Indent):
			fmt.Fprintln(lw.w, p.Paint(ui.Dim, l))
		case strings.HasPrefix(l, "   "):
			fmt.Fprintln(lw.w, p.Paint(ui.Dim, l))
		default:
			m := resultKindRE.FindStringSubmatch(l)
			if m == nil {
				fmt.Fprintln(lw.w, l)
				continue
			}
			kind, rest := m[1], m[2]
			tail := ""
			if t := timedTailRE.FindStringSubmatch(rest); t != nil {
				rest, tail = t[1], p.Paint(ui.Dim, t[2]+t[3]+rel(t[3]))
			}
			fmt.Fprintln(lw.w, "  "+p.Paint(ui.StateColor(kind)+ui.Bold, kind)+"  "+p.Paint(ui.StateColor(kind), rest)+tail)
		}
	}
	flush()
}

// syntaxOneLog reports whether lines use syntax 1 framing.
func syntaxOneLog(lines []string) bool {
	for _, l := range lines {
		if strings.HasPrefix(l, "=== ") {
			return true
		}
		if strings.HasPrefix(l, logparse.RoundMark) {
			return false
		}
	}
	return false
}

var resultKindV1RE = regexp.MustCompile(`^(PASS|FAIL|SKIP|DRIFT|APPROVED|STOP)  `)

// linesV1 renders a syntax 1 log (one that couldn't be migrated yet because
// its lane is running).
func (lw logWriter) linesV1(lines []string) {
	inOutput := false
	for _, l := range lines {
		color := ""
		switch {
		case l == "--- output":
			inOutput = true
			color = ui.Dim
		case strings.HasPrefix(l, "--- exit"), strings.HasPrefix(l, "--- interrupted"):
			inOutput = false
			color = ui.Dim
		case strings.HasPrefix(l, "=== "):
			inOutput = false
			color = ui.Bold
		case !inOutput:
			if m := resultKindV1RE.FindStringSubmatch(l); m != nil {
				color = ui.StateColor(m[1])
			}
		}
		fmt.Fprintln(lw.w, lw.p.Paint(color, l))
	}
}
