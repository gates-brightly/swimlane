package testwait

import (
	"time"

	"golang.org/x/sys/unix"
)

// Exited waits until process pid is gone (a zombie counts as gone: it
// isn't running), at most within; false if it's still running. A pid that
// doesn't exist has exited.
func Exited(pid int, within time.Duration) bool {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return true // no such process
	}
	defer unix.Close(fd)
	ms := int(within / time.Millisecond)
	for {
		n, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, ms)
		if err == unix.EINTR {
			continue
		}
		return err == nil && n > 0
	}
}
