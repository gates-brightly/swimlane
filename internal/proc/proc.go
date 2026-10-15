// Package proc tells whether a recorded process is still the one that was
// recorded. A pid alone can't: once a process exits, the kernel hands its
// pid to the next one, so a pid file left by a killed lane can name an
// unrelated live process. Recording the pid with the process's start time
// tells them apart.
package proc

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// Host is this machine's name (os.Hostname; "" if it can't be read). A
// recorded process is named with its host: two machines can share a repo
// (an operator's laptop and a container mounting it), and a pid means
// nothing on another host.
func Host() string {
	hostOnce.Do(func() { host, _ = os.Hostname() })
	return host
}

var (
	hostOnce sync.Once
	host     string
)

// Local reports whether a process recorded on host h runs on this
// machine, so this one can tell whether it's alive. A record without a
// host (from an older swim) is taken as local, as it always was.
func Local(h string) bool { return h == "" || h == Host() }

// Alive reports whether a process with pid exists (whoever it is now). A
// zombie (exited, never reaped) doesn't count: it keeps its pid and start
// time, and where nothing reaps orphans (a container whose pid 1 isn't an
// init) a killed lane stays a zombie for good.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return (err == nil || errors.Is(err, syscall.EPERM)) && !zombie(pid)
}

// Start is when process pid started, as an opaque string ("" when it
// can't be read: the process is gone, or this system can't tell).
func Start(pid int) string {
	st, err := StartTime(pid)
	if err != nil {
		return ""
	}
	return st
}

// Same reports whether pid is still the process that had start time start
// when it was recorded. With no recorded start (an older record), or where
// the start time can't be read, a live pid counts: unsure is alive.
func Same(pid int, start string) bool {
	if !Alive(pid) {
		return false
	}
	if start == "" || start == "-" {
		return true
	}
	now, err := StartTime(pid)
	if err != nil {
		return true
	}
	return now == start
}

// Record is "pid start" (start "-" when unknown), the form pid files hold.
func Record(pid int) string {
	st := Start(pid)
	if st == "" {
		st = "-"
	}
	return strconv.Itoa(pid) + " " + st
}

// Parse reads a Record (or a bare pid, from before start times were
// recorded: start is then ""). ok is false for anything else.
func Parse(s string) (pid int, start string, ok bool) {
	f := strings.Fields(s)
	if len(f) == 0 || len(f) > 2 {
		return 0, "", false
	}
	pid, err := strconv.Atoi(f[0])
	if err != nil || pid <= 0 {
		return 0, "", false
	}
	if len(f) == 2 && f[1] != "-" {
		start = f[1]
	}
	return pid, start, true
}
