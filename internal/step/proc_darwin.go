package step

import "golang.org/x/sys/unix"

const sZomb = 5 // p_stat of a zombie (SZOMB in sys/proc.h)

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
