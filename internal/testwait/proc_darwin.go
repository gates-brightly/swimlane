package testwait

import (
	"time"

	"golang.org/x/sys/unix"
)

// Exited waits until process pid is gone (a zombie counts as gone: it
// isn't running), at most within; false if it's still running. A pid that
// doesn't exist has exited.
func Exited(pid int, within time.Duration) bool {
	kq, err := unix.Kqueue()
	if err != nil {
		return false
	}
	defer unix.Close(kq)
	ev := unix.Kevent_t{Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT}
	if _, err := unix.Kevent(kq, []unix.Kevent_t{ev}, nil, nil); err != nil {
		return true // ESRCH: no such process
	}
	ts := unix.NsecToTimespec(int64(within))
	out := make([]unix.Kevent_t, 1)
	for {
		n, err := unix.Kevent(kq, nil, out, &ts)
		if err == unix.EINTR {
			continue
		}
		return err == nil && n > 0
	}
}
