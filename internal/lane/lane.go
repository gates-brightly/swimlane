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
	"regexp"
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
	return filepath.Join(LogDir(root), fmt.Sprintf("agent%d%s.log", n, cellPart()))
}

// CheckLog is lane n's check log (swim plan --check), kept apart from its
// round log.
func CheckLog(root string, n int) string {
	return filepath.Join(LogDir(root), fmt.Sprintf("agent%d%s.check.log", n, cellPart()))
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

// cellPart is ".<cell>" in a cell's process (file names), else "".
func cellPart() string {
	if c := CurrentCell(); c != "" {
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
	return filepath.Join(root, ".swim", fmt.Sprintf("lane%d%s.pid", n, cellPart()))
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

var (
	// Header keys are "# Key: value" with at most one space after the #;
	// indented comment lines ("#   After: ...") are prose, not keys.
	roundRE = regexp.MustCompile(`^#?[ \t]?Round:\s*(.*)$`)
	jobRE   = regexp.MustCompile(`^#[ \t]?Job:\s*(\S+)`)
	afterRE = regexp.MustCompile(`^#[ \t]?After:\s*(.*)$`)
	metaRE  = regexp.MustCompile(`^#[ \t]?(Owner|Created|Guards|Timeout|Step-Timeout|Locks|Fail|Handler-Timeout|On-Success|On-Failure|On-Exit|Outputs|Matrix|Matrix-Exclude|Matrix-Parallel|Matrix-Fail|Includes|Always|Rerun-Children|Observe|Finally|Cache):\s*(.*)$`)
	keyRE   = regexp.MustCompile(`^#[ \t]?([A-Za-z][A-Za-z0-9 _-]*?):\s*(.*)$`)
	sepRE   = regexp.MustCompile(`[\s,]+`)
	// stageUseRE / stageArgRE are a stage call: `stage check`, `swim stage
	// check`, or ["swim", "stage", "check"] from another language.
	stageUseRE    = regexp.MustCompile(`(?:^|[\s;&|(])stage\s+["']?(?:snapshot|check|change|verify)\b`)
	evidenceUseRE = regexp.MustCompile(`(?:^|[\s;&|(])evidence\s+(?:--\S+\s+\S*\s*)*["']`)
	evidenceArgRE = regexp.MustCompile(`['"]evidence['"]\s*,`)
	dynGuardRE    = regexp.MustCompile(`(?:^|[\s;&|(!])guard\s+["']?\$`)
	stageArgRE    = regexp.MustCompile(`['"]stage['"]\s*,\s*['"](?:snapshot|check|change|verify)['"]`)
	// handlerFnRE is a handler function's first line: on_failure() {
	handlerFnRE = regexp.MustCompile(`^\s*(?:function\s+)?(on_success|on_failure|on_exit)\s*\(\s*\)`)
	guardUse    = regexp.MustCompile(`(?:^|[\s;&|(!])guard\s+([A-Za-z_][A-Za-z0-9_]*)`)
	// guardArgUse is `swim guard FLAG` from another language, the
	// arguments as a list: ["swim", "guard", "FLAG", ...].
	guardArgUse = regexp.MustCompile(`['"]guard['"]\s*,\s*['"]([A-Za-z_][A-Za-z0-9_]*)['"]`)
	// sdkGuardUse is an SDK's guard call: r.guard("FLAG", ...) (Python,
	// TypeScript) or r.Guard("FLAG", ...) (Go); sdkDynGuard names its flag
	// at run time.
	sdkGuardUse = regexp.MustCompile(`\.[Gg]uard\(\s*["'\x60]([A-Za-z_][A-Za-z0-9_]*)["'\x60]`)
	sdkDynGuard = regexp.MustCompile(`\.[Gg]uard\(\s*[A-Za-z_]`)
	knownKeys   = map[string]bool{"round": true, "job": true, "after": true, "owner": true, "created": true, "guards": true, "timeout": true, "step-timeout": true, "locks": true, "swim": true,
		"fail": true, "handler-timeout": true, "on-success": true, "on-failure": true, "on-exit": true, "outputs": true,
		"matrix": true, "matrix-exclude": true, "matrix-parallel": true, "matrix-fail": true, "includes": true,
		"always": true, "rerun-children": true, "observe": true, "finally": true, "cache": true}
)

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
		if cell := CurrentCell(); cell != "" {
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
	if cell := CurrentCell(); cell != "" && info.IsMatrix() {
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
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if i < 200 {
			if i == 0 && strings.HasPrefix(line, "#!") || syntaxLineRE.MatchString(line) {
				continue
			}
			if line == StubMarker {
				info.Stub = true
				continue
			}
			if m := afterRE.FindStringSubmatch(line); m != nil {
				for _, tok := range sepRE.Split(m[1], -1) {
					switch strings.ToLower(tok) {
					case "", "swim", "-", "none", "(none)":
						continue
					}
					info.After = append(info.After, tok)
				}
				continue
			}
			if m := jobRE.FindStringSubmatch(line); m != nil {
				if info.Job == "" {
					info.Job = m[1]
				}
				continue
			}
			if m := metaRE.FindStringSubmatch(line); m != nil {
				v := strings.TrimSpace(m[2])
				switch m[1] {
				case "Owner":
					info.Owner = v
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
					for _, tok := range sepRE.Split(v, -1) {
						switch {
						case tok == "" || tok == "-" || strings.EqualFold(tok, "none"):
						case !ValidLockName(strings.NewReplacer("${", "", "}", "", "$", "").Replace(tok)):
							info.Problems = append(info.Problems, fmt.Sprintf("Locks: %q is not a lock name (letters, digits, . _ - /); ignored", tok))
						default:
							info.Locks = append(info.Locks, tok)
						}
					}
				case "Outputs":
					info.setOutputs(sepRE.Split(v, -1), "Outputs:")
				case "Observe":
					info.addProbe(v, "Observe:")
					if k := len(info.Observe); k > 0 && info.Observe[k-1].Line == 0 {
						info.Observe[k-1].Line = i + 1
					}
				case "Includes":
					for _, g := range sepRE.Split(v, -1) {
						if g != "" && g != "-" && !strings.EqualFold(g, "none") {
							info.Includes = append(info.Includes, g)
						}
					}
				case "Matrix", "Matrix-Exclude", "Matrix-Parallel", "Matrix-Fail":
					if v != "" && v != "-" {
						info.setMatrixLine(m[1], v)
					}
				case "Fail":
					info.setFail(v, "Fail:")
				case "Always":
					info.Always = info.setBool(v, "Always:")
				case "Finally":
					for _, tok := range sepRE.Split(v, -1) {
						switch strings.ToLower(tok) {
						case "", "swim", "-", "none", "(none)":
							continue
						}
						info.Finally = append(info.Finally, tok)
					}
				case "Cache":
					info.setCache(v, "Cache:")
				case "Rerun-Children":
					info.RerunChildren = info.setBool(v, "Rerun-Children:")
				case "Handler-Timeout":
					info.setHandlerTimeout(v, "Handler-Timeout")
				case "On-Success", "On-Failure", "On-Exit":
					if v != "" && v != "-" && !strings.EqualFold(v, "none") {
						info.setHandlerCmd(strings.ReplaceAll(strings.ToLower(m[1]), "-", "_"), v, i+1)
					}
				case "Guards":
					if f := strings.Fields(v); len(f) > 0 && f[0] != "-" && !strings.EqualFold(f[0], "none") && !listed[f[0]] {
						listed[f[0]] = true
						info.Guards = append(info.Guards, Guard{Flag: f[0], Desc: strings.TrimSpace(strings.TrimPrefix(v, f[0]))})
					}
				}
				continue
			}
			if inBlock {
				if m := keyRE.FindStringSubmatch(line); m != nil && !knownKeys[strings.ToLower(m[1])] && !strings.HasPrefix(line, "#!") {
					info.Extra = append(info.Extra, KV{m[1], strings.TrimSpace(m[2])})
					continue
				}
				if !roundRE.MatchString(line) {
					inBlock = false
				}
			}
			if info.Stub && strings.HasPrefix(line, "# ") && info.Message == "" && !roundRE.MatchString(line) && i > 0 {
				info.Message = strings.TrimPrefix(line, "# ")
			}
			if m := roundRE.FindStringSubmatch(line); m != nil && info.Round == "" {
				info.Round = strings.TrimSpace(m[1])
			}
		}
		// Outcome handler functions, and the guards inside them.
		if m := handlerFnRE.FindStringSubmatch(raw); m != nil {
			if _, dup := info.Handler(m[1]); !dup {
				info.Handlers = append(info.Handlers, Handler{Name: m[1], Line: i + 1})
				inHandler = len(info.Handlers) - 1
			}
		} else if inHandler >= 0 && strings.TrimRight(raw, " \t") == "}" {
			inHandler = -1
		}
		// Guard flags used in the body count even if the header doesn't list them.
		if !strings.HasPrefix(line, "#") {
			if stageUseRE.MatchString(line) || stageArgRE.MatchString(line) {
				info.Staged = true
			}
			if dynGuardRE.MatchString(line) || sdkDynGuard.MatchString(line) {
				info.DynamicGuards++
			}
			if evidenceUseRE.MatchString(line) || evidenceArgRE.MatchString(line) {
				info.UsesEvidence = true
			}
			uses := guardUse.FindAllStringSubmatch(line, -1)
			uses = append(uses, guardArgUse.FindAllStringSubmatch(line, -1)...)
			uses = append(uses, sdkGuardUse.FindAllStringSubmatch(line, -1)...)
			for _, m := range uses {
				if inHandler >= 0 {
					info.Handlers[inHandler].Guards = append(info.Handlers[inHandler].Guards, m[1])
				}
				if !listed[m[1]] {
					listed[m[1]] = true
					info.Guards = append(info.Guards, Guard{Flag: m[1]})
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

var syntaxLineRE = regexp.MustCompile(`^#\s*swim:\s*syntax\s+\d+\s*$`)

var (
	lane1RE      = regexp.MustCompile(`^#\s*Lane:.*Written:\s*(\S+)`)
	headKeyRE    = regexp.MustCompile(`^#\s*(Round|Job|After):`)
	v1StageRE    = regexp.MustCompile(`^# ([1-4])\. (Snapshot|Checks?|Change|Verify)\b`)
	stageCallRE  = regexp.MustCompile(`^\s*stage\s+\w+`)
	laneInitRE   = regexp.MustCompile(`^\s*lane_init\s`)
	v1StageNames = map[string]string{"1": "snapshot", "2": "check", "3": "change", "4": "verify"}
)

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
		if m := lane1RE.FindStringSubmatch(l); m != nil {
			created = m[1]
		}
		if strings.HasPrefix(strings.TrimSpace(l), "# Created:") {
			hasCreated = true
		}
		if stageCallRE.MatchString(l) {
			hasStages = true
		}
	}
	// Created: goes after the leading Round/Job/After lines.
	insertAt := -1
	for k := i; k < len(lines) && k < i+8; k++ {
		if headKeyRE.MatchString(strings.TrimSpace(lines[k])) {
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
		if laneInitRE.MatchString(lines[k]) {
			afterInit = true
		}
		if afterInit && !hasStages {
			if m := v1StageRE.FindStringSubmatch(lines[k]); m != nil {
				out = append(out, "stage "+v1StageNames[m[1]])
			}
		}
	}
	return strings.Join(out, "\n"), true
}

// Running returns the pid recorded for lane n if that process is alive.
// Outside a cell's process, a lane any of whose cells runs is running.
func Running(root string, n int) (int, bool) {
	if pid, ok := runningIn(PIDFile(root, n)); ok || CurrentCell() != "" {
		return pid, ok
	}
	cells, _ := filepath.Glob(filepath.Join(root, ".swim", fmt.Sprintf("lane%d.*.pid", n)))
	for _, f := range cells {
		if pid, ok := runningIn(f); ok {
			return pid, true
		}
	}
	return 0, false
}

// CellRunning returns the pid recorded for lane n's matrix cell or job
// cell if that process is alive.
func CellRunning(root string, n int, cell string) (int, bool) {
	return runningIn(filepath.Join(root, ".swim", fmt.Sprintf("lane%d.%s.pid", n, CellSlug(cell))))
}

func runningIn(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, start, ok := proc.Parse(string(data))
	if !ok {
		return 0, false
	}
	// The start time is in the claim file next to it, if this pid wrote
	// it: a pid file from an older swim falls back to the pid alone.
	if data, err := os.ReadFile(strings.TrimSuffix(path, ".pid") + ".claim"); err == nil && start == "" {
		if cp, cs, ok := proc.Parse(string(data)); ok && cp == pid {
			start = cs
		}
	}
	return pid, proc.Same(pid, start)
}

// Alive reports whether a process with pid exists. A pid alone may have
// been handed to another process since; Running checks the start time
// recorded with it too.
func Alive(pid int) bool { return proc.Alive(pid) }

// claimFile is lane n's claim: the lock it's claimed under, holding the
// claimer's pid and start time.
func claimFile(root string, n int) string {
	return filepath.Join(root, ".swim", fmt.Sprintf("lane%d%s.claim", n, cellPart()))
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
	if _, err := claim.WriteAt([]byte(proc.Record(pid)+"\n"), 0); err != nil {
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

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

// Slug turns free text into a file-name fragment.
func Slug(s string) string {
	s = strings.Trim(slugRE.ReplaceAllString(strings.ToLower(s), "-"), "-")
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

var jobIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{7,63}$`)

// ValidJobID reports whether id can be used as a job id: 8-64 characters of
// letters, digits, '.', '_' or '-', not all digits (so it can't be mistaken
// for a lane number).
func ValidJobID(id string) bool {
	if !jobIDRE.MatchString(id) {
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

var oldLogRE = regexp.MustCompile(`^agent([0-9]+)(\.prev-.+)?\.log$`)

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
		m := oldLogRE.FindStringSubmatch(e.Name())
		if m == nil || e.IsDir() {
			continue
		}
		n, _ := strconv.Atoi(m[1])
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
