package launcher

import "golang.org/x/sys/unix"

// hideMemory makes the launcher non-dumpable: a process of the same user
// (a lane) can't ptrace it or read its /proc/<pid>/mem or environ, so it
// can't lift the run key from its memory. Lanes are unaffected: exec
// makes each dumpable again.
func hideMemory() { unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0) }
