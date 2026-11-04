package main

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/gates-brightly/swimlane/internal/testwait"
)

// cmdAwait waits for a path to exist, never for a time (as internal/e2e
// does): dag99's lane 1 holds at E2E_HOLD until the test creates it. A
// deadline only fails a test that hung.
//
//	e2etool await PATH
func cmdAwait(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: e2etool await PATH")
	}
	path := args[0]
	dir := filepath.Dir(path)
	os.MkdirAll(dir, 0o755)
	w, err := testwait.Watch(dir, nil)
	if err != nil {
		return err
	}
	defer w.Close()
	exists := func() bool { _, err := os.Stat(path); return err == nil }
	if !testwait.UntilOK(w, exists, testwait.Deadline) {
		return exitCode(1)
	}
	return nil
}
