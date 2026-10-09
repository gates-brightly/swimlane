// Package logparse defines the lane log grammar and reads it back.
//
// A lane log is append-only. Each round starts with a ROUND START marker;
// steps are framed so command output can never be mistaken for a result
// line:
//
//	=== ROUND START 2026-10-09T12:00:00Z swim 1 job=3f2a9c1e-...
//	Round: Cut over service X
//
//	=== STEP 2026-10-09T12:00:01Z plan is a no-op
//	$ terraform plan -detailed-exitcode
//	cwd: /repo   git: main@abc1234   runtime: v20.11.0
//	env: AWS_PROFILE=dev STAGE=dev
//	--- output
//	...command output, ANSI stripped...
//	--- exit 0 (3.2s)
//	PASS  plan is a no-op
//
//	SKIP  delete table (dry run: set FIN_ALLOW_DELETE=1 to approve)
//	DRIFT  queue policy differs from code
//
//	=== SUMMARY 2026-10-09T12:01:00Z swim 1 job=3f2a9c1e-... pass=3 fail=0 skip=1 drift=1 exit=0 duration=60.0s
//	=== END
//
// Result lines are `KIND  label` with KIND one of PASS, FAIL, SKIP, DRIFT,
// APPROVED, STOP. FAIL lines end in `(exit N)`.
package logparse

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Markers and result kinds.
const (
	RoundStart = "=== ROUND START"
	StepStart  = "=== STEP"
	Summary    = "=== SUMMARY"
	End        = "=== END"
	OutputMark = "--- output"
	ExitMark   = "--- exit"
	IntrMark   = "--- interrupted"

	Pass     = "PASS"
	Fail     = "FAIL"
	Skip     = "SKIP"
	Drift    = "DRIFT"
	Approved = "APPROVED"
	Stop     = "STOP"
)

// ResultLine formats a result line.
func ResultLine(kind, label, detail string) string {
	s := kind + "  " + label
	if detail != "" {
		s += " (" + detail + ")"
	}
	return s
}

// Result is one parsed result line.
type Result struct {
	Kind   string
	Label  string
	Detail string
}

// Text renders the result as it appears in the log.
func (r Result) Text() string { return ResultLine(r.Kind, r.Label, r.Detail) }

// Round is the parsed tail of a log: everything since the last ROUND START.
type Round struct {
	Found     bool // a ROUND START marker was present
	Title     string
	Job       string
	StartedAt string
	Steps     int
	Results   []Result
	Pass      int
	Fail      int
	Skip      int
	Drift     int
	Finished  bool // a SUMMARY was written
	ExitCode  int
	Duration  string
	Interrupt bool
}

// Failed returns the FAIL and STOP results.
func (r *Round) Failed() []Result {
	var out []Result
	for _, res := range r.Results {
		if res.Kind == Fail || res.Kind == Stop {
			out = append(out, res)
		}
	}
	return out
}

var (
	resultRE  = regexp.MustCompile(`^(PASS|FAIL|SKIP|DRIFT|APPROVED|STOP)  (.*?)(?: \((.*)\))?$`)
	summaryKV = regexp.MustCompile(`(\w+)=(\S+)`)
	jobKV     = regexp.MustCompile(` job=(\S+)`)
)

// ParseFile parses the current round of the log at path. A missing log
// yields an empty Round and no error.
func ParseFile(path string) (*Round, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return &Round{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f)
}

// Parse reads a whole log and returns its last round.
func Parse(r interface{ Read([]byte) (int, error) }) (*Round, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	cur := &Round{}
	inOutput := false
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, RoundStart):
			cur = &Round{Found: true}
			if f := strings.Fields(line); len(f) >= 4 {
				cur.StartedAt = f[3]
			}
			if m := jobKV.FindStringSubmatch(line); m != nil {
				cur.Job = m[1]
			}
			inOutput = false
			continue
		case strings.HasPrefix(line, StepStart):
			cur.Steps++
			inOutput = false
			continue
		case line == OutputMark:
			inOutput = true
			continue
		case strings.HasPrefix(line, ExitMark):
			inOutput = false
			continue
		case strings.HasPrefix(line, IntrMark):
			inOutput = false
			cur.Interrupt = true
			continue
		}
		if inOutput {
			continue
		}
		switch {
		case strings.HasPrefix(line, "Round: ") && cur.Title == "":
			cur.Title = strings.TrimSpace(strings.TrimPrefix(line, "Round: "))
		case strings.HasPrefix(line, Summary):
			cur.Finished = true
			for _, m := range summaryKV.FindAllStringSubmatch(line, -1) {
				switch m[1] {
				case "exit":
					cur.ExitCode, _ = strconv.Atoi(m[2])
				case "duration":
					cur.Duration = m[2]
				}
			}
		default:
			if m := resultRE.FindStringSubmatch(line); m != nil {
				res := Result{Kind: m[1], Label: m[2], Detail: m[3]}
				cur.Results = append(cur.Results, res)
				switch res.Kind {
				case Pass:
					cur.Pass++
				case Fail:
					cur.Fail++
				case Skip:
					cur.Skip++
				case Drift:
					cur.Drift++
				}
			}
		}
	}
	return cur, sc.Err()
}

// SummaryLine formats the SUMMARY marker.
func SummaryLine(ts string, lane int, r *Round, exit int, duration string) string {
	return fmt.Sprintf("%s %s swim %d job=%s pass=%d fail=%d skip=%d drift=%d exit=%d duration=%s",
		Summary, ts, lane, r.Job, r.Pass, r.Fail, r.Skip, r.Drift, exit, duration)
}
