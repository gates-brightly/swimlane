package step

import (
	"fmt"

	"golang.org/x/sys/unix"
)

const sZomb = 5 // p_stat of a zombie (SZOMB in sys/proc.h)

// startTime is when process pid started, as an opaque string (the kernel's
// p_starttime). A reused pid has another.
func startTime(pid int) (string, error) {
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

// groupAlive reports whether a live process is left in process group pgid.
// Zombies don't count: a signal to the group still finds them until
// they're reaped.
func groupAlive(pgid int) bool {
	if !groupExists(pgid) {
		return false
	}
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pgid)
	if err != nil {
		return true
	}
	for _, kp := range kps {
		if kp.Proc.P_stat != sZomb {
			return true
		}
	}
	return false
}
