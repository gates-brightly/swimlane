package step

import (
	"os"
	"strconv"

	"github.com/gates-brightly/swimlane/internal/proc"
)

// groupAlive reports whether a live process is left in process group pgid.
// Zombies don't count: where nothing reaps orphans (a container without an
// init), they linger, and a signal to the group still finds them.
func groupAlive(pgid int) bool {
	if !groupExists(pgid) {
		return false
	}
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return true
	}
	want := strconv.Itoa(pgid)
	for _, e := range ents {
		if n := e.Name(); n[0] >= '1' && n[0] <= '9' {
			if f, err := proc.Stat(n); err == nil && f[2] == want && f[0] != "Z" && f[0] != "X" {
				return true
			}
		}
	}
	return false
}
