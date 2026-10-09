// Package lane knows a lane's files: lane.N.sh (lane script), .swim/logs/agentN.log (log),
// .swim/laneN.pid (running marker) and .swim/snapshots/.
package lane

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gates-brightly/swimlane/internal/syntax"
)

// StubMarker identifies a "nothing pending" lane script.
const StubMarker = "# swim:stub"

func Script(root string, n int) string { return filepath.Join(root, fmt.Sprintf("lane.%d.sh", n)) }
func Log(root string, n int) string {
	return filepath.Join(LogDir(root), fmt.Sprintf("agent%d.log", n))
}

// LogDir holds lane logs and their archives.
func LogDir(root string) string { return filepath.Join(root, ".swim", "logs") }
func PIDFile(root string, n int) string {
	return filepath.Join(root, ".swim", fmt.Sprintf("lane%d.pid", n))
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
	Locks       []string // Locks: resources this round needs exclusively (default none)
	Extra       []KV     // other keys in the header block, kept and shown
	Problems    []string // header values swim couldn't use (e.g. a bad Timeout)
	Message     string   // stub message
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
	roundRE   = regexp.MustCompile(`^#?[ \t]?Round:\s*(.*)$`)
	jobRE     = regexp.MustCompile(`^#[ \t]?Job:\s*(\S+)`)
	afterRE   = regexp.MustCompile(`^#[ \t]?After:\s*(.*)$`)
	metaRE    = regexp.MustCompile(`^#[ \t]?(Owner|Created|Guards|Timeout|Locks):\s*(.*)$`)
	keyRE     = regexp.MustCompile(`^#[ \t]?([A-Za-z][A-Za-z0-9 _-]*?):\s*(.*)$`)
	sepRE     = regexp.MustCompile(`[\s,]+`)
	guardUse  = regexp.MustCompile(`(?:^|[\s;&|(!])guard\s+([A-Za-z_][A-Za-z0-9_]*)`)
	knownKeys = map[string]bool{"round": true, "job": true, "after": true, "owner": true, "created": true, "guards": true, "timeout": true, "locks": true, "swim": true}
)

// ReadScript inspects lane.N.sh. A lane script without a Round: line is
// treated as not pending, so a half-written file is never launched by
// `swim run`. A script in a newer syntax than this swim reads is an error.
func ReadScript(root string, n int) (Info, error) {
	path := Script(root, n)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Info{}, nil
	}
	if err != nil {
		return Info{}, err
	}
	info := ParseScript(string(data))
	if err := syntax.Check(filepath.Base(path), info.Syntax); err != nil {
		return Info{}, err
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
// lines straight after the shebang and syntax line).
func ParseScript(src string) Info {
	info := Info{Exists: true, Syntax: syntax.OfScript(src)}
	lines := strings.Split(src, "\n")
	inBlock := true
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
				case "Locks":
					for _, tok := range sepRE.Split(v, -1) {
						switch {
						case tok == "" || tok == "-" || strings.EqualFold(tok, "none"):
						case !ValidLockName(tok):
							info.Problems = append(info.Problems, fmt.Sprintf("Locks: %q is not a lock name (letters, digits, . _ - /); ignored", tok))
						default:
							info.Locks = append(info.Locks, tok)
						}
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
		// Guard flags used in the body count even if the header doesn't list them.
		if !strings.HasPrefix(line, "#") {
			for _, m := range guardUse.FindAllStringSubmatch(line, -1) {
				if !listed[m[1]] {
					listed[m[1]] = true
					info.Guards = append(info.Guards, Guard{Flag: m[1]})
				}
			}
		}
	}
	if info.Stub {
		info.Round, info.Job, info.After, info.Guards, info.Locks = "", "", nil, nil, nil
	}
	return info
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
func Running(root string, n int) (int, bool) {
	data, err := os.ReadFile(PIDFile(root, n))
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, Alive(pid)
}

// Alive reports whether a process with pid exists.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// WritePID records pid as lane n's running lane script. It refuses if another
// live process already holds the lane.
func WritePID(root string, n, pid int) error {
	if other, ok := Running(root, n); ok && other != pid {
		return fmt.Errorf("swim %d is already running (pid %d)", n, other)
	}
	if err := os.MkdirAll(filepath.Dir(PIDFile(root, n)), 0o755); err != nil {
		return err
	}
	return os.WriteFile(PIDFile(root, n), []byte(strconv.Itoa(pid)+"\n"), 0o644)
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

// Archive renames .swim/logs/agentN.log to agentN.prev-<what>.log there and returns the new
// path. It never overwrites an existing archive.
func Archive(root string, n int, what string) (string, error) {
	if err := refuseRunning(root, n); err != nil {
		return "", err
	}
	slug := Slug(what)
	if slug == "" {
		return "", errors.New("archive needs a description, e.g. `swim archive 1 cutover-api`")
	}
	src := Log(root, n)
	if _, err := os.Stat(src); err != nil {
		return "", fmt.Errorf("nothing to archive: %w", err)
	}
	dst := filepath.Join(LogDir(root), fmt.Sprintf("agent%d.prev-%s.log", n, slug))
	if _, err := os.Stat(dst); err == nil {
		return "", fmt.Errorf("%s already exists; choose a different description", filepath.Base(dst))
	}
	return dst, os.Rename(src, dst)
}

// WriteScript writes content to lane.N.sh (mode 0755) unless the lane is
// running. Unless force is set it also refuses to replace a pending round.
func WriteScript(root string, n int, content string, force bool) error {
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
	tmp := Script(root, n) + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o755); err != nil {
		return err
	}
	// Rename gives running readers (there should be none) the old inode
	// rather than a half-written file.
	return os.Rename(tmp, Script(root, n))
}

// NewJobID returns a random (version 4) UUID.
func NewJobID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
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

var scriptNameRE = regexp.MustCompile(`^lane\.([0-9]+)\.sh$`)

// ScriptsBeyond returns the numbers of lane scripts in root numbered above
// n (e.g. lane.5.sh when only 4 lanes are configured), sorted.
func ScriptsBeyond(root string, n int) []int {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range entries {
		if m := scriptNameRE.FindStringSubmatch(e.Name()); m != nil {
			if k, err := strconv.Atoi(m[1]); err == nil && k > n {
				out = append(out, k)
			}
		}
	}
	sort.Ints(out)
	return out
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
