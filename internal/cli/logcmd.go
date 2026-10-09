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

	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// swim log [N|JOB] [--all]
func cmdLog(args []string) error {
	var all bool
	rest, err := flags{bools: map[string]*bool{"all": &all}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return usagef("usage: swim log [N|JOB] [--all]")
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	out := logWriter{w: os.Stdout, p: ui.Painter{On: ui.ColorEnabled(os.Stdout)}}

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

	// A job id: print that job's rounds from any lane's current or archived log.
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
		return fmt.Errorf("no log holds a round of job %s", ref)
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
		if line := sc.Text(); strings.HasPrefix(line, logparse.RoundStart) {
			if fields := strings.Fields(line); len(fields) >= 4 {
				return fields[3]
			}
		}
	}
	return ""
}

var roundJobRE = regexp.MustCompile(` job=(\S+)`)

// roundsOf returns the rounds in path (each from its ROUND START line to the
// next) whose job matches ref.
func roundsOf(path, ref string) ([][]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var rounds [][]string
	var cur []string
	keep := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, logparse.RoundStart) {
			if keep {
				rounds = append(rounds, cur)
			}
			cur, keep = nil, false
			if m := roundJobRE.FindStringSubmatch(line); m != nil {
				keep = lane.MatchJob(m[1], ref)
			}
		}
		if keep {
			cur = append(cur, line)
		}
	}
	if keep {
		rounds = append(rounds, cur)
	}
	return rounds, sc.Err()
}

// logWriter prints logs, colouring markers and result lines on a terminal.
type logWriter struct {
	w io.Writer
	p ui.Painter
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

var resultKindRE = regexp.MustCompile(`^(PASS|FAIL|SKIP|DRIFT|APPROVED|STOP)  `)

// lines prints log lines; outside command output, markers are bold and
// result lines take their state colour.
func (lw logWriter) lines(lines []string) {
	inOutput := false
	for _, l := range lines {
		color := ""
		switch {
		case l == logparse.OutputMark:
			inOutput = true
			color = ui.Dim
		case strings.HasPrefix(l, logparse.ExitMark), strings.HasPrefix(l, logparse.IntrMark):
			inOutput = false
			color = ui.Dim
		case strings.HasPrefix(l, "=== "):
			inOutput = false
			color = ui.Bold
		case !inOutput:
			if m := resultKindRE.FindStringSubmatch(l); m != nil {
				color = ui.StateColor(m[1])
			}
		}
		fmt.Fprintln(lw.w, lw.p.Paint(color, l))
	}
}
