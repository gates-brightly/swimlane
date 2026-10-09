package ci

import (
	"bufio"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/status"
)

// LaneReport is one selected lane after the run: its status entry and, if
// it ran in this run, its round from the log.
type LaneReport struct {
	N        int
	Job      string
	Title    string
	State    string // passed | failed | skipped | interrupted
	Reason   string
	Exit     *int
	Duration float64
	Round    *logparse.Round // nil if the lane didn't run in this run
	Lines    []string        // that round's log lines
	Log      string          // the log's path, relative to the repo
}

// Collect builds the reports for lanes after run, from status.yml and the logs.
func Collect(root string, lanes []int, run string) []LaneReport {
	st, _ := status.Load(root)
	var out []LaneReport
	for _, n := range lanes {
		r := LaneReport{N: n, State: status.Skipped, Log: fmt.Sprintf(".swim/logs/agent%d.log", n)}
		if st != nil {
			if l := st.Get(n); l != nil {
				r.Job, r.Title, r.State, r.Reason, r.Exit = l.Job, l.Round, l.State, l.Reason, l.ExitCode
				if l.DurationS != nil {
					r.Duration = *l.DurationS
				}
			}
		}
		if lines := lastRound(lane.Log(root, n)); len(lines) > 0 {
			if round, err := logparse.ParseRound(lines); err == nil && round.Run == run {
				r.Round, r.Lines = round, lines
				if r.Title == "" {
					r.Title = round.Title
				}
			}
		}
		out = append(out, r)
	}
	return out
}

// lastRound returns the lines of the last round in a log.
func lastRound(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var cur []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, logparse.RoundMark) {
			cur = nil
		}
		if cur != nil || strings.HasPrefix(line, logparse.RoundMark) {
			cur = append(cur, line)
		}
	}
	return cur
}

func word(state string) string {
	switch state {
	case status.Passed:
		return "PASS"
	case status.Failed:
		return "FAIL"
	case status.Interrupted:
		return "INTERRUPTED"
	case status.Skipped:
		return "SKIP"
	}
	return strings.ToUpper(state)
}

func dur(s float64) string {
	d := int(s + 0.5)
	return fmt.Sprintf("%d:%02d", d/60, d%60)
}

// Title is a lane's section title, e.g. "swim 3 PASS - Cut over (job 3f2a9c1e, 0:42)".
func (r LaneReport) SectionTitle() string {
	t := fmt.Sprintf("swim %d %s - %s", r.N, word(r.State), r.Title)
	var extra []string
	if r.Job != "" {
		extra = append(extra, "job "+lane.ShortJob(r.Job))
	}
	if r.Round != nil {
		extra = append(extra, dur(r.Duration))
	}
	if r.State == status.Skipped && r.Reason != "" {
		extra = append(extra, r.Reason)
	}
	if len(extra) > 0 {
		t += " (" + strings.Join(extra, ", ") + ")"
	}
	return t
}

// Sections prints each lane's round log in its own section, in lane order.
// Failed lanes stay open: ungrouped where the provider folds every group.
func Sections(w io.Writer, p Provider, reps []LaneReport) {
	for _, r := range reps {
		failed := r.State == status.Failed || r.State == status.Interrupted
		if r.Round == nil {
			if r.State == status.Skipped {
				continue // nothing ran; the summary and a notice say why
			}
		}
		if failed && p.Folds() {
			fmt.Fprintf(w, "==== %s ====\n", r.SectionTitle())
			writeLines(w, r)
			fmt.Fprintln(w)
			continue
		}
		p.Group(w, r.SectionTitle(), !failed)
		writeLines(w, r)
		p.EndGroup(w)
	}
}

func writeLines(w io.Writer, r LaneReport) {
	if len(r.Lines) == 0 {
		fmt.Fprintf(w, "(no round in %s for this run)\n", r.Log)
		return
	}
	for _, l := range r.Lines {
		fmt.Fprintln(w, l)
	}
}

// Annotate surfaces failures (error), drift (warning), dry-run skips
// (notice) and, as one notice, the lanes skipped because of others.
func Annotate(w io.Writer, p Provider, reps []LaneReport) {
	skipped := map[string][]int{}
	var reasons []string
	for _, r := range reps {
		title := fmt.Sprintf("swim %d: %s", r.N, r.Title)
		if r.Round == nil {
			if r.State == status.Skipped {
				if _, ok := skipped[r.Reason]; !ok {
					reasons = append(reasons, r.Reason)
				}
				skipped[r.Reason] = append(skipped[r.Reason], r.N)
			} else if r.State == status.Failed {
				p.Annotate(w, Error, title, or(r.Reason, "lane failed before its round started"))
			}
			continue
		}
		for _, res := range r.Round.Results {
			switch res.Kind {
			case logparse.Fail, logparse.Blocked, logparse.Stop:
				p.Annotate(w, Error, title, res.Text())
			case logparse.Drift:
				p.Annotate(w, Warning, title, res.Text())
			case logparse.Skip:
				if strings.HasPrefix(res.Detail, "dry run:") {
					p.Annotate(w, Notice, title, res.Label+": "+res.Detail)
				}
			}
		}
	}
	if len(reasons) > 0 {
		var parts []string
		total := 0
		for _, why := range reasons {
			ns := skipped[why]
			total += len(ns)
			parts = append(parts, fmt.Sprintf("swim %s (%s)", joinInts(ns), or(why, "not run")))
		}
		noun := "lanes"
		if total == 1 {
			noun = "lane"
		}
		p.Annotate(w, Notice, fmt.Sprintf("swim: %d %s skipped", total, noun), strings.Join(parts, "; "))
	}
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func joinInts(ns []int) string {
	s := make([]string, len(ns))
	for i, n := range ns {
		s[i] = fmt.Sprint(n)
	}
	return strings.Join(s, ", ")
}

// Markdown writes the job summary (GitHub's $GITHUB_STEP_SUMMARY).
func Markdown(w io.Writer, reps []LaneReport, run, commit string, code int) {
	result := "passed"
	if code != 0 {
		result = "failed"
	}
	fmt.Fprintf(w, "## swim ci: %s\n\n", result)
	fmt.Fprintf(w, "run `%s`", run)
	if commit != "" {
		fmt.Fprintf(w, " · commit `%s`", short(commit))
	}
	fmt.Fprintf(w, " · %d lanes\n\n", len(reps))
	fmt.Fprintln(w, "| lane | job | result | pass | fail | skip | drift | time | failed steps |")
	fmt.Fprintln(w, "|---|---|---|---:|---:|---:|---:|---:|---|")
	for _, r := range reps {
		var pass, fail, skip, drift int
		var failed []string
		if r.Round != nil {
			pass, fail, skip, drift = r.Round.Pass, r.Round.Fail, r.Round.Skip, r.Round.Drift
			for _, f := range r.Round.Failed() {
				failed = append(failed, mdCell(f.Text()))
			}
		} else if r.Reason != "" {
			failed = append(failed, mdCell(r.Reason))
		}
		t := ""
		if r.Round != nil {
			t = dur(r.Duration)
		}
		fmt.Fprintf(w, "| swim %d | `%s` | %s | %d | %d | %d | %d | %s | %s |\n", r.N, lane.ShortJob(r.Job), word(r.State),
			pass, fail, skip, drift, t, strings.Join(failed, "<br>"))
	}
	fmt.Fprintln(w, "\nLogs: `.swim/logs/` (upload them as an artifact to keep them).")
}

func mdCell(s string) string { return strings.NewReplacer("|", "\\|", "\n", " ").Replace(s) }

// JUnit XML: one testsuite per lane, one testcase per step.
type junitSuites struct {
	XMLName  xml.Name     `xml:"testsuites"`
	Name     string       `xml:"name,attr"`
	Tests    int          `xml:"tests,attr"`
	Failures int          `xml:"failures,attr"`
	Skipped  int          `xml:"skipped,attr"`
	Suites   []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	Name     string      `xml:"name,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Skipped  int         `xml:"skipped,attr"`
	Time     string      `xml:"time,attr"`
	Cases    []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name      string        `xml:"name,attr"`
	ClassName string        `xml:"classname,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *junitMessage `xml:"failure,omitempty"`
	Skipped   *junitMessage `xml:"skipped,omitempty"`
}

type junitMessage struct {
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

// JUnit writes the report: a FAIL, BLOCKED or STOP is a <failure> (with the
// step's output tail), a SKIP is <skipped>, and a lane that never ran is a
// suite with one skipped case saying why.
func JUnit(w io.Writer, reps []LaneReport) error {
	all := junitSuites{Name: "swim"}
	for _, r := range reps {
		s := junitSuite{Name: fmt.Sprintf("swim %d: %s", r.N, r.Title), Time: fmt.Sprintf("%.1f", r.Duration)}
		class := fmt.Sprintf("swim.lane%d", r.N)
		if r.Round == nil {
			msg := or(r.Reason, "did not run")
			c := junitCase{Name: "round", ClassName: class, Time: "0"}
			if r.State == status.Failed || r.State == status.Interrupted {
				c.Failure = &junitMessage{Message: msg}
				s.Failures++
			} else {
				c.Skipped = &junitMessage{Message: msg}
				s.Skipped++
			}
			s.Cases, s.Tests = append(s.Cases, c), 1
		} else {
			for _, res := range r.Round.Results {
				c := junitCase{Name: res.Label, ClassName: class + "." + res.Stage, Time: fmt.Sprintf("%.1f", res.Dur)}
				switch res.Kind {
				case logparse.Fail, logparse.Blocked, logparse.Stop:
					c.Failure = &junitMessage{Message: res.Text(), Body: tail(res.Output, 30)}
					if res.Kind == logparse.Stop {
						c.Name = "STOP: " + res.Label
					}
					s.Failures++
				case logparse.Skip:
					c.Skipped = &junitMessage{Message: res.Detail}
					s.Skipped++
				case logparse.Pass, logparse.Approved:
				default:
					continue // DRIFT and WARN are annotations, not tests
				}
				s.Cases = append(s.Cases, c)
			}
			s.Tests = len(s.Cases)
		}
		all.Tests += s.Tests
		all.Failures += s.Failures
		all.Skipped += s.Skipped
		all.Suites = append(all.Suites, s)
	}
	io.WriteString(w, xml.Header)
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(all); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

func tail(lines []string, n int) string {
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
