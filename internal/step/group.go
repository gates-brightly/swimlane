package step

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// A step runs in its own process group, so a time limit (or an interrupt)
// reaches everything it started: bash -c 'server & tests' leaves no server
// behind. While it runs, swim step records the group in <spool>.pgid; if
// swim step itself is SIGKILLed it can't clean up, so the launcher's kill
// and RecoverSpool kill the recorded group instead.

// DefaultKillGrace is how long a stopped step has between SIGTERM (or the
// forwarded interrupt) and SIGKILL.
const DefaultKillGrace = 5 * time.Second

// GroupSuffix ends the name of a running step's process group record.
const GroupSuffix = ".partial.pgid"

// GroupPath is where a running step records its process group: the group
// id (the step's pid) and the process's start time.
func GroupPath(logPath string) string { return logPath + GroupSuffix }

// recordGroup writes the step's group record; a start time that can't be
// read is "-" (the group is then killed only once its leader is gone).
func recordGroup(logPath string, pid int) {
	st, err := startTime(pid)
	if err != nil {
		st = "-"
	}
	os.WriteFile(GroupPath(logPath), []byte(fmt.Sprintf("%d %s\n", pid, st)), 0o644)
}

// groupExists reports whether any process, a zombie included, is left in
// process group pgid.
func groupExists(pgid int) bool {
	err := syscall.Kill(-pgid, 0)
	return err == nil || err == syscall.EPERM
}

// endGroup makes sure nothing is left of a stopped step's group: what
// survives the grace after the signal that stopped it is killed.
func endGroup(pgid int, until time.Time) {
	for groupAlive(pgid) {
		if !time.Now().Before(until) {
			syscall.Kill(-pgid, syscall.SIGKILL)
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// KillRecorded SIGKILLs the process group a step recorded at logPath (one
// whose swim step was killed before it could clean up) and removes the
// record. The group is killed only while it is still that step's: its
// leader has the recorded start time, or the leader is gone but the group
// lives on (a pid isn't handed out again while a group still uses it).
// It reports whether it sent the signal.
func KillRecorded(logPath string) bool {
	p := GroupPath(logPath)
	data, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	os.Remove(p)
	f := strings.Fields(string(data))
	if len(f) != 2 {
		return false
	}
	pid, err := strconv.Atoi(f[0])
	if err != nil || pid <= 1 {
		return false
	}
	if st, err := startTime(pid); err == nil {
		if f[1] == "-" || st != f[1] {
			return false // the pid now belongs to another process
		}
	} else if !groupAlive(pid) {
		return false
	}
	return syscall.Kill(-pid, syscall.SIGKILL) == nil
}
