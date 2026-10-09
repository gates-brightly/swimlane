// Package logparse defines the lane log grammar, writes it, and reads it back.
//
// Syntax 2 (current) is built to be read with cat or an editor. Every line
// says what it is by its first characters, so command output can never be
// mistaken for a result:
//
//	# swim lane log | syntax 2 | swim 1                       file header
//
//	== ROUND 2026-10-09T12:00:00Z  job=3f2a9c1e-...  Cut over orders-api
//	   script: lane.1.sh | owner: zach | after: 1 | git: main@abc1234 | ...
//
//	-- stage snapshot  12:00:01                               a stage begins
//	  PASS  cfn template                           0.4s  12:00:01
//	        $ aws cloudformation get-template --stack-name orders-prod
//	        | {"TemplateBody": ...}                           command output
//	        saved: .swim/snapshots/lane1-...-cfn-template.txt
//
//	-- stage check  12:00:02
//	  FAIL  tf plan (exit 2)                       3.1s  12:00:02
//	        $ terraform plan -detailed-exitcode
//	        | Error: ...
//	  STOP  gate failed: tf plan
//
//	== END FAIL  pass=1 fail=1 skip=0 drift=0  exit=1  3.6s  2026-10-09T12:00:04Z
//	   stages: snapshot PASS | check FAIL | change none | verify none
//	   failed: FAIL  tf plan (exit 2); STOP  gate failed: tf plan
//
// Result lines are two spaces, a KIND (PASS FAIL SKIP DRIFT APPROVED STOP
// WARN), two spaces and a label. Step results end with a duration and a
// clock time; marks (SKIP, DRIFT, APPROVED, STOP, WARN) carry a detail in
// parentheses instead. Lines indented 8 spaces belong to the step above.
//
// Syntax 1 logs (=== ROUND START / === STEP / --- output framing) are still
// read, and ConvertV1 rewrites them as syntax 2.
package logparse

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gates-brightly/swimlane/internal/syntax"
)

// Result kinds.
const (
	Pass     = "PASS"
	Fail     = "FAIL"
	Skip     = "SKIP"
	Drift    = "DRIFT"
	Approved = "APPROVED"
	Stop     = "STOP"
	Warn     = "WARN"
)

// Syntax 2 markers and indents.
const (
	RoundMark = "== ROUND "
	EndMark   = "== END "
	StageMark = "-- stage "
	Indent    = "        " // lines that belong to a step
	CmdPrefix = Indent + "$ "
	OutPrefix = Indent + "| "
	ctxIndent = "   " // round context and END detail lines
)

// ResultLine formats a result as text, e.g. "FAIL  tf plan (exit 2)".
func ResultLine(kind, label, detail string) string {
	s := kind + "  " + label
	if detail != "" {
		s += " (" + detail + ")"
	}
	return s
}

// StepLine is a step's result line: the result, padded, then its duration
// and clock time.
func StepLine(kind, label, detail, dur, clock string) string {
	text := ResultLine(kind, label, detail)
	pad := 44 - utf8.RuneCountInString(text)
	if pad < 2 {
		pad = 2
	}
	return "  " + text + strings.Repeat(" ", pad) + fmt.Sprintf("%6s  %s", dur, clock)
}

// MarkLine is a result line without a duration (SKIP, DRIFT, STOP, ...).
func MarkLine(kind, label, detail string) string { return "  " + ResultLine(kind, label, detail) }

// StageLine starts a stage.
func StageLine(name, clock string) string { return StageMark + name + "  " + clock }

// KV is one key/value of a round's context line.
type KV struct{ K, V string }

// RoundHeader starts a round: a blank line, the ROUND line and its context.
func RoundHeader(ts, job, title string, ctx []KV) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n%s%s  job=%s  %s\n", RoundMark, ts, job, title)
	parts := make([]string, 0, len(ctx))
	for _, kv := range ctx {
		parts = append(parts, kv.K+": "+kv.V)
	}
	if len(parts) > 0 {
		b.WriteString(ctxIndent + strings.Join(parts, " | ") + "\n")
	}
	return b.String()
}

// EndBlock closes a round: totals, the stage results and the failures.
func EndBlock(state string, r *Round, exit int, dur, ts string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n%s%s  pass=%d fail=%d skip=%d drift=%d  exit=%d  %s  %s",
		EndMark, state, r.Pass, r.Fail, r.Skip, r.Drift, exit, dur, ts)
	if r.Run != "" {
		fmt.Fprintf(&b, "  run=%s", r.Run)
	}
	b.WriteString("\n")
	b.WriteString(ctxIndent + "stages: " + StagesText(r.StageResults()) + "\n")
	if failed := r.Failed(); len(failed) > 0 {
		texts := make([]string, len(failed))
		for i, f := range failed {
			texts[i] = f.Text()
		}
		b.WriteString(ctxIndent + "failed: " + strings.Join(texts, "; ") + "\n")
	}
	return b.String()
}

// Result is one parsed result line.
type Result struct {
	Kind   string
	Label  string
	Detail string
	Stage  string // stage it ran in (setup before any stage line)
	Step   bool   // a step result (has a duration), not a mark
}

// Text renders the result as text, e.g. "FAIL  tf plan (exit 2)".
func (r Result) Text() string { return ResultLine(r.Kind, r.Label, r.Detail) }

// StageResult is one stage's outcome.
type StageResult struct{ Name, State string }

// StagesText renders stage results: "snapshot PASS | check FAIL | ...".
func StagesText(rs []StageResult) string {
	parts := make([]string, len(rs))
	for i, s := range rs {
		parts[i] = s.Name + " " + s.State
	}
	return strings.Join(parts, " | ")
}

// Round is the parsed tail of a log: everything since the last round start.
type Round struct {
	Syntax    int
	Found     bool // a round start was present
	Title     string
	Job       string
	StartedAt string
	Steps     int
	Results   []Result
	Pass      int
	Fail      int
	Skip      int
	Drift     int
	Finished  bool // the round's END (SUMMARY in syntax 1) was written
	ExitCode  int
	Duration  string
	Interrupt bool
	Stage     string   // stage in effect at the end of the log
	Declared  []string // stage lines seen, in order
	Run       string   // swim run id (from the round's context line)
	// Context holds the round header's key/value context line.
	Context map[string]string
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

// StageResults returns each standard stage's outcome, in order, preceded by
// setup if anything ran there. A stage is FAIL if anything in it failed or
// stopped, PASS if a step passed, SKIP if it only skipped, else none.
func (r *Round) StageResults() []StageResult {
	state := func(name string) (string, bool) {
		pass, skip, any := false, false, false
		for _, res := range r.Results {
			if res.Stage != name {
				continue
			}
			any = true
			switch res.Kind {
			case Fail, Stop:
				return "FAIL", true
			case Pass:
				pass = true
			case Skip:
				skip = true
			}
		}
		switch {
		case pass:
			return "PASS", true
		case skip:
			return "SKIP", true
		}
		return "none", any
	}
	var out []StageResult
	if s, any := state(syntax.Setup); any {
		out = append(out, StageResult{syntax.Setup, s})
	}
	for _, name := range syntax.Stages {
		s, _ := state(name)
		out = append(out, StageResult{name, s})
	}
	return out
}

func (r *Round) add(res Result) {
	r.Results = append(r.Results, res)
	if res.Step {
		r.Steps++
	}
	switch res.Kind {
	case Pass:
		r.Pass++
	case Fail:
		r.Fail++
	case Skip:
		r.Skip++
	case Drift:
		r.Drift++
	}
}

// ParseFile parses the current round of the log at path. A missing log
// yields an empty Round and no error.
func ParseFile(path string) (*Round, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return &Round{Syntax: syntax.Current}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}

// Parse reads a whole log (either syntax) and returns its last round.
func Parse(rd io.Reader) (*Round, error) {
	br := bufio.NewReader(rd)
	first, _ := br.Peek(64)
	v := syntax.OfLog(strings.NewReader(string(first)))
	if err := syntax.Check("lane log", v); err != nil {
		return nil, err
	}
	if v == 1 {
		return parseV1(br)
	}
	return parseV2(br)
}

func scanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	return sc
}

var (
	resultV2RE = regexp.MustCompile(`^  (PASS|FAIL|SKIP|DRIFT|APPROVED|STOP|WARN)  (.*)$`)
	timedRE    = regexp.MustCompile(`^(.*?)\s+(\d+(?:\.\d+)?s)\s+(\d\d:\d\d:\d\d)$`)
	labelRE    = regexp.MustCompile(`^(.*?)(?: \((.*)\))?$`)
	roundV2RE  = regexp.MustCompile(`^== ROUND (\S+)\s+job=(\S+)\s+(.*)$`)
	endV2RE    = regexp.MustCompile(`^== END (\S+)\s+pass=\d+ fail=\d+ skip=\d+ drift=\d+\s+exit=(-?\d+)\s+(\S+)`)
)

// splitLabel separates "label (detail)".
func splitLabel(s string) (string, string) {
	m := labelRE.FindStringSubmatch(strings.TrimRight(s, " "))
	return m[1], m[2]
}

func parseV2(rd io.Reader) (*Round, error) {
	sc := scanner(rd)
	cur := &Round{Syntax: 2, Stage: syntax.Setup}
	ctxNext := false // the line after == ROUND may be its context
	for sc.Scan() {
		line := sc.Text()
		inCtx := ctxNext
		ctxNext = false
		switch {
		case strings.HasPrefix(line, RoundMark):
			cur = &Round{Syntax: 2, Found: true, Stage: syntax.Setup}
			if m := roundV2RE.FindStringSubmatch(line); m != nil {
				cur.StartedAt, cur.Job, cur.Title = m[1], m[2], strings.TrimSpace(m[3])
			}
			ctxNext = true
			continue
		case strings.HasPrefix(line, EndMark):
			cur.Finished = true
			if m := endV2RE.FindStringSubmatch(line); m != nil {
				cur.ExitCode, _ = strconv.Atoi(m[2])
				cur.Duration = m[3]
				if m[1] == "INTERRUPTED" {
					cur.Interrupt = true
				}
			}
		case strings.HasPrefix(line, StageMark):
			if f := strings.Fields(strings.TrimPrefix(line, StageMark)); len(f) > 0 {
				cur.Stage = f[0]
				cur.Declared = append(cur.Declared, f[0])
			}
		case inCtx && strings.HasPrefix(line, ctxIndent) && !strings.HasPrefix(line, Indent):
			cur.Context = ParseContext(line)
			cur.Run = cur.Context["run"]
		case strings.HasPrefix(line, Indent), strings.HasPrefix(line, ctxIndent):
			// step detail, round context or END detail: not a result
		default:
			m := resultV2RE.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			res := Result{Kind: m[1], Stage: cur.Stage}
			rest := m[2]
			if t := timedRE.FindStringSubmatch(rest); t != nil {
				res.Step, rest = true, t[1]
			}
			res.Label, res.Detail = splitLabel(rest)
			if strings.Contains(res.Detail, "interrupted") {
				cur.Interrupt = true
			}
			cur.add(res)
		}
	}
	return cur, sc.Err()
}

// ParseRound parses one round's lines (as cut from a syntax 2 log).
func ParseRound(lines []string) (*Round, error) {
	text := strings.Join(lines, "\n") + "\n"
	if len(lines) > 0 && strings.HasPrefix(lines[0], RoundMark) {
		text = syntax.LogHeader(0) + "\n" + text
	}
	return Parse(strings.NewReader(text))
}

// ParseContext reads a round's context line, "   k: v | k: v | ...".
func ParseContext(line string) map[string]string {
	ctx := map[string]string{}
	for _, part := range strings.Split(strings.TrimSpace(line), " | ") {
		if k, v, ok := strings.Cut(part, ": "); ok {
			ctx[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return ctx
}

// Syntax 1 markers, kept for reading and converting old logs.
const (
	v1RoundStart = "=== ROUND START"
	v1StepStart  = "=== STEP"
	v1Summary    = "=== SUMMARY"
	v1End        = "=== END"
	v1Output     = "--- output"
	v1Exit       = "--- exit"
	v1Intr       = "--- interrupted"
)

var (
	resultV1RE = regexp.MustCompile(`^(PASS|FAIL|SKIP|DRIFT|APPROVED|STOP)  (.*?)(?: \((.*)\))?$`)
	kvRE       = regexp.MustCompile(`(\w+)=(\S+)`)
)

func parseV1(rd io.Reader) (*Round, error) {
	sc := scanner(rd)
	cur := &Round{Syntax: 1, Stage: syntax.Setup}
	inOutput, inStep := false, false
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, v1RoundStart):
			cur = &Round{Syntax: 1, Found: true, Stage: syntax.Setup}
			if f := strings.Fields(line); len(f) >= 4 {
				cur.StartedAt = f[3]
			}
			if m := kvRE.FindAllStringSubmatch(line, -1); m != nil {
				for _, kv := range m {
					if kv[1] == "job" {
						cur.Job = kv[2]
					}
				}
			}
			inOutput, inStep = false, false
			continue
		case strings.HasPrefix(line, v1StepStart):
			inOutput, inStep = false, true
			continue
		case line == v1Output:
			inOutput = true
			continue
		case strings.HasPrefix(line, v1Exit):
			inOutput = false
			continue
		case strings.HasPrefix(line, v1Intr):
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
		case strings.HasPrefix(line, v1Summary):
			cur.Finished = true
			for _, m := range kvRE.FindAllStringSubmatch(line, -1) {
				switch m[1] {
				case "exit":
					cur.ExitCode, _ = strconv.Atoi(m[2])
				case "duration":
					cur.Duration = m[2]
				}
			}
		default:
			if m := resultV1RE.FindStringSubmatch(line); m != nil {
				step := inStep && (m[1] == Pass || m[1] == Fail)
				cur.add(Result{Kind: m[1], Label: m[2], Detail: m[3], Stage: syntax.Setup, Step: step})
				if step {
					inStep = false
				}
			}
		}
	}
	return cur, sc.Err()
}

// ConvertV1 rewrites a syntax 1 log as syntax 2. Every round, step, output
// line and result is kept; steps land in the setup stage (syntax 1 had no
// stages), and a missing END is left missing (an unfinished round).
func ConvertV1(rd io.Reader, w io.Writer, lane int) error {
	sc := scanner(rd)
	bw := bufio.NewWriter(w)
	fmt.Fprintln(bw, syntax.LogHeader(lane))

	type step struct {
		label, ts, cmd, exit, dur string
		output                    []string
		inOutput, haveExit        bool
		interrupted               bool
	}
	var (
		round      *Round // results so far, for the END block
		pendingHdr struct {
			ts, job, title, script, pid string
			open                        bool
		}
		st      *step
		inEnd   bool
		endLine string
	)
	clockOf := func(ts string) string {
		if len(ts) >= 19 {
			return ts[11:19]
		}
		return "--:--:--"
	}
	flushHeader := func() {
		if !pendingHdr.open {
			return
		}
		var ctx []KV
		if pendingHdr.script != "" {
			ctx = append(ctx, KV{"script", pendingHdr.script})
		}
		if pendingHdr.pid != "" {
			ctx = append(ctx, KV{"pid", pendingHdr.pid})
		}
		ctx = append(ctx, KV{"migrated", "from syntax 1"})
		title := pendingHdr.title
		if title == "" {
			title = "(no Round: line)"
		}
		bw.WriteString(RoundHeader(pendingHdr.ts, pendingHdr.job, title, ctx))
		pendingHdr.open = false
	}
	// emitStep writes a step block; kind/label/detail come from its result
	// line, or from the exit code for an unlabelled step.
	emitStep := func(kind, label, detail string) {
		if st == nil {
			return
		}
		flushHeader()
		if kind == "" {
			kind, detail = Pass, ""
			if st.exit != "0" && st.exit != "" {
				kind, detail = Fail, "exit "+st.exit
			}
			if label = st.label; label == "" {
				label = "(unlabelled step)"
			}
		}
		dur := st.dur
		if dur == "" {
			dur = "?"
		}
		bw.WriteString(StepLine(kind, label, detail, dur, clockOf(st.ts)) + "\n")
		if st.cmd != "" {
			bw.WriteString(CmdPrefix + st.cmd + "\n")
		}
		for _, o := range st.output {
			bw.WriteString(OutPrefix + o + "\n")
		}
		round.add(Result{Kind: kind, Label: label, Detail: detail, Stage: syntax.Setup, Step: true})
		st = nil
	}
	exitRE := regexp.MustCompile(`exit (-?\d+) \(([\d.]+s)\)$`)

	for sc.Scan() {
		line := sc.Text()
		if st != nil && st.inOutput {
			switch {
			case strings.HasPrefix(line, v1Exit), strings.HasPrefix(line, v1Intr):
				st.inOutput, st.haveExit = false, true
				st.interrupted = strings.HasPrefix(line, v1Intr)
				if m := exitRE.FindStringSubmatch(line); m != nil {
					st.exit, st.dur = m[1], m[2]
				}
			default:
				st.output = append(st.output, line)
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, v1RoundStart):
			emitStep("", "", "")
			if inEnd {
				bw.WriteString(endLine)
				inEnd = false
			}
			f := strings.Fields(line)
			pendingHdr.ts, pendingHdr.job, pendingHdr.title, pendingHdr.script, pendingHdr.pid = "", "-", "", "", ""
			if len(f) >= 4 {
				pendingHdr.ts = f[3]
			}
			for _, kv := range kvRE.FindAllStringSubmatch(line, -1) {
				if kv[1] == "job" {
					pendingHdr.job = kv[2]
				}
			}
			pendingHdr.open = true
			round = &Round{}
		case pendingHdr.open && strings.HasPrefix(line, "Round: "):
			pendingHdr.title = strings.TrimSpace(strings.TrimPrefix(line, "Round: "))
		case pendingHdr.open && strings.HasPrefix(line, "script: ") || pendingHdr.open && strings.HasPrefix(line, "runbook: "):
			for _, part := range strings.Split(line, "   ") {
				k, v, ok := strings.Cut(strings.TrimSpace(part), ": ")
				if !ok {
					continue
				}
				switch k {
				case "script", "runbook":
					pendingHdr.script = v
				case "pid":
					pendingHdr.pid = v
				}
			}
		case strings.HasPrefix(line, v1StepStart):
			emitStep("", "", "")
			if round == nil {
				round = &Round{}
			}
			f := strings.SplitN(line, " ", 4)
			st = &step{}
			if len(f) >= 3 {
				st.ts = f[2]
			}
			if len(f) == 4 {
				st.label = f[3]
			}
		case st != nil && !st.haveExit && strings.HasPrefix(line, "$ "):
			st.cmd = strings.TrimPrefix(line, "$ ")
		case st != nil && !st.haveExit && line == v1Output:
			st.inOutput = true
		case st != nil && !st.haveExit:
			// cwd:/git:/env: step context lines: dropped (the round context
			// in syntax 2 carries what's needed)
		case strings.HasPrefix(line, "saved: "):
			bw.WriteString(Indent + line + "\n")
		case strings.HasPrefix(line, v1Summary):
			emitStep("", "", "")
			flushHeader()
			exit, dur, ts := 0, "?", ""
			if f := strings.Fields(line); len(f) >= 3 {
				ts = f[2]
			}
			for _, kv := range kvRE.FindAllStringSubmatch(line, -1) {
				switch kv[1] {
				case "exit":
					exit, _ = strconv.Atoi(kv[2])
				case "duration":
					dur = kv[2]
				}
			}
			state := "PASS"
			switch {
			case exit == 130 || exit == 143 || exit == 129:
				state = "INTERRUPTED"
			case exit != 0:
				state = "FAIL"
			}
			if round == nil {
				round = &Round{}
			}
			endLine = EndBlock(state, round, exit, dur, ts)
			inEnd = true
		case inEnd && strings.HasPrefix(line, "failed: "):
			// the END block recomputes failures from the results
		case line == v1End:
			if inEnd {
				bw.WriteString(endLine)
				inEnd = false
			}
		case strings.TrimSpace(line) == "":
		default:
			m := resultV1RE.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			if st != nil && st.haveExit && (m[1] == Pass || m[1] == Fail) {
				emitStep(m[1], m[2], m[3])
				continue
			}
			emitStep("", "", "")
			flushHeader()
			if round == nil {
				round = &Round{}
			}
			bw.WriteString(MarkLine(m[1], m[2], m[3]) + "\n")
			round.add(Result{Kind: m[1], Label: m[2], Detail: m[3], Stage: syntax.Setup})
		}
	}
	emitStep("", "", "")
	flushHeader()
	if inEnd {
		bw.WriteString(endLine)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return bw.Flush()
}

// EnsureV2 makes path ready for syntax 2 lines: a missing or empty log gets
// the header; a syntax 1 log is converted in place (the original is kept as
// path.syntax1.bak); a log in a newer syntax is an error.
func EnsureV2(path string, lane int) error {
	switch v := syntax.OfLogFile(path); {
	case v == 0:
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(syntax.LogHeader(lane)+"\n"), 0o644)
	case v == 1:
		return convertInPlace(path, lane, path+".syntax1.bak")
	default:
		return syntax.Check(path, v)
	}
}

// convertInPlace rewrites a syntax 1 log as syntax 2, copying the original to
// backup first (if backup isn't "").
func convertInPlace(path string, lane int, backup string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if backup != "" {
		if err := os.WriteFile(backup, src, 0o644); err != nil {
			return err
		}
	}
	var out strings.Builder
	if err := ConvertV1(strings.NewReader(string(src)), &out, lane); err != nil {
		return err
	}
	tmp := path + ".migrating"
	if err := os.WriteFile(tmp, []byte(out.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ConvertFile rewrites a syntax 1 log file as syntax 2 in place.
func ConvertFile(path string, lane int) error { return convertInPlace(path, lane, "") }
