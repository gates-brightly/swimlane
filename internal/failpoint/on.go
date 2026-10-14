//go:build swimfailpoint

package failpoint

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Enabled is whether this binary was built with failpoints.
const Enabled = true

// modes is SWIM_FAILPOINT, read once: site -> what to do there.
var modes = parse(os.Getenv(EnvVar))

type mode struct {
	pause bool
	nth   int // act at the nth hit across processes (SWIM_FAILPOINT_DIR); 1 by default
}

func parse(spec string) map[string]mode {
	out := map[string]mode{}
	for _, f := range strings.Split(spec, ",") {
		name, how, _ := strings.Cut(strings.TrimSpace(f), ":")
		if name == "" {
			continue
		}
		how, at, _ := strings.Cut(how, "@")
		m := mode{pause: how == "pause", nth: 1}
		if n, err := strconv.Atoi(at); err == nil && n > 0 {
			m.nth = n
		}
		out[name] = m
	}
	return out
}

// Armed reports whether SWIM_FAILPOINT names site name and, with
// SWIM_FAILPOINT_DIR, it hasn't acted yet.
func Armed(name string) bool {
	if _, ok := modes[name]; !ok {
		return false
	}
	if dir := os.Getenv(DirEnv); dir != "" {
		if _, err := os.Stat(filepath.Join(dir, name+".hit")); err == nil {
			return false
		}
	}
	return true
}

// Hit acts at site name if SWIM_FAILPOINT names it: kill (SIGKILL this
// process) or pause (stop here for good). With SWIM_FAILPOINT_DIR, the
// hits of every process are counted there (DIR/<name>.<i>), and only the
// nth acts (name:kill@3 is the third; the first by default), recording its
// pid in DIR/<name>.hit first.
func Hit(name string) {
	m, ok := modes[name]
	if !ok {
		return
	}
	if dir := os.Getenv(DirEnv); dir != "" {
		if !count(dir, name, m.nth) {
			return
		}
		f, err := os.OpenFile(filepath.Join(dir, name+".hit"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return // another process acted here already
		}
		fmt.Fprintf(f, "%d\n", os.Getpid())
		f.Sync()
		f.Close()
	}
	if m.pause {
		for {
			time.Sleep(time.Hour)
		}
	}
	syscall.Kill(os.Getpid(), syscall.SIGKILL)
	select {} // not reached: SIGKILL can't be caught
}

// count takes the next hit number for name and reports whether it is the
// nth: creating DIR/<name>.<i> exclusively claims hit i.
func count(dir, name string, nth int) bool {
	for i := 1; i <= nth; i++ {
		f, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("%s.%d", name, i)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "failpoint %s: %v\n", name, err)
			return false
		}
		f.Close()
		return i == nth
	}
	return false
}
