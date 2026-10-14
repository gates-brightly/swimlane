package step

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

// procStat is /proc/<pid>/stat from field 3 on: the command name (field 2)
// is in parentheses and may hold spaces, so fields count from after the
// last ')'. f[0] is the state, f[2] the process group, f[19] the start time.
func procStat(pid string) ([]string, error) {
	data, err := os.ReadFile("/proc/" + pid + "/stat")
	if err != nil {
		return nil, err
	}
	i := strings.LastIndexByte(string(data), ')')
	if i < 0 {
		return nil, errors.New("unexpected /proc stat format")
	}
	f := strings.Fields(string(data[i+1:]))
	if len(f) < 20 {
		return nil, errors.New("unexpected /proc stat format")
	}
	return f, nil
}

// startTime is when process pid started, as an opaque string (clock ticks
// since boot). A reused pid has another.
func startTime(pid int) (string, error) {
	f, err := procStat(strconv.Itoa(pid))
	if err != nil {
		return "", err
	}
	return f[19], nil
}

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
			if f, err := procStat(n); err == nil && f[2] == want && f[0] != "Z" && f[0] != "X" {
				return true
			}
		}
	}
	return false
}
