package proc

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

// Stat is /proc/<pid>/stat from field 3 on: the command name (field 2) is
// in parentheses and may hold spaces, so fields count from after the last
// ')'. f[0] is the state, f[2] the process group, f[19] the start time.
func Stat(pid string) ([]string, error) {
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

// StartTime is when process pid started, as an opaque string (clock ticks
// since boot). A reused pid has another.
func StartTime(pid int) (string, error) {
	f, err := Stat(strconv.Itoa(pid))
	if err != nil {
		return "", err
	}
	return f[19], nil
}

// zombie reports whether pid has exited and waits to be reaped (state Z,
// or X while it goes).
func zombie(pid int) bool {
	f, err := Stat(strconv.Itoa(pid))
	return err == nil && (f[0] == "Z" || f[0] == "X")
}
