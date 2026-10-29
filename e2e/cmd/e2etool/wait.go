package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gates-brightly/swimlane/internal/testwait"
)

// The scenarios wait for events, never a fixed time (as internal/e2e
// does): these block until something changes, and a deadline only fails a
// scenario that hung.
//
//	e2etool await PATH [--pid PID]   until PATH exists (exit 1 if PID exits first, or at the deadline)
//	e2etool exited PID SECONDS       until process PID exits (exit 1 if it's still running after SECONDS)
//	e2etool fingerprint DIR          the state of the files under DIR, for changed
//	e2etool changed DIR FP           until the files under DIR differ from FP (at once if they already do)

// cmdAwait waits for a path to exist: a held lane step (until the scenario
// touches it), or a scenario waiting for a lane's marker.
func cmdAwait(args []string) error {
	if len(args) != 1 && !(len(args) == 3 && args[1] == "--pid") {
		return errors.New("usage: e2etool await PATH [--pid PID]")
	}
	path := args[0]
	dir := filepath.Dir(path)
	os.MkdirAll(dir, 0o755)
	w, err := testwait.Watch(dir, nil)
	if err != nil {
		return err
	}
	defer w.Close()
	src := testwait.Any{w}
	gone := func() bool { return false }
	if len(args) == 3 {
		pid, err := strconv.Atoi(args[2])
		if err != nil {
			return fmt.Errorf("--pid %q: %v", args[2], err)
		}
		exit, exited := processExit(pid)
		src = append(src, exit)
		gone = exited
	}
	exists := func() bool { _, err := os.Stat(path); return err == nil }
	if !testwait.UntilOK(src, func() bool { return exists() || gone() }, testwait.Deadline) || !exists() {
		return exitCode(1)
	}
	return nil
}

// processExit is a Source that changes when pid exits, and whether it has.
func processExit(pid int) (testwait.Source, func() bool) {
	var s testwait.Signal
	done := make(chan struct{})
	go func() {
		if testwait.Exited(pid, testwait.Deadline) {
			close(done)
			s.Notify()
		}
	}()
	return &s, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}
}

// cmdExited waits for a process to exit, at most SECONDS (a float).
func cmdExited(args []string) error {
	if len(args) != 2 {
		return errors.New("usage: e2etool exited PID SECONDS")
	}
	pid, err := strconv.Atoi(args[0])
	if err != nil {
		return err
	}
	secs, err := strconv.ParseFloat(args[1], 64)
	if err != nil {
		return err
	}
	if !testwait.Exited(pid, time.Duration(secs*float64(time.Second))) {
		return exitCode(1)
	}
	return nil
}

// fingerprint hashes every file's path, size and modification time under
// dir: any write, creation, removal or rename changes it.
func fingerprint(dir string) string {
	h := sha256.New()
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if info, err := d.Info(); err == nil {
			fmt.Fprintf(h, "%s %d %d\n", p, info.Size(), info.ModTime().UnixNano())
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))
}

func cmdFingerprint(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: e2etool fingerprint DIR")
	}
	fmt.Println(fingerprint(args[0]))
	return nil
}

// cmdChanged blocks until the files under DIR no longer match FP (taken
// before the caller checked its condition, so a change while it did
// isn't missed).
func cmdChanged(args []string) error {
	if len(args) != 2 {
		return errors.New("usage: e2etool changed DIR FP")
	}
	dir, fp := args[0], args[1]
	w, err := testwait.Watch(dir, func(d string) bool { return filepath.Base(d) == ".git" })
	if err != nil {
		return err
	}
	defer w.Close()
	if !testwait.UntilOK(w, func() bool { return fingerprint(dir) != fp }, testwait.Deadline) {
		return exitCode(1)
	}
	return nil
}
