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
// Result lines are two spaces, a KIND (PASS FAIL BLOCKED SKIP DRIFT APPROVED
// STOP WARN), two spaces and a label. Step results end with a duration and a
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
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gates-brightly/swimlane/internal/chars"
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
	Blocked  = "BLOCKED" // a command swim refused to run (counts as a failure)
	Msg      = "MSG"     // a round's status message (swim msg); never a result
	Checked  = "CHECKED" // a read-only check stopped where change would begin; never a result
	// PAUSE and RESUME (swim pause, which swim no longer has) are in
	// older logs: read as marks, never a result.
	Pause  = "PAUSE"
	Resume = "RESUME"
)

// Syntax 2 markers and indents.
const (
	RoundMark = "== ROUND "
	EndMark   = "== END "
	StageMark = "-- stage "
	// HandlerMark starts an outcome handler's section (on_success,
	// on_failure, on_exit): its results don't count toward the round's.
	HandlerMark = "-- handler "
	Indent      = "        " // lines that belong to a step
	CmdPrefix   = Indent + "$ "
	OutPrefix   = Indent + "| "
	ctxIndent   = "   " // round context and END detail lines
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

// MsgText is a status message as the log records it: "migrating billing
// (7/25, 12:04:10)".
func MsgText(text, progress, clock string) string {
	detail := clock
	if progress != "" {
		detail = progress + ", " + clock
	}
	return ResultLine(Msg, text, detail)
}

// HandlerLine starts an outcome handler's section.
func HandlerLine(name, clock string) string { return HandlerMark + name + "  " + clock }

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
	if hs := r.HandlerResults(); len(hs) > 0 {
		b.WriteString(ctxIndent + "handlers: " + StagesText(hs) + "\n")
	}
	if failed := r.Failed(); len(failed) > 0 {
		texts := make([]string, len(failed))
		for i, f := range failed {
			texts[i] = f.Text()
		}
		b.WriteString(ctxIndent + "failed: " + strings.Join(texts, "; ") + "\n")
	}
	return b.String()
}

// NotStartedRound is a round that ended before its script got going: a
// lane swim refused to start (its lock, its manifest), or one that exited
// before lane_init's round header. It says why in a STOP line, with the
// last lines the lane printed (masked by the caller) as its output, so swim
// log and swim report show it like any failed round.
func NotStartedRound(ts, job, title, run, why string, printed []string, exit int) string {
	if job == "" {
		job = "-"
	}
	var ctx []KV
	if run != "" {
		ctx = append(ctx, KV{K: "run", V: run})
	}
	text := RoundHeader(ts, job, title, ctx)
	stop := Result{Kind: Stop, Label: "not started: " + why, Stage: "setup"}
	text += MarkLine(stop.Kind, stop.Label, "") + "\n"
	for _, l := range printed {
		text += OutPrefix + l + "\n"
	}
	return text + EndBlock("FAIL", &Round{Results: []Result{stop}, Run: run}, exit, "0.0s", ts)
}

// Result is one parsed result line.
type Result struct {
	Kind   string
	Label  string
	Detail string
	Stage  string // stage it ran in (setup before any stage line)
	// Handler is the outcome handler it ran in (on_failure, ...); "" for
	// the round itself. Handler results never count toward the round's.
	Handler string
	Step    bool    // a step result (has a duration), not a mark
	Dur     float64 // a step's duration in seconds (syntax 2; 0 if unknown)
	Clock   string  // a step's start time, hh:mm:ss local (syntax 2)
	// The step's block (syntax 2): its command line, output lines (with
	// "-- attempt" separators kept) and saved snapshot path.
	Cmd    string
	Output []string
	Saved  string
	// Key and Target are the step's own (run --key, --target, # Targets:),
	// as logged: its name across rounds, what it talks to.
	Key, Target string
	// Messages are the status messages (swim msg) posted while the step ran.
	Messages []string
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
	DryRun    bool     // the round ended SKIP: nothing failed, but a guarded step dry-ran
	Stage     string   // stage in effect at the end of the log
	Declared  []string // stage lines seen, in order
	Handlers  []string // handler sections seen, in order
	Checked   bool     // a read-only check reached the change stage (CHECKED)
	Run       string   // swim run id (from the round's context line)
	// Context holds the round header's key/value context line.
	Context map[string]string

	handler string // the handler section being read
}

// Skipped returns the round's SKIP results: its guarded steps that dry-ran.
func (r *Round) Skipped() []Result {
	var out []Result
	for _, res := range r.Results {
		if res.Handler == "" && res.Kind == Skip {
			out = append(out, res)
		}
	}
	return out
}

// DryRunReason says why a round that only got as far as a dry run didn't
// pass: "dry run: <first guarded step>", and how many more there were.
func (r *Round) DryRunReason() string {
	sk := r.Skipped()
	if len(sk) == 0 {
		return ""
	}
	why := "dry run: " + sk[0].Label
	if len(sk) > 1 {
		why += fmt.Sprintf(" (+%d more)", len(sk)-1)
	}
	return why
}

// Failed returns the FAIL and STOP results.
func (r *Round) Failed() []Result {
	var out []Result
	for _, res := range r.Results {
		if res.Handler == "" && (res.Kind == Fail || res.Kind == Stop || res.Kind == Blocked) {
			out = append(out, res)
		}
	}
	return out
}

// HandlerResults returns each handler that ran and its outcome: FAIL if a
// step in it failed or it stopped, PASS if a step passed, else SKIP or none.
func (r *Round) HandlerResults() []StageResult {
	var out []StageResult
	for _, h := range r.Handlers {
		state := "none"
		for _, res := range r.Results {
			if res.Handler != h {
				continue
			}
			switch {
			case res.Kind == Fail || res.Kind == Stop || res.Kind == Blocked:
				state = "FAIL"
			case res.Kind == Pass && state != "FAIL":
				state = "PASS"
			case res.Kind == Skip && state == "none":
				state = "SKIP"
			}
		}
		out = append(out, StageResult{h, state})
	}
	return out
}

// HandlerState is handler name's outcome ("" if it didn't run).
func (r *Round) HandlerState(name string) string {
	for _, h := range r.HandlerResults() {
		if h.Name == name {
			return h.State
		}
	}
	return ""
}

// StageResults returns each standard stage's outcome, in order, preceded by
// setup if anything ran there. A stage is FAIL if anything in it failed or
// stopped, PASS if a step passed, SKIP if it only skipped, else none.
func (r *Round) StageResults() []StageResult {
	state := func(name string) (string, bool) {
		pass, skip, any := false, false, false
		for _, res := range r.Results {
			if res.Stage != name || res.Handler != "" {
				continue
			}
			any = true
			switch res.Kind {
			case Fail, Stop, Blocked:
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
	if res.Handler != "" {
		return // a handler's results don't count toward the round's
	}
	if res.Step {
		r.Steps++
	}
	switch res.Kind {
	case Pass:
		r.Pass++
	case Fail, Blocked:
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

// Parse reads a whole log and returns its last round.
func Parse(rd io.Reader) (*Round, error) {
	br := bufio.NewReader(rd)
	first, _ := br.Peek(64)
	v := syntax.OfLog(strings.NewReader(string(first)))
	if err := syntax.Check("lane log", v); err != nil {
		return nil, err
	}
	if v == 1 {
		return nil, fmt.Errorf("lane log is swim syntax 1, from before swim versioned its files, which this swim doesn't read")
	}
	return parseV2(br)
}

func scanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	return sc
}

// The lines swim writes, read with string functions: the formats are
// swim's own (StepLine, MarkLine, RoundHeader, EndBlock).

// v2Kinds are the result kinds of a syntax 2 result line.
var v2Kinds = map[string]bool{Pass: true, Fail: true, Blocked: true, Skip: true, Drift: true, Approved: true, Stop: true,
	Warn: true, Msg: true, Checked: true, Pause: true, Resume: true}

// resultV2 reads "  KIND  rest": the kind and the rest.
func resultV2(line string) (kind, rest string, ok bool) { return SplitResult(line) }

// SplitResult reads a result line, "  KIND  rest": its kind (one a log
// holds, or one of extra: RUNNING, which swim log shows) and the rest.
func SplitResult(line string, extra ...string) (kind, rest string, ok bool) {
	body, ok := strings.CutPrefix(line, "  ")
	if !ok {
		return "", "", false
	}
	kind, rest, ok = strings.Cut(body, "  ")
	if !ok || !v2Kinds[kind] && !slices.Contains(extra, kind) {
		return "", "", false
	}
	return kind, rest, true
}

// SplitTimed is timed: a step result's text, duration and clock time.
func SplitTimed(s string) (head, dur, clock string, ok bool) { return timed(s) }

// EndExit is the exit code an END line records.
func EndExit(line string) (int, bool) {
	if !strings.HasPrefix(line, EndMark) {
		return 0, false
	}
	for _, f := range strings.Fields(line) {
		if v, ok := strings.CutPrefix(f, "exit="); ok {
			n, err := strconv.Atoi(v)
			return n, err == nil
		}
	}
	return 0, false
}

// RoundJob is the job a ROUND line names.
func RoundJob(line string) (string, bool) {
	_, job, _, ok := roundV2(line)
	return job, ok
}

// timed splits a step's "label (detail)    1.2s  12:04:10" into the text,
// its duration ("1.2s") and its clock time.
func timed(s string) (head, dur, clock string, ok bool) {
	f := strings.Fields(s)
	if len(f) < 3 {
		return "", "", "", false
	}
	clock, dur = f[len(f)-1], f[len(f)-2]
	if len(clock) != 8 || clock[2] != ':' || clock[5] != ':' || !chars.Digits(clock[:2]) || !chars.Digits(clock[3:5]) || !chars.Digits(clock[6:]) {
		return "", "", "", false
	}
	num, isDur := strings.CutSuffix(dur, "s")
	whole, frac, dotted := strings.Cut(num, ".")
	if !isDur || !chars.Digits(whole) || dotted && !chars.Digits(frac) {
		return "", "", "", false
	}
	head = strings.TrimRight(s, " \t")
	head = strings.TrimRight(strings.TrimSuffix(head, clock), " \t")
	head, cut := strings.CutSuffix(head, dur)
	if !cut || head == "" || !strings.ContainsAny(head[len(head)-1:], " \t") {
		return "", "", "", false
	}
	return strings.TrimRight(head, " \t"), dur, clock, true
}

// splitLabel separates "label (detail)": the detail is what follows the
// first " (" when the text ends with ")".
func splitLabel(s string) (string, string) {
	s = strings.TrimRight(s, " ")
	if i := strings.Index(s, " ("); i >= 0 && strings.HasSuffix(s, ")") {
		return s[:i], s[i+2 : len(s)-1]
	}
	return s, ""
}

// roundV2 reads "== ROUND <ts>  job=<job>  <title>".
func roundV2(line string) (ts, job, title string, ok bool) {
	rest, _ := strings.CutPrefix(line, RoundMark)
	ts, rest, ok = strings.Cut(rest, " ")
	rest = strings.TrimLeft(rest, " \t")
	if !ok || ts == "" || !strings.HasPrefix(rest, "job=") {
		return "", "", "", false
	}
	job, title, ok = strings.Cut(strings.TrimPrefix(rest, "job="), " ")
	if !ok || job == "" {
		return "", "", "", false
	}
	return ts, job, strings.TrimSpace(title), true
}

// endV2 reads "== END <state>  pass=N fail=N skip=N drift=N  exit=N  <dur> ...".
func endV2(line string) (state string, exit int, dur string, ok bool) {
	f := strings.Fields(strings.TrimPrefix(line, EndMark))
	if len(f) < 7 {
		return "", 0, "", false
	}
	for i, k := range []string{"pass=", "fail=", "skip=", "drift="} {
		if n, has := strings.CutPrefix(f[1+i], k); !has || !chars.Digits(n) {
			return "", 0, "", false
		}
	}
	code, has := strings.CutPrefix(f[5], "exit=")
	exit, err := strconv.Atoi(code)
	if !has || err != nil {
		return "", 0, "", false
	}
	return f[0], exit, f[6], true
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
			if ts, job, title, ok := roundV2(line); ok {
				cur.StartedAt, cur.Job, cur.Title = ts, job, title
			}
			ctxNext = true
			continue
		case strings.HasPrefix(line, EndMark):
			if !cur.Found {
				continue // an END with no ROUND before it ends nothing
			}
			cur.Finished = true
			if state, exit, dur, ok := endV2(line); ok {
				cur.ExitCode, cur.Duration = exit, dur
				switch state {
				case "INTERRUPTED":
					cur.Interrupt = true
				case Skip:
					cur.DryRun = true
				}
			}
		case strings.HasPrefix(line, HandlerMark):
			if f := strings.Fields(strings.TrimPrefix(line, HandlerMark)); len(f) > 0 {
				cur.handler = f[0]
				cur.Handlers = append(cur.Handlers, f[0])
			}
		case strings.HasPrefix(line, StageMark):
			if f := strings.Fields(strings.TrimPrefix(line, StageMark)); len(f) > 0 {
				cur.Stage = f[0]
				cur.Declared = append(cur.Declared, f[0])
			}
		case inCtx && strings.HasPrefix(line, ctxIndent) && !strings.HasPrefix(line, Indent):
			cur.Context = ParseContext(line)
			cur.Run = cur.Context["run"]
		case strings.HasPrefix(line, Indent):
			// A step's block: its command, output and snapshot.
			if n := len(cur.Results); n > 0 && cur.Results[n-1].Step {
				res := &cur.Results[n-1]
				body := strings.TrimPrefix(line, Indent)
				switch {
				case strings.HasPrefix(body, "$ ") && res.Cmd == "" && len(res.Output) == 0:
					res.Cmd = strings.TrimPrefix(body, "$ ")
				case body == "|" || strings.HasPrefix(body, "| "):
					res.Output = append(res.Output, strings.TrimPrefix(strings.TrimPrefix(body, "|"), " "))
				case strings.HasPrefix(body, "saved: "):
					res.Saved = strings.TrimPrefix(body, "saved: ")
				case strings.HasPrefix(body, "key: "):
					res.Key = strings.TrimPrefix(body, "key: ")
				case strings.HasPrefix(body, "target: "):
					res.Target = strings.TrimPrefix(body, "target: ")
				case strings.HasPrefix(body, "-- "+Msg+"  "):
					res.Messages = append(res.Messages, strings.TrimPrefix(body, "-- "+Msg+"  "))
					res.Output = append(res.Output, body)
				case strings.HasPrefix(body, "-- "):
					res.Output = append(res.Output, body)
				}
			}
		case strings.HasPrefix(line, ctxIndent):
			// round context or END detail: not a result
		default:
			kind, rest, ok := resultV2(line)
			if !ok {
				continue
			}
			res := Result{Kind: kind, Stage: cur.Stage, Handler: cur.handler}
			if head, dur, clock, ok := timed(rest); ok {
				res.Step, rest, res.Clock = true, head, clock
				res.Dur, _ = strconv.ParseFloat(strings.TrimSuffix(dur, "s"), 64)
			}
			res.Label, res.Detail = splitLabel(rest)
			if res.Kind == Checked {
				cur.Checked = true
			}
			if strings.Contains(res.Detail, "interrupted") && res.Handler == "" {
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

// EnsureV2 makes path ready for syntax 2 lines: a missing or empty log gets
// the header; a syntax 1 log, or one in a newer syntax, is an error.
func EnsureV2(path string, lane int) error {
	switch v := syntax.OfLogFile(path); {
	case v == 0:
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(syntax.LogHeader(lane)+"\n"), 0o644)
	case v == 1:
		return fmt.Errorf("%s is swim syntax 1, from before swim versioned its files, which this swim doesn't read: archive it (swim archive %d) or delete it", path, lane)
	default:
		return syntax.Check(path, v)
	}
}
