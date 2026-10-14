package proc

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// StartTime is when process pid started, as an opaque string (the kernel's
// p_starttime). A reused pid has another.
func StartTime(pid int) (string, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	if int(kp.Proc.P_pid) != pid {
		return "", fmt.Errorf("no process %d", pid)
	}
	t := kp.Proc.P_starttime
	return fmt.Sprintf("%d.%06d", t.Sec, t.Usec), nil
}
