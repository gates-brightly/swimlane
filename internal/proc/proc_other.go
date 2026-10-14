//go:build !linux && !darwin

package proc

import "errors"

// StartTime isn't available here: callers fall back to the pid alone.
func StartTime(pid int) (string, error) {
	return "", errors.New("process start time not supported on this system")
}
