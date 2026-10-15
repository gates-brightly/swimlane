package peer

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const supported = true

// PID is the process at the other end of unix socket c (SO_PEERCRED).
func PID(c net.Conn) (int, bool) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	pid := 0
	raw.Control(func(fd uintptr) {
		if cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED); err == nil {
			pid = int(cred.Pid)
		}
	})
	return pid, pid > 0
}

// parent is pid's parent, from /proc/<pid>/stat (the field after the
// command name, which may itself hold spaces and parentheses).
func parent(pid int) (int, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0, false
	}
	pp, err := strconv.Atoi(f[1])
	return pp, err == nil
}

// Cwd is pid's working directory (/proc/<pid>/cwd; a process of the same
// user can read it).
func Cwd(pid int) (string, bool) {
	dir, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd")
	return dir, err == nil && filepath.IsAbs(dir)
}

// comm is pid's command name (/proc/<pid>/comm, 15 characters at most).
func comm(pid int) (string, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(data)), true
}

func shortName(name string) string {
	if len(name) > 15 {
		return name[:15]
	}
	return name
}
