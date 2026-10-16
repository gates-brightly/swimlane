// Package lane knows a lane's files: its script (lane.N.sh, or lane.N.py,
// lane.N.ts, a binary lane.N, ...) and sidecar lane.N.yml, .swim/logs/agentN.log
// (log), .swim/laneN.pid (running marker) and .swim/snapshots/.
package lane

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gates-brightly/swimlane/internal/cache"
	"github.com/gates-brightly/swimlane/internal/fsutil"
	"github.com/gates-brightly/swimlane/internal/outputs"
	"github.com/gates-brightly/swimlane/internal/proc"
	"github.com/gates-brightly/swimlane/internal/syntax"
)

// StubMarker identifies a "nothing pending" lane script.
const StubMarker = "# swim:stub"

func Log(root string, n int) string {
	if CheckMode() {
		return CheckLog(root, n)
	}
	return filepath.Join(LogDir(root), fmt.Sprintf("agent%d%s.log", n, cellPart(n)))
}

// CheckLog is lane n's check log (swim plan --check), kept apart from its
// round log.
func CheckLog(root string, n int) string {
	return filepath.Join(LogDir(root), fmt.Sprintf("agent%d%s.check.log", n, cellPart(n)))
}

// CellLog is the log of lane n's cell (a matrix round); in a read-only
// check, its check log.
func CellLog(root string, n int, cell string) string {
	check := ""
	if CheckMode() {
		check = ".check"
	}
	return filepath.Join(LogDir(root), fmt.Sprintf("agent%d.%s%s.log", n, CellSlug(cell), check))
}

// cellPart is ".<cell>" for lane n's files in one of its cells' processes
// (file names), else "".
func cellPart(n int) string {
	if c := CellOf(n); c != "" {
		return "." + CellSlug(c)
	}
	return ""
}

// CheckEnv is set (to 1) in swim plan --check and in every lane process it
// starts: rounds stop where change would begin, guards never approve, logs
// go to the check log and lane status is left alone.
const CheckEnv = "SWIM_CHECK_ONLY"

// CheckMode reports whether this process is part of a read-only check.
func CheckMode() bool { return os.Getenv(CheckEnv) == "1" }

// LogDir holds lane logs and their archives.
func LogDir(root string) string { return filepath.Join(root, ".swim", "logs") }
func PIDFile(root string, n int) string {
	return filepath.Join(root, ".swim", fmt.Sprintf("lane%d%s.pid", n, cellPart(n)))
}
func SnapshotDir(root string) string { return filepath.Join(root, ".swim", "snapshots") }
func RCFile(root string, n int) string {
	return filepath.Join(root, fmt.Sprintf(".lane.%d.rc", n))
}

// Guard is a guard flag a round honours.
type Guard struct {
	Flag string
	Desc string // from the header's Guards: line; "" if only found in the body
}

// KV is a header key the lane script sets that swim doesn't know.
type KV struct{ K, V string }

// Info describes the lane script currently on disk, with defaults filled in
// for header metadata it doesn't set.
type Info struct {
	Exists  bool
	Stub    bool
	Syntax  int      // the script's syntax (1 if it declares none)
	Round   string   // Round: (required for the lane to run)
	Job     string   // Job: id ("" if the script has none; one is made at start)
	After   []string // After: lanes or job ids this round waits for (default none)
	Owner   string   // Owner: who wrote the round (default "")
	Created string   // Created: date (default: the file's modification date)
	Guards  []Guard  // Guards: lines, plus guard flags used in the body
	Timeout time.Duration
	// TimeoutText is the Timeout: value as written ("" for none).
	TimeoutText string
	// StepTimeout is Step-Timeout:, the default limit for each step.
	StepTimeout     time.Duration
	StepTimeoutText string
	Locks           []string // Locks: resources this round needs exclusively (default none)
	Extra           []KV     // other keys in the header block, kept and shown
	Problems        []string // header values swim couldn't use (e.g. a bad Timeout)
	Message         string   // stub message
	// FailHard is Fail: hard: the first failed step stops the round (and
	// goes to on_failure). Default soft. Script syntax 3.
	FailHard bool
	FailText string // the Fail: value as written ("" if unset)
	// HandlerTimeout is Handler-Timeout:, the budget for all handlers
	// together (default DefaultHandlerTimeout).
	HandlerTimeout     time.Duration
	HandlerTimeoutText string
	// Handlers are the round's outcome handlers: bash functions on_success,
	// on_failure and on_exit, or for a lane in another language the
	// On-Success:, On-Failure: and On-Exit: commands. Script syntax 3.
	Handlers []Handler
	// Staged: the script calls stage (bash, or swim stage from another
	// language), so a read-only check has a place to stop.
	Staged bool
	// Matrix is the round's matrix (# Matrix:), empty for a plain round;
	// see Cells. MatrixExclude drops combinations; MatrixParallel caps cells
	// at once (0: 1); MatrixFail is continue (default) or stop.
	Matrix         []Axis
	MatrixExclude  [][]KV
	MatrixParallel int
	MatrixFail     string
	// Includes are repo files (globs) that are part of the round, such as
	// local modules a Python or TypeScript lane imports: they're hashed
	// with the script (ScriptBytes), so approvals cover them.
	Includes []string
	// UsesEvidence: the script has evidence steps (evidence "tf plan" ...),
	// so its approvals cover the evidence too (see swim approve).
	UsesEvidence bool
	// DynamicGuards counts guard calls whose flag is named at run time
	// (guard "$FLAG" ...): swim can't know them ahead.
	DynamicGuards int
	// Outputs are the files or directories the round promises in
	// $SWIM_OUT (# Outputs:), for lanes that depend on it (input N name).
	Outputs []string
	// Always (# Always: yes) makes the lane setup work, like a CI
	// before_script: it never counts as already passed, so every run that
	// includes it runs it fresh, and every other lane in that run waits for
	// it to finish. A lane that names it in After: pulls it into the run.
	Always bool
	// Finally (# Finally: 1 2) are lanes (or jobs, cells) this lane starts
	// after once they have ended, in whatever state, where After: needs
	// them to pass: cleanup, a run report. Naming one of them in a run
	// pulls this lane in. FinallyJob marks a job of a lane of jobs ("# swim
	// job 1.9 finally"): it runs after its earlier siblings, even when one
	// failed.
	Finally    []string
	FinallyJob bool
	// RerunChildren (# Rerun-Children: yes, Always lanes only) makes every
	// lane downstream of this one, at any depth, run again in swim all even
	// if it already passed.
	RerunChildren bool
	// Subs are a group lane's sub-jobs (group.go), its cells.
	Subs []SubJob
	// Observe are the round's probes (# Observe:): facts swim can't pin,
	// such as which binary PATH finds or the cloud account, read when a
	// guard is approved and again just before it returns (observe.go).
	Observe []Probe
	// Cache are the tool caches the round uses (# Cache: terraform
	// @infra/.terraform.lock.hcl): swim points each tool's download cache at
	// a directory that outlives the round, and pins the lockfiles the tool
	// checks the cache against (internal/cache).
	Cache []cache.Decl
	// Modules are the modules the round uses (# Modules: notify clone@1
	// ./modules/x), as written: the only ones its mod and mod_env may call
	// (internal/module resolves them).
	Modules []string
	// Target is what the lane's steps talk to, by default (# Targets:
	// orders-api), for swim observe; a step's --target overrides it.
	Target string
}

// DefaultHandlerTimeout is the handlers' budget when Handler-Timeout: is unset.
const DefaultHandlerTimeout = 5 * time.Minute

// Handler names, in the order they run: on_success or on_failure, then
// on_exit.
const (
	OnSuccess = "on_success"
	OnFailure = "on_failure"
	OnExit    = "on_exit"
)

// HandlerNames are the outcome handlers swim calls.
var HandlerNames = []string{OnSuccess, OnFailure, OnExit}

// Handler is one outcome handler a round defines.
type Handler struct {
	Name   string   // on_success, on_failure or on_exit
	Cmd    string   // the command (a lane in another language); "" for a bash function
	Line   int      // where it is defined (1-based; 0 in a sidecar)
	Guards []string // guard flags used inside it (bash functions)
}

// Handler returns the round's handler called name, if it defines one.
func (i Info) Handler(name string) (Handler, bool) {
	for _, h := range i.Handlers {
		if h.Name == name {
			return h, true
		}
	}
	return Handler{}, false
}

// Pending reports whether the lane script holds a round to run.
func (i Info) Pending() bool { return i.Exists && !i.Stub && i.Round != "" }

// GuardFlags lists the flags of i.Guards.
func (i Info) GuardFlags() []string {
	out := make([]string, len(i.Guards))
	for k, g := range i.Guards {
		out[k] = g.Flag
	}
	return out
}

// knownKeys are the header keys swim reads (lower case); any other key in
// the header block is kept as an extra and shown.
var knownKeys = map[string]bool{"round": true, "job": true, "after": true, "owner": true, "created": true, "guards": true, "timeout": true, "step-timeout": true, "locks": true, "swim": true,
	"fail": true, "handler-timeout": true, "on-success": true, "on-failure": true, "on-exit": true, "outputs": true,
	"matrix": true, "matrix-exclude": true, "matrix-parallel": true, "matrix-fail": true, "includes": true,
	"always": true, "rerun-children": true, "observe": true, "finally": true, "cache": true, "modules": true, "targets": true}

// stageNames are the stages, in order.
var stageNames = map[string]bool{"snapshot": true, "check": true, "change": true, "verify": true}

// ReadScript inspects lane n's script and its sidecar lane.N.yml. A lane
// script without a Round (in its header or sidecar) is treated as not
// pending, so a half-written file is never launched by `swim run`. A script
// in a newer syntax than this swim reads, a lane with two scripts, or a
// sidecar swim can't read is an error.
func ReadScript(root string, n int) (Info, error) {
	if err := ScriptConflict(root, n); err != nil {
		return Info{}, err
	}
	// A lane split into jobs (group.go): its header, its jobs as cells.
	if g, ok := LaneJobs(root, n); ok {
		info := g.Info
		if err := syntax.CheckScript(g.File, info.Syntax); err != nil {
			return Info{}, err
		}
		info.Syntax = max(info.Syntax, syntax.Current)
		info.Subs = append([]SubJob(nil), g.Info.Subs...)
		if cell := CellOf(n); cell != "" {
			c, ok := info.Cell(cell)
			if !ok {
				return Info{}, fmt.Errorf("swim %d has no job %d.%s", n, n, cell)
			}
			info = info.ForCell(c)
		}
		return info, nil
	}
	path := Script(root, n)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Info{}, nil
	}
	if err != nil {
		return Info{}, err
	}
	var info Info
	if IsBinary(data) {
		info = Info{Exists: true, Syntax: syntax.Current}
	} else {
		info = ParseScript(string(data))
		if !strings.HasSuffix(path, ".sh") && info.Syntax == 1 {
			info.Syntax = syntax.Current // syntax 1 was bash only
		}
	}
	if err := syntax.CheckScript(filepath.Base(path), info.Syntax); err != nil {
		return Info{}, err
	}
	if err := applySidecar(Sidecar(root, n), &info); err != nil {
		return Info{}, err
	}
	info.resolveMatrix(root)
	// In a cell's process, the round is that cell's.
	if cell := CellOf(n); cell != "" && info.IsMatrix() {
		c, ok := info.Cell(cell)
		if !ok {
			return Info{}, fmt.Errorf("swim %d has no cell %q", n, cell)
		}
		info = info.ForCell(c)
	}
	if info.Stub {
		info.Round, info.Job, info.After, info.Guards, info.Locks, info.Handlers, info.Outputs, info.Matrix = "", "", nil, nil, nil, nil, nil, nil
		info.Always, info.RerunChildren, info.Finally = false, false, nil
	}
	if info.Created == "" {
		if st, err := os.Stat(path); err == nil {
			info.Created = st.ModTime().Format("2006-01-02")
		}
	}
	return info, nil
}

// ParseScript reads a lane script's header and body. Round, Job and After
// may appear anywhere in the leading comments (as in syntax 1); unknown
// keys are collected from the metadata block at the top (the `# Key: value`
// lines straight after the shebang and syntax line). Header comments are
// "#" or "//" (lane.N.js, lane.N.ts, lane.N.go, ...).
//
// The header is the lane's contract: its guards (# Guards:, "*" for flags
// named at run time), the files it reads (# Includes:) and its modules
// (# Modules:) are what the run allows. The body is read only for where
// its stages and outcome handlers are, and whether it shows evidence (see
// text.go).
func ParseScript(src string) Info {
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		if t := strings.TrimLeft(l, " \t"); strings.HasPrefix(t, "//") {
			lines[i] = "#" + strings.TrimPrefix(t, "//")
		}
	}
	info := Info{Exists: true, Syntax: syntax.OfScript(strings.Join(lines, "\n"))}
	inBlock := true
	inHandler := -1 // index in info.Handlers of the function being read
	listed := map[string]bool{}
	// In a bash lane, heredoc bodies and the lines of a string over several
	// lines hold no code: they aren't read for stages, evidence or handlers.
	var text []bool
	if bashScript(src) {
		text = textLines(src)
	}
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if i < len(text) && text[i] {
			continue
		}
		if i < 200 {
			if i == 0 && strings.HasPrefix(line, "#!") || syntax.IsScriptLine(line) {
				continue
			}
			if line == StubMarker {
				info.Stub = true
				continue
			}
			key, v, isKey := headerKey(line, false)
			roundKey, roundVal, _ := headerKey(line, true)
			isRound := roundKey == "Round"
			if isKey && info.setHeader(key, v, i+1, listed) {
				continue
			}
			if inBlock {
				if isKey && !knownKeys[strings.ToLower(key)] && !strings.HasPrefix(line, "#!") {
					info.Extra = append(info.Extra, KV{key, v})
					continue
				}
				if !isRound {
					inBlock = false
				}
			}
			if info.Stub && strings.HasPrefix(line, "# ") && info.Message == "" && !isRound && i > 0 {
				info.Message = strings.TrimPrefix(line, "# ")
			}
			if isRound && info.Round == "" {
				info.Round = roundVal
			}
		}
		// Outcome handler functions, and the guards inside them.
		if name, ok := handlerDef(raw); ok {
			if _, dup := info.Handler(name); !dup {
				info.Handlers = append(info.Handlers, Handler{Name: name, Line: i + 1})
				inHandler = len(info.Handlers) - 1
			}
		} else if inHandler >= 0 && strings.TrimRight(raw, " \t") == "}" {
			inHandler = -1
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		cmds := Commands(line)
		if !info.Staged && stagesIn(line, cmds) {
			info.Staged = true
		}
		if !info.UsesEvidence && evidenceIn(line, cmds) {
			info.UsesEvidence = true
		}
		if inHandler >= 0 {
			for _, c := range cmds {
				if len(c) > 1 && c[0].Text == "guard" && !c[1].Expands && isIdent(c[1].Text) {
					info.Handlers[inHandler].Guards = append(info.Handlers[inHandler].Guards, c[1].Text)
				}
			}
		}
	}
	info.checkAlways("Rerun-Children:")
	if info.Stub {
		info.Round, info.Job, info.After, info.Guards, info.Locks, info.Handlers, info.Outputs, info.Matrix = "", "", nil, nil, nil, nil, nil, nil
		info.Always, info.RerunChildren, info.Finally = false, false, nil
	}
	return info
}

// setHeader applies a header key the script sets (line is 1-based), and
// reports whether swim reads the key there. Round is read apart (it may
// come without its #).
func (info *Info) setHeader(key, v string, line int, listed map[string]bool) bool {
	switch key {
	case "After":
		for _, tok := range splitList(v) {
			switch strings.ToLower(tok) {
			case "swim", "-", "none", "(none)":
				continue
			}
			info.After = append(info.After, tok)
		}
	case "Job":
		f := strings.Fields(v)
		if len(f) == 0 {
			return false
		}
		if info.Job == "" {
			info.Job = f[0]
		}
	case "Owner":
		info.Owner = v
	case "Targets":
		// The lane's default target for its steps (swim observe groups
		// retries and failures by what a step talks to).
		if f := splitList(v); len(f) > 0 && info.Target == "" {
			info.Target = f[0]
		}
	case "Created":
		info.Created = v
	case "Timeout":
		info.TimeoutText = v
		if v != "" && v != "-" && !strings.EqualFold(v, "none") {
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				info.Problems = append(info.Problems, fmt.Sprintf("Timeout %q is not a duration like 30m or 1h30m; no timeout applies", v))
			} else {
				info.Timeout = d
			}
		}
	case "Step-Timeout":
		if v != "" && v != "-" && !strings.EqualFold(v, "none") {
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				info.Problems = append(info.Problems, fmt.Sprintf("Step-Timeout %q is not a duration like 2m or 30s; no default step limit applies", v))
			} else {
				info.StepTimeout, info.StepTimeoutText = d, v
			}
		}
	case "Locks":
		for _, tok := range splitList(v) {
			switch {
			case tok == "-" || strings.EqualFold(tok, "none"):
			case !ValidLockName(strings.NewReplacer("${", "", "}", "", "$", "").Replace(tok)):
				info.Problems = append(info.Problems, fmt.Sprintf("Locks: %q is not a lock name (letters, digits, . _ - /); ignored", tok))
			default:
				info.Locks = append(info.Locks, tok)
			}
		}
	case "Outputs":
		info.setOutputs(splitList(v), "Outputs:")
	case "Observe":
		info.addProbe(v, "Observe:")
		if k := len(info.Observe); k > 0 && info.Observe[k-1].Line == 0 {
			info.Observe[k-1].Line = line
		}
	case "Includes":
		for _, g := range splitList(v) {
			if g != "-" && !strings.EqualFold(g, "none") {
				info.Includes = append(info.Includes, g)
			}
		}
	case "Matrix", "Matrix-Exclude", "Matrix-Parallel", "Matrix-Fail":
		if v != "" && v != "-" {
			info.setMatrixLine(key, v)
		}
	case "Fail":
		info.setFail(v, "Fail:")
	case "Always":
		info.Always = info.setBool(v, "Always:")
	case "Finally":
		for _, tok := range splitList(v) {
			switch strings.ToLower(tok) {
			case "swim", "-", "none", "(none)":
				continue
			}
			info.Finally = append(info.Finally, tok)
		}
	case "Cache":
		info.setCache(v, "Cache:")
	case "Modules":
		info.setModules(splitList(v))
	case "Rerun-Children":
		info.RerunChildren = info.setBool(v, "Rerun-Children:")
	case "Handler-Timeout":
		info.setHandlerTimeout(v, "Handler-Timeout")
	case "On-Success", "On-Failure", "On-Exit":
		if v != "" && v != "-" && !strings.EqualFold(v, "none") {
			info.setHandlerCmd(strings.ReplaceAll(strings.ToLower(key), "-", "_"), v, line)
		}
	case "Guards":
		info.addGuard(v, listed)
	default:
		return false
	}
	return true
}

// addGuard reads a # Guards: line: "FLAG  what it approves", or "*  why"
// for a lane that names flags at run time (guard "$FLAG"), which may then
// ask for any.
func (info *Info) addGuard(v string, listed map[string]bool) {
	f := strings.Fields(v)
	if len(f) == 0 || f[0] == "-" || strings.EqualFold(f[0], "none") {
		return
	}
	if f[0] == "*" {
		if info.DynamicGuards == 0 {
			info.DynamicGuards = 1
		}
		return
	}
	if !listed[f[0]] {
		listed[f[0]] = true
		info.Guards = append(info.Guards, Guard{Flag: f[0], Desc: strings.TrimSpace(strings.TrimPrefix(v, f[0]))})
	}
}

// handlerDef reports whether a line starts an outcome handler function:
// on_failure() {, function on_exit () {.
func handlerDef(raw string) (string, bool) {
	t := strings.TrimSpace(raw)
	t = strings.TrimSpace(strings.TrimPrefix(t, "function "))
	for _, name := range HandlerNames {
		rest, ok := strings.CutPrefix(t, name)
		if !ok {
			continue
		}
		rest = strings.TrimLeft(rest, " \t")
		if after, ok := strings.CutPrefix(rest, "("); ok && strings.HasPrefix(strings.TrimLeft(after, " \t"), ")") {
			return name, true
		}
	}
	return "", false
}

// stagesIn reports whether a line starts a stage: `stage check`, `swim
// stage check`, or ["swim", "stage", "check"] from another language.
func stagesIn(line string, cmds [][]Word) bool {
	for _, c := range cmds {
		if len(c) > 1 && c[0].Text == "stage" && stageNames[c[1].Text] {
			return true
		}
		if rest, ok := swimCall(c); ok && len(rest) > 1 && rest[0].Text == "stage" && stageNames[rest[1].Text] {
			return true
		}
	}
	for _, s := range quotedAfter(line, "stage") {
		if stageNames[s] {
			return true
		}
	}
	return false
}

// modValueOpts are mod's options that take a value.
var modValueOpts = map[string]bool{"--label": true, "--timeout": true, "--retry": true, "--backoff": true, "--retry-on": true, "--key": true, "--target": true}

// evidenceIn reports whether a line has an evidence step: `evidence
// "<label>" ...`, `swim evidence`, `mod --evidence ...`, ["evidence", ...]
// from another language, or an SDK's module call with evidence on.
func evidenceIn(line string, cmds [][]Word) bool {
	for _, c := range cmds {
		if len(c) > 1 && c[0].Text == "evidence" {
			return true
		}
		if rest, ok := swimCall(c); ok && len(rest) > 0 && rest[0].Text == "evidence" {
			return true
		}
		if len(c) > 1 && c[0].Text == "mod" {
			for k := 1; k < len(c) && strings.HasPrefix(c[k].Text, "--"); k++ {
				if c[k].Text == "--evidence" {
					return true
				}
				if modValueOpts[c[k].Text] {
					k++
				}
			}
		}
	}
	if quotedThen(line, "evidence", ',') {
		return true
	}
	return sdkModuleEvidence(line)
}

// sdkModuleEvidence reports whether a line calls an SDK's module (or
// moduleWith, ModuleWith) with evidence on: evidence=True, evidence: true,
// Evidence: true.
func sdkModuleEvidence(line string) bool {
	i := strings.Index(line, ".module")
	if j := strings.Index(line, ".Module"); i < 0 || j >= 0 && j < i {
		i = j
	}
	if i < 0 {
		return false
	}
	rest := line[i:]
	for {
		k := strings.Index(rest, "vidence")
		if k < 1 {
			return false
		}
		if e := rest[k-1]; e == 'e' || e == 'E' {
			after := strings.TrimLeft(rest[k+len("vidence"):], " \t")
			if after != "" && (after[0] == '=' || after[0] == ':') {
				val := strings.TrimLeft(after[1:], " \t")
				if strings.HasPrefix(val, "True") || strings.HasPrefix(val, "true") {
					return true
				}
			}
		}
		rest = rest[k+len("vidence"):]
	}
}

// setOutputs reads Outputs: names: relative paths inside $SWIM_OUT.
func (i *Info) setOutputs(names []string, where string) {
	i.Outputs = nil
	for _, name := range names {
		switch {
		case name == "" || name == "-" || strings.EqualFold(name, "none"):
		case !outputs.ValidName(name):
			i.Problems = append(i.Problems, fmt.Sprintf("%s %q is not a path inside $SWIM_OUT (relative, no ..); ignored", where, name))
		default:
			i.Outputs = append(i.Outputs, name)
		}
	}
}

// setFail reads a Fail: value (soft or hard).
func (i *Info) setFail(v, where string) {
	i.FailText = v
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "-", "soft":
		i.FailHard = false
	case "hard":
		i.FailHard = true
	default:
		i.Problems = append(i.Problems, fmt.Sprintf("%s %q is not soft or hard; soft applies", where, v))
		i.FailHard = false
	}
}

// setBool reads a yes/no header value ("" is no).
func (i *Info) setBool(v, where string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "-", "no", "false", "off", "none":
		return false
	case "yes", "true", "on":
		return true
	}
	i.Problems = append(i.Problems, fmt.Sprintf("%s %q is not yes or no; no applies", where, v))
	return false
}

// checkAlways drops Rerun-Children from a lane that isn't Always: only
// setup work decides that what runs after it is stale.
func (i *Info) checkAlways(where string) {
	if i.Always && len(i.Finally) > 0 {
		i.Problems = append(i.Problems, "`Finally:` on an Always lane: every other lane waits for an Always lane, so it can't wait for them to end; ignored")
		i.Finally = nil
	}
	if i.RerunChildren && !i.Always {
		i.Problems = append(i.Problems, fmt.Sprintf("%s yes needs `Always: yes` (only an Always lane reruns its children); ignored", where))
		i.RerunChildren = false
	}
}

// setHandlerTimeout reads a Handler-Timeout: value.
func (i *Info) setHandlerTimeout(v, where string) {
	i.HandlerTimeout, i.HandlerTimeoutText = 0, ""
	if v == "" || v == "-" || strings.EqualFold(v, "none") {
		return
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		i.Problems = append(i.Problems, fmt.Sprintf("%s %q is not a duration like 5m; the default %s applies", where, v, DefaultHandlerTimeout))
		return
	}
	i.HandlerTimeout, i.HandlerTimeoutText = d, v
}

// setHandlerCmd sets (or replaces) handler name's command.
func (i *Info) setHandlerCmd(name, cmd string, line int) {
	for k, h := range i.Handlers {
		if h.Name == name {
			i.Handlers[k].Cmd, i.Handlers[k].Line = cmd, line
			return
		}
	}
	i.Handlers = append(i.Handlers, Handler{Name: name, Cmd: cmd, Line: line})
}

// HandlerBudget is the handlers' time budget: Handler-Timeout:, or the
// default.
func (i Info) HandlerBudget() time.Duration {
	if i.HandlerTimeout > 0 {
		return i.HandlerTimeout
	}
	return DefaultHandlerTimeout
}

// The syntax 1 template's lines MigrateScript reads.
var v1StageNames = map[string]string{"1": "snapshot", "2": "check", "3": "change", "4": "verify"}

// v1Written is the old template's "# Lane: ... Written: DATE" date.
func v1Written(l string) (string, bool) {
	rest, ok := strings.CutPrefix(l, "#")
	if !ok || !strings.HasPrefix(strings.TrimSpace(rest), "Lane:") {
		return "", false
	}
	_, after, ok := strings.Cut(rest, "Written:")
	if f := strings.Fields(after); ok && len(f) > 0 {
		return f[0], true
	}
	return "", false
}

// v1HeadKey reports whether a trimmed line is a Round, Job or After line.
func v1HeadKey(t string) bool {
	rest, ok := strings.CutPrefix(t, "#")
	rest = strings.TrimSpace(rest)
	return ok && (strings.HasPrefix(rest, "Round:") || strings.HasPrefix(rest, "Job:") || strings.HasPrefix(rest, "After:"))
}

// v1Stage reads the old template's numbered section line, "# 2. Checks".
func v1Stage(l string) (string, bool) {
	rest, ok := strings.CutPrefix(l, "# ")
	if !ok || len(rest) < 4 || rest[1:3] != ". " {
		return "", false
	}
	name, ok := v1StageNames[rest[:1]]
	if !ok {
		return "", false
	}
	w := rest[3:]
	for _, t := range []string{"Snapshot", "Checks", "Check", "Change", "Verify"} {
		if after, ok := strings.CutPrefix(w, t); ok && (after == "" || !(isLetter(rune(after[0])) || isDigit(rune(after[0])) || after[0] == '_')) {
			return name, true
		}
	}
	return "", false
}

// firstWordIs reports whether a line's first word is cmd, with something
// after it.
func firstWordIs(l, cmd string) bool {
	f := strings.Fields(l)
	return len(f) > 1 && f[0] == cmd
}

// MigrateScript rewrites a syntax 1 lane script as syntax 2: it adds the
// syntax line, a Created: date from the old template's "Written:" field, and
// for scripts made from the old template, `stage` lines at its numbered
// "# 1. Snapshot / 2. Checks / 3. Change / 4. Verify" sections. Everything
// else is kept byte for byte. ok is false if src isn't syntax 1.
func MigrateScript(src string) (string, bool) {
	if syntax.OfScript(src) != 1 {
		return src, false
	}
	lines := strings.Split(src, "\n")
	var out []string
	i := 0
	if len(lines) > 0 && strings.HasPrefix(lines[0], "#!") {
		out = append(out, lines[0])
		i = 1
	}
	out = append(out, syntax.ScriptLine())

	created, hasCreated, hasStages := "", false, false
	for _, l := range lines {
		if d, ok := v1Written(l); ok {
			created = d
		}
		if strings.HasPrefix(strings.TrimSpace(l), "# Created:") {
			hasCreated = true
		}
		if firstWordIs(l, "stage") {
			hasStages = true
		}
	}
	// Created: goes after the leading Round/Job/After lines.
	insertAt := -1
	for k := i; k < len(lines) && k < i+8; k++ {
		if v1HeadKey(strings.TrimSpace(lines[k])) {
			insertAt = k
		} else if insertAt >= 0 {
			break
		}
	}
	afterInit := false
	for k := i; k < len(lines); k++ {
		out = append(out, lines[k])
		if k == insertAt && created != "" && !hasCreated {
			out = append(out, "# Created: "+created)
		}
		if firstWordIs(lines[k], "lane_init") {
			afterInit = true
		}
		if afterInit && !hasStages {
			if name, ok := v1Stage(lines[k]); ok {
				out = append(out, "stage "+name)
			}
		}
	}
	return strings.Join(out, "\n"), true
}

// Running returns the pid recorded for lane n if that process is alive.
// Outside one of its cells' processes, a lane any of whose cells runs is
// running.
func Running(root string, n int) (int, bool) {
	pid, _, ok := RunningOn(root, n)
	return pid, ok
}

// RunningOn is Running, with the machine the process runs on. A lane
// another machine claimed counts as running: this one can't see its
// process (two machines sharing a repo), so it never takes it for dead.
func RunningOn(root string, n int) (pid int, host string, ok bool) {
	if pid, host, ok := runningIn(PIDFile(root, n)); ok || CellOf(n) != "" {
		return pid, host, ok
	}
	cells, _ := filepath.Glob(filepath.Join(root, ".swim", fmt.Sprintf("lane%d.*.pid", n)))
	for _, f := range cells {
		if pid, host, ok := runningIn(f); ok {
			return pid, host, true
		}
	}
	return 0, "", false
}

// CellRunning returns the pid recorded for lane n's matrix cell or job
// cell if that process is alive.
func CellRunning(root string, n int, cell string) (int, bool) {
	pid, _, ok := runningIn(filepath.Join(root, ".swim", fmt.Sprintf("lane%d.%s.pid", n, CellSlug(cell))))
	return pid, ok
}

func runningIn(path string) (int, string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, "", false
	}
	pid, start, ok := proc.Parse(string(data))
	if !ok {
		return 0, "", false
	}
	// The start time and host are in the claim file next to it, if this
	// pid wrote it: a pid file from an older swim falls back to the pid
	// alone, on this machine.
	host := ""
	if data, err := os.ReadFile(strings.TrimSuffix(path, ".pid") + ".claim"); err == nil && start == "" {
		if cp, cs, ch, ok := parseClaim(string(data)); ok && cp == pid {
			start, host = cs, ch
		}
	}
	if !proc.Local(host) {
		return pid, host, true
	}
	return pid, host, proc.Same(pid, start)
}

// claimRecord is a claim file's content: "pid start host" (proc.Record,
// then the machine the process runs on).
func claimRecord(pid int) string {
	rec := proc.Record(pid)
	if h := proc.Host(); h != "" && !strings.ContainsAny(h, " \t\n") {
		rec += " " + h
	}
	return rec
}

// parseClaim reads a claimRecord, or a bare proc.Record from an older swim
// (host "": this machine).
func parseClaim(s string) (pid int, start, host string, ok bool) {
	f := strings.Fields(s)
	if len(f) == 3 {
		host, f = f[2], f[:2]
	}
	pid, start, ok = proc.Parse(strings.Join(f, " "))
	return pid, start, host, ok
}

// Alive reports whether a process with pid exists. A pid alone may have
// been handed to another process since; Running checks the start time
// recorded with it too.
func Alive(pid int) bool { return proc.Alive(pid) }

// claimFile is lane n's claim: the lock it's claimed under, holding the
// claimer's pid and start time.
func claimFile(root string, n int) string {
	return filepath.Join(root, ".swim", fmt.Sprintf("lane%d%s.claim", n, cellPart(n)))
}

// WritePID records pid as lane n's running lane script, with its start
// time (in the claim file), so a later process given the same pid isn't
// taken for it. The pid file itself holds the bare pid, as older swims
// read it. It refuses if another live process already holds the lane.
// The check and the write happen under a lock on the claim file, so of
// two processes claiming a lane at once exactly one wins.
func WritePID(root string, n, pid int) error {
	if err := os.MkdirAll(filepath.Dir(PIDFile(root, n)), 0o755); err != nil {
		return err
	}
	claim, err := os.OpenFile(claimFile(root, n), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer claim.Close()
	if err := syscall.Flock(int(claim.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("claim swim %d: %w", n, err)
	}
	defer syscall.Flock(int(claim.Fd()), syscall.LOCK_UN)
	if other, ok := Running(root, n); ok && other != pid {
		return fmt.Errorf("swim %d is already running (pid %d)", n, other)
	}
	if err := claim.Truncate(0); err != nil {
		return err
	}
	if _, err := claim.WriteAt([]byte(claimRecord(pid)+"\n"), 0); err != nil {
		return err
	}
	if err := claim.Sync(); err != nil {
		return err
	}
	return fsutil.WriteAtomic(PIDFile(root, n), []byte(strconv.Itoa(pid)+"\n"), 0o644)
}

// RemovePID clears lane n's running marker.
func RemovePID(root string, n int) { os.Remove(PIDFile(root, n)) }

// ErrRunning is returned when a command would touch a running lane.
type ErrRunning struct{ Lane, PID int }

func (e ErrRunning) Error() string {
	return fmt.Sprintf("swim %d is running (pid %d); never edit or move a running lane's files", e.Lane, e.PID)
}

func refuseRunning(root string, n int) error {
	if pid, ok := Running(root, n); ok {
		return ErrRunning{n, pid}
	}
	return nil
}

// Slug turns free text into a file-name fragment: lower-case letters and
// digits, every other run of characters one "-".
func Slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || isDigit(r) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
	}
	s = b.String()
	if len(s) > 60 {
		s = strings.TrimRight(s[:60], "-")
	}
	return s
}

// Archive renames lane n's logs aside, .swim/logs/agentN.log to
// agentN.prev-<what>.log, and each job's or matrix cell's log
// (agentN.<job>.log) to agentN.prev-<what>.<job>.log, and returns the
// first new path. It never overwrites an existing archive. With its logs
// archived (and a stub or no script) the lane is no longer in use
// (Associated).
func Archive(root string, n int, what string) (string, error) {
	moves, err := archiveMoves(root, n, what)
	if err != nil {
		return "", err
	}
	for _, m := range moves {
		if err := os.Rename(m[0], m[1]); err != nil {
			return "", err
		}
	}
	return moves[0][1], nil
}

// ArchiveCheck reports where Archive would move lane n's log (the first,
// when it has several), or why it can't (the lane is running, has no log,
// or an archive name is taken), without moving anything.
func ArchiveCheck(root string, n int, what string) (string, error) {
	moves, err := archiveMoves(root, n, what)
	if err != nil {
		return "", err
	}
	return moves[0][1], nil
}

// archiveMoves is Archive's renames, [from, to], the lane's own log first.
func archiveMoves(root string, n int, what string) ([][2]string, error) {
	if err := refuseRunning(root, n); err != nil {
		return nil, err
	}
	slug := Slug(what)
	if slug == "" {
		return nil, errors.New("archive needs a description, e.g. `swim archive 1 --as cutover-api`")
	}
	dir := LogDir(root)
	var moves [][2]string
	if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("agent%d.log", n))); err == nil {
		moves = append(moves, [2]string{filepath.Join(dir, fmt.Sprintf("agent%d.log", n)), filepath.Join(dir, fmt.Sprintf("agent%d.prev-%s.log", n, slug))})
	}
	entries, _ := os.ReadDir(dir)
	prefix := fmt.Sprintf("agent%d.", n)
	for _, e := range entries {
		name := e.Name()
		if name == prefix+"log" || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".log") || strings.Contains(name, ".prev-") || strings.HasSuffix(name, ".check.log") {
			continue
		}
		cell := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".log")
		if cell == "" {
			continue
		}
		moves = append(moves, [2]string{filepath.Join(dir, name), filepath.Join(dir, fmt.Sprintf("agent%d.prev-%s.%s.log", n, slug, cell))})
	}
	if len(moves) == 0 {
		return nil, fmt.Errorf("swim %d has nothing to archive (no .swim/logs/agent%d.log, and no job or cell logs)", n, n)
	}
	for _, m := range moves {
		if _, err := os.Stat(m[1]); err == nil {
			return nil, fmt.Errorf("%s already exists; choose a different name (--as)", filepath.Base(m[1]))
		}
	}
	return moves, nil
}

// WriteScript writes content to lane.N.sh (mode 0755) unless the lane is
// running. Unless force is set it also refuses to replace a pending round.
func WriteScript(root string, n int, content string, force bool) error {
	return WriteScriptAs(root, n, "sh", content, force)
}

// WriteScriptAs writes content to lane.N.<ext> (mode 0755) unless the lane
// is running, replacing the lane's script in any other language and its
// sidecar (a stub or a new round starts clean). Unless force is set it
// refuses to replace a pending round.
func WriteScriptAs(root string, n int, ext, content string, force bool) error {
	if err := refuseRunning(root, n); err != nil {
		return err
	}
	if g, ok := GroupOf(root, n); ok {
		return fmt.Errorf("swim %d is the group file %s; edit it there, or remove the file to free the lane", n, g.File)
	}
	if !force {
		info, err := ReadScript(root, n)
		if err != nil {
			return err
		}
		if info.Pending() {
			return fmt.Errorf("swim %d already holds a pending round (%q); run it and archive/stub it first, or pass --force", n, info.Round)
		}
	}
	dst := filepath.Join(root, fmt.Sprintf("lane.%d.%s", n, ext))
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o755); err != nil {
		return err
	}
	// Rename gives running readers (there should be none) the old inode
	// rather than a half-written file.
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	for _, p := range append(Scripts(root, n), Sidecar(root, n)) {
		if p != dst {
			os.Remove(p)
		}
	}
	return nil
}

// NewJobID returns a random (version 4) UUID.
func NewJobID() string {
	for {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			panic(err)
		}
		b[6] = b[6]&0x0f | 0x40
		b[8] = b[8]&0x3f | 0x80
		id := fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
		// Its 8-character prefix (how people type it) must not read as a
		// lane number: about 1 in 50 random ids start with 8 digits.
		if strings.Trim(id[:8], "0123456789") != "" {
			return id
		}
	}
}

// NewRunID returns a run id: r-<UTC yyyymmddThhmmssZ>-<4 hex>. Sortable,
// and unique enough on one machine.
func NewRunID() string {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return fmt.Sprintf("r-%s-%x", time.Now().UTC().Format("20060102T150405Z"), b)
}

// ValidRunID reports whether id can be used as a run id (e.g. passed with
// --run-id): the same characters as a job id.
func ValidRunID(id string) bool { return ValidJobID(id) }

// ValidJobID reports whether id can be used as a job id: 8-64 characters of
// letters, digits, '.', '_' or '-', not all digits (so it can't be mistaken
// for a lane number).
func ValidJobID(id string) bool {
	if !validJobID(id) {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return true
		}
	}
	return false
}

// ShortJob is the first 8 characters of a job id, for display.
func ShortJob(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// MatchJob reports whether ref pins job id: an exact match, or a prefix of
// at least 8 characters.
func MatchJob(id, ref string) bool {
	if id == "" || ref == "" {
		return false
	}
	if strings.EqualFold(id, ref) {
		return true
	}
	return len(ref) >= 8 && strings.HasPrefix(strings.ToLower(id), strings.ToLower(ref))
}

// oldLog reads an old root-level lane log's name: agentN.log, or an
// archive agentN.prev-<name>.log.
func oldLog(name string) (int, bool) {
	mid, ok := strings.CutPrefix(name, "agent")
	if !ok {
		return 0, false
	}
	if mid, ok = strings.CutSuffix(mid, ".log"); !ok {
		return 0, false
	}
	num, archive, dotted := strings.Cut(mid, ".")
	if !isDigits(num) || dotted && (!strings.HasPrefix(archive, "prev-") || archive == "prev-") {
		return 0, false
	}
	n, err := strconv.Atoi(num)
	return n, err == nil
}

// validJobID reports whether id is a job id: 8 to 64 characters, letters,
// digits, . _ and -, starting with a letter or digit.
func validJobID(id string) bool {
	if len(id) < 8 || len(id) > 64 || !(isLetter(rune(id[0])) || isDigit(rune(id[0]))) {
		return false
	}
	return allOf(id, func(r rune) bool { return isLetter(r) || isDigit(r) || r == '.' || r == '_' || r == '-' })
}

// MigrateLogs moves lane logs written by older versions of swim to the repo
// root (agentN.log, agentN.prev-*.log) into .swim/logs/. Logs of a running
// lane, and files whose destination already exists, are left in place. It
// returns the names it moved.
func MigrateLogs(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var moved []string
	for _, e := range entries {
		n, ok := oldLog(e.Name())
		if !ok || e.IsDir() {
			continue
		}
		if _, running := Running(root, n); running {
			continue
		}
		dst := filepath.Join(LogDir(root), e.Name())
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		if os.MkdirAll(LogDir(root), 0o755) != nil {
			return moved
		}
		if os.Rename(filepath.Join(root, e.Name()), dst) == nil {
			moved = append(moved, e.Name())
		}
	}
	return moved
}

// StopPath is lane n's stop request (.swim/laneN.stop): while it exists,
// holding the lane's run id, swim step starts no new step in that lane. The
// launcher writes it on the first Ctrl-C; swim interrupt N writes it too.
func StopPath(root string, n int) string {
	return filepath.Join(root, ".swim", fmt.Sprintf("lane%d.stop", n))
}

// RequestStop asks lane n (in run) to stop at its next step boundary.
func RequestStop(root string, n int, run string) error {
	os.MkdirAll(filepath.Join(root, ".swim"), 0o755)
	return os.WriteFile(StopPath(root, n), []byte(run+"\n"), 0o644)
}

// StopRequested reports whether lane n's run has been asked to stop. A
// request for another run (left over from an earlier one) doesn't count;
// an empty run matches any request.
func StopRequested(root string, n int, run string) bool {
	data, err := os.ReadFile(StopPath(root, n))
	if err != nil {
		return false
	}
	want := strings.TrimSpace(string(data))
	return run == "" || want == "" || want == run
}

// ClearStop removes lane n's stop request.
func ClearStop(root string, n int) { os.Remove(StopPath(root, n)) }
