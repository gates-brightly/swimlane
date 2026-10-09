// Package lane knows a lane's files: lane.N.sh (lane script), agentN.log (log),
// .swim/laneN.pid (running marker) and .swim/snapshots/.
package lane

import (
	"bufio"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// StubMarker identifies a "nothing pending" lane script.
const StubMarker = "# swim:stub"

func Script(root string, n int) string { return filepath.Join(root, fmt.Sprintf("lane.%d.sh", n)) }
func Log(root string, n int) string    { return filepath.Join(root, fmt.Sprintf("agent%d.log", n)) }
func PIDFile(root string, n int) string {
	return filepath.Join(root, ".swim", fmt.Sprintf("lane%d.pid", n))
}
func SnapshotDir(root string) string { return filepath.Join(root, ".swim", "snapshots") }
func RCFile(root string, n int) string {
	return filepath.Join(root, fmt.Sprintf(".lane.%d.rc", n))
}

// Info describes the lane script currently on disk.
type Info struct {
	Exists  bool
	Stub    bool
	Round   string // first Round: line (lane scripts only)
	Job     string // Job: line, the job's id ("" if the script has none)
	Message string // stub message
}

// Pending reports whether the lane script holds a round to run.
func (i Info) Pending() bool { return i.Exists && !i.Stub && i.Round != "" }

var (
	roundRE = regexp.MustCompile(`^#?\s*Round:\s*(.*)$`)
	jobRE   = regexp.MustCompile(`^#\s*Job:\s*(\S+)`)
)

// ReadScript inspects lane.N.sh. A lane script without a Round: line is treated
// as not pending, so a half-written file is never launched by `swim run`.
func ReadScript(root string, n int) (Info, error) {
	f, err := os.Open(Script(root, n))
	if errors.Is(err, os.ErrNotExist) {
		return Info{}, nil
	}
	if err != nil {
		return Info{}, err
	}
	defer f.Close()
	info := Info{Exists: true}
	sc := bufio.NewScanner(f)
	for i := 0; sc.Scan() && i < 200; i++ {
		line := strings.TrimSpace(sc.Text())
		if line == StubMarker {
			info.Stub = true
			continue
		}
		if m := jobRE.FindStringSubmatch(line); m != nil && info.Job == "" {
			info.Job = m[1]
			continue
		}
		if info.Stub && strings.HasPrefix(line, "# ") && info.Message == "" && !roundRE.MatchString(line) && i > 0 {
			info.Message = strings.TrimPrefix(line, "# ")
		}
		if m := roundRE.FindStringSubmatch(line); m != nil && info.Round == "" {
			info.Round = strings.TrimSpace(m[1])
		}
	}
	if info.Stub {
		info.Round, info.Job = "", ""
	}
	return info, sc.Err()
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

// Archive renames agentN.log to agentN.prev-<what>.log and returns the new
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
	dst := filepath.Join(root, fmt.Sprintf("agent%d.prev-%s.log", n, slug))
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
