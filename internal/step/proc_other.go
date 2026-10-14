//go:build !linux && !darwin

package step

// groupAlive reports whether anything is left in process group pgid
// (zombies included: there's no portable way to tell them apart).
func groupAlive(pgid int) bool { return groupExists(pgid) }
