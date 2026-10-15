// Package peer says which process is at the other end of a unix socket,
// and whether it descends from another: swim's sockets (the approval
// broker, a lane's SDK socket) answer only the processes of the lane they
// serve, not merely whoever holds its token.
package peer

import "net"

// Supported reports whether this platform can name a socket's peer; where
// it can't, callers fall back to the token alone.
const Supported = supported

// Descends reports whether process pid is ancestor or one of its
// descendants, walking parent links up to init. A process that left its
// lane's tree (daemonized, reparented to init) doesn't.
func Descends(pid, ancestor int) bool {
	for i := 0; i < 4096 && pid > 1; i++ {
		if pid == ancestor {
			return true
		}
		pp, ok := parent(pid)
		if !ok || pp == pid {
			return false
		}
		pid = pp
	}
	return pid == ancestor
}

// FromLane reports whether the process at the other end of c is pid or
// descends from it. ok is false when the platform can't tell (see
// Supported) or the peer can't be read.
func FromLane(c net.Conn, pid int) (from, ok bool) {
	p, ok := PID(c)
	if !ok {
		return false, false
	}
	return Descends(p, pid), true
}

// AncestorNamed reports whether a process above pid (its parent, theirs,
// up to init) runs a program called name (the kernel's short command
// name, cut as the kernel cuts it). A process can't change who started
// it, so swim's hidden commands use this to tell a lane swim run started
// (whatever its environment says) from one run directly with bash.
func AncestorNamed(pid int, name string) bool {
	for i := 0; i < 4096 && pid > 1; i++ {
		pp, ok := parent(pid)
		if !ok || pp == pid || pp <= 1 {
			return false
		}
		if c, ok := comm(pp); ok && c == shortName(name) {
			return true
		}
		pid = pp
	}
	return false
}
