//go:build !linux

package launcher

// hideMemory does nothing here: the OS's own rules for debugging another
// process are what keep a lane out of the launcher's memory.
func hideMemory() {}
