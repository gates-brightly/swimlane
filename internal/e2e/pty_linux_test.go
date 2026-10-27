package e2e

import (
	"fmt"
	"os"
	"syscall"
	"testing"
	"unsafe"
)

// openPTY opens a pseudo-terminal pair with only the standard library
// (Linux /dev/ptmx). It returns the controlling side and the terminal.
func openPTY(t *testing.T) (ptm, pts *os.File) {
	t.Helper()
	ptm, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skip("no /dev/ptmx:", err)
	}
	var unlock int32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, ptm.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); e != 0 {
		ptm.Close()
		t.Skip("unlockpt:", e)
	}
	var n uint32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, ptm.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))); e != 0 {
		ptm.Close()
		t.Skip("ptsname:", e)
	}
	pts, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		ptm.Close()
		t.Skip("open pts:", err)
	}
	return ptm, pts
}

// tail is the end of s, for a failure message.
func tail(s string) string {
	if len(s) > 200 {
		return "…" + s[len(s)-200:]
	}
	return s
}
