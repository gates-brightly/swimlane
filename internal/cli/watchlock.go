package cli

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/queue"
)

// watchLock is the file every swim watch holds a shared lock on while it
// runs, so others can tell a worker is up.
func watchLock(root string) string { return filepath.Join(root, ".swim", "watch.lock") }

// holdWatchLock marks this swim watch as running; release undoes it.
func holdWatchLock(root string) (release func()) {
	os.MkdirAll(filepath.Dir(watchLock(root)), 0o755)
	f, err := os.OpenFile(watchLock(root), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return func() {}
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_SH) != nil {
		f.Close()
		return func() {}
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }
}

// workerRunning reports whether a swim watch runs in this repo.
func workerRunning(root string) bool {
	f, err := os.OpenFile(watchLock(root), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.Is(err, syscall.EWOULDBLOCK)
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// claimDue claims the first waiting batch a worker may start now, in
// queue order. A batch an older swim scheduled (not_before) waits for its
// time.
func claimDue(root string, cfg *config.Config) (*queue.Entry, error) {
	entries, err := queue.List(root, false)
	if err != nil {
		return nil, err
	}
	queue.SortWaiting(entries)
	now := time.Now()
	for _, w := range entries {
		if w.State != queue.Queued || !w.Due(now) {
			continue
		}
		if why := cantRunHere(root, w); why != "" {
			holdHere(root, w, why)
			continue
		}
		holdHere(root, w, "")
		if e, err := queue.Claim(root, w, operator(), cfg.QueueLeaseFor()); err == nil {
			return e, nil
		}
	}
	return nil, nil
}
