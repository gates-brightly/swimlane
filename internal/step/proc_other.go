//go:build !linux && !darwin

package step

import "errors"

// startTime isn't available here: a recorded group is then killed only
// once its leader is gone (see KillRecorded).
func startTime(pid int) (string, error) {
	return "", errors.New("process start time not supported on this system")
}

// groupAlive reports whether anything is left in process group pgid
// (zombies included: there's no portable way to tell them apart).
func groupAlive(pgid int) bool { return groupExists(pgid) }
