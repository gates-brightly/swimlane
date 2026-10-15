package peer

import (
	"bytes"
	"net"

	"golang.org/x/sys/unix"
)

const supported = true

// PID is the process at the other end of unix socket c (LOCAL_PEERPID).
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
		if p, err := unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID); err == nil {
			pid = p
		}
	})
	return pid, pid > 0
}

// parent is pid's parent (kern.proc.pid).
func parent(pid int) (int, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(kp.Proc.P_pid) != pid {
		return 0, false
	}
	return int(kp.Eproc.Ppid), true
}

// comm is pid's command name (p_comm, 16 characters at most).
func comm(pid int) (string, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(kp.Proc.P_pid) != pid {
		return "", false
	}
	b := kp.Proc.P_comm[:]
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b), true
}

func shortName(name string) string {
	if len(name) > 16 {
		return name[:16]
	}
	return name
}
