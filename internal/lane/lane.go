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
	return filepath.Join(LogDir(root), fmt.Sprintf("agent%d.log", n))
}

// CheckLog is lane n's check log (swim plan --check), kept apart from its
// round log.
func CheckLog(root string, n int) string {
	return filepath.Join(LogDir(root), fmt.Sprintf("agent%d.check.log", n))
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
	return filepath.Join(root, ".swim", fmt.Sprintf("lane%d.pid", n))
}
func SnapshotDir(root string) string { return filepath.Join(root, ".swim", "snapshots") }

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
	Exists bool
	Stub   bool
	Syntax int    // the script's syntax (1 if it declares none)
	Round  string // Round: (required for the lane to run)
	Task   string // Task: the lane's name (swim run NAME); "" for none
	// Workspace is where the lane runs: "" (its staged copy, holding only
	// what its manifest pins) or WorkspaceLive (the repo's working tree,
	// where it can read files nobody approved).
	Workspace string
	Job       string   // Job: id ("" if the script has none; one is made at start)
	After     []string // After: lanes or job ids this round waits for (default none)
	Owner     string   // Owner: who wrote the round (default "")
	Created   string   // Created: date (default: the file's modification date)
	Guards    []Guard  // Guards: lines, plus guard flags used in the body
	Timeout   time.Duration
	// TimeoutText is the Timeout: value as written ("" for none).
	TimeoutText string
	// StepTimeout is Step-Timeout:, the default limit for each step.
	StepTimeout     time.Duration
	StepTimeoutText string
	Locks           []string // Locks: resources this round needs exclusively (default none)
	Extra           []KV     // other keys in the header block, kept and shown
	Problems        []string // header values swim couldn't use (e.g. a bad Timeout)
	Removed         []string // header keys of what this swim no longer has (Finally:, Matrix:, Modules:, ...)
	Message         string   // stub message
	// FailHard is Fail: hard: the first failed step stops the round.
	// Default soft. Script syntax 3.
	FailHard bool
	FailText string // the Fail: value as written ("" if unset)
	// Staged: the script calls stage (bash, or swim stage from another
	// language), so a read-only check has a place to stop.
	Staged bool
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
	// Observe are the round's probes (# Observe:): facts swim can't pin,
	// such as which binary PATH finds or the cloud account, read when a
	// guard is approved and again just before it returns (observe.go).
	Observe []Probe
	// Cache are the tool caches the round uses (# Cache: terraform
	// @infra/.terraform.lock.hcl): swim points each tool's download cache at
	// a directory that outlives the round, and pins the lockfiles the tool
	// checks the cache against (internal/cache).
	Cache []cache.Decl
	// Target is what the lane's steps talk to, by default (# Targets:
	// orders-api), logged with each step; a step's --target overrides it.
	Target string
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
var knownKeys = map[string]bool{"round": true, "task": true, "workspace": true, "job": true, "after": true, "owner": true, "created": true, "guards": true, "timeout": true, "step-timeout": true, "locks": true, "swim": true,
	"fail": true, "outputs": true, "includes": true,
	"observe": true, "cache": true, "modules": true, "targets": true}

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
	if info.Stub {
		info.Round, info.Job, info.After, info.Guards, info.Locks, info.Outputs = "", "", nil, nil, nil, nil
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
// named at run time) and the files it reads (# Includes:) are what the run
// allows. The body is read only for where
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
	}
	if info.Stub {
		info.Round, info.Job, info.After, info.Guards, info.Locks, info.Outputs = "", "", nil, nil, nil, nil
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
	case "Workspace":
		switch w := strings.ToLower(strings.TrimSpace(v)); w {
		case "", "-", "staged":
			info.Workspace = ""
		case WorkspaceLive:
			info.Workspace = WorkspaceLive
		default:
			info.Problems = append(info.Problems, fmt.Sprintf("Workspace %q is staged (the default) or live; the lane runs staged", v))
		}
	case "Task":
		if f := strings.Fields(v); len(f) > 0 && info.Task == "" {
			info.Task = f[0]
			if !ValidTaskName(f[0]) {
				info.Problems = append(info.Problems, fmt.Sprintf("Task %q is not a task name (a-z, 0-9 and -, starting with a letter, at most 40; not only hex digits and -); swim run can't find it by name", f[0]))
			}
		}
	case "Targets":
		// The lane's default target for its steps (logged with each).
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
	case "Fail":
		info.setFail(v, "Fail:")
	case "Cache":
		info.setCache(v, "Cache:")
	case "Guards":
		info.addGuard(v, listed)
	case "Matrix", "Matrix-Exclude", "Matrix-Parallel", "Matrix-Fail", "Always", "Finally", "Rerun-Children",
		"Handler-Timeout", "On-Success", "On-Failure", "On-Exit", "Modules":
		info.Removed = append(info.Removed, key)
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

// evidenceIn reports whether a line has an evidence step: `evidence
// "<label>" ...`, `swim evidence`, or ["evidence", ...] from another
// language.
func evidenceIn(line string, cmds [][]Word) bool {
	for _, c := range cmds {
		if len(c) > 1 && c[0].Text == "evidence" {
			return true
		}
		if rest, ok := swimCall(c); ok && len(rest) > 0 && rest[0].Text == "evidence" {
			return true
		}
	}
	return quotedThen(line, "evidence", ',')
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

// v1HeadKey reports whether a trimmed line is a Round, Job or After line.
func v1HeadKey(t string) bool {
	rest, ok := strings.CutPrefix(t, "#")
	rest = strings.TrimSpace(rest)
	return ok && (strings.HasPrefix(rest, "Round:") || strings.HasPrefix(rest, "Job:") || strings.HasPrefix(rest, "After:"))
}

// firstWordIs reports whether a line's first word is cmd, with something
// after it.
func firstWordIs(l, cmd string) bool {
	f := strings.Fields(l)
	return len(f) > 1 && f[0] == cmd
}

// Running returns the pid recorded for lane n if that process is alive.
func Running(root string, n int) (int, bool) {
	pid, _, ok := RunningOn(root, n)
	return pid, ok
}

// RunningOn is Running, with the machine the process runs on. A lane
// another machine claimed counts as running: this one can't see its
// process (two machines sharing a repo), so it never takes it for dead.
func RunningOn(root string, n int) (pid int, host string, ok bool) {
	return runningIn(PIDFile(root, n))
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
	return filepath.Join(root, ".swim", fmt.Sprintf("lane%d.claim", n))
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
// agentN.prev-<what>.log, and each job's log
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
		job := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".log")
		if job == "" {
			continue
		}
		moves = append(moves, [2]string{filepath.Join(dir, name), filepath.Join(dir, fmt.Sprintf("agent%d.prev-%s.%s.log", n, slug, job))})
	}
	if len(moves) == 0 {
		return nil, fmt.Errorf("swim %d has nothing to archive (no .swim/logs/agent%d.log, and no job logs)", n, n)
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

// validJobID reports whether id is a job id: 8 to 64 characters, letters,
// digits, . _ and -, starting with a letter or digit.
func validJobID(id string) bool {
	if len(id) < 8 || len(id) > 64 || !(isLetter(rune(id[0])) || isDigit(rune(id[0]))) {
		return false
	}
	return allOf(id, func(r rune) bool { return isLetter(r) || isDigit(r) || r == '.' || r == '_' || r == '-' })
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

// ValidTaskName reports whether s can name a lane (# Task:): a lower-case
// letter, then letters, digits and -, at most 40 in all, and not only hex
// digits and - (a job id's prefix could be that).
func ValidTaskName(s string) bool {
	if len(s) == 0 || len(s) > 40 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	hexOnly := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
		if !(r >= 'a' && r <= 'f' || r >= '0' && r <= '9' || r == '-') {
			hexOnly = false
		}
	}
	return !hexOnly
}

// WorkspaceLive is # Workspace: live: the lane runs in the repo's working
// tree rather than its staged copy.
const WorkspaceLive = "live"
