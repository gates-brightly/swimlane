//go:build !linux && !darwin

package proc

import "errors"

// StartTime isn't available here: callers fall back to the pid alone.
func StartTime(pid int) (string, error) {
	return "", errors.New("process start time not supported on this system")
}

// zombie can't be told here: a zombie counts as alive.
func zombie(pid int) bool { return false }
