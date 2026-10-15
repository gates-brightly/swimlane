//go:build !linux && !darwin

package peer

import "net"

const supported = false

// PID can't name a socket's peer on this platform.
func PID(net.Conn) (int, bool) { return 0, false }

func parent(int) (int, bool) { return 0, false }

func comm(int) (string, bool) { return "", false }

// Cwd can't read another process's working directory on this platform.
func Cwd(int) (string, bool) { return "", false }

func shortName(name string) string { return name }
