//go:build !swimfailpoint

package failpoint

// Enabled is whether this binary was built with failpoints.
const Enabled = false

// Hit acts at site name if SWIM_FAILPOINT names it. Without the
// swimfailpoint tag it does nothing.
func Hit(name string) {}

// Armed reports whether SWIM_FAILPOINT names site name (and it hasn't
// acted yet), for a site that must set things up before its Hit (a write
// split in two). Without the swimfailpoint tag it is always false.
func Armed(name string) bool { return false }
