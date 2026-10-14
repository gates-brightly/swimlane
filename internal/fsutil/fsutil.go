// Package fsutil writes swim's state files so that a crash or a power loss
// leaves either the old file or the new one, whole.
//
// A rename alone is atomic for running processes, but not durable: without
// an fsync the kernel may write the rename to disk before the file's data,
// and after a power loss the file is empty or short. So the data is synced
// before the rename, and the directory after it (the rename itself lives
// in the directory).
//
// On macOS, fsync flushes to the drive but not through the drive's own
// cache (that takes F_FULLFSYNC, which is much slower): a power cut can
// still lose the last writes there, though never mix old and new.
package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"github.com/gates-brightly/swimlane/internal/failpoint"
)

// WriteAtomic replaces path with data: written to a temporary file in the
// same directory, synced, given perm, renamed over path, and the directory
// synced. Readers see the old file or the new one, never a partial one.
func WriteAtomic(path string, data []byte, perm os.FileMode) error {
	return WriteAtomicAt("", path, data, perm)
}

// WriteAtomicAt is WriteAtomic with a failpoint (internal/failpoint) named
// site halfway through writing the temporary file: a crash there must
// leave the old file whole. site "" is plain WriteAtomic.
func WriteAtomicAt(site, path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(name)
		}
	}()
	if site != "" && failpoint.Armed(site) {
		half := len(data) / 2
		if _, err := tmp.Write(data[:half]); err != nil {
			return err
		}
		tmp.Sync()
		failpoint.Hit(site)
		data = data[half:]
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	ok = true
	return SyncDir(dir)
}

// Rename moves oldpath to newpath and syncs the directories involved, so
// the move survives a crash right after it.
func Rename(oldpath, newpath string) error {
	if err := os.Rename(oldpath, newpath); err != nil {
		return err
	}
	if err := SyncDir(filepath.Dir(newpath)); err != nil {
		return err
	}
	if d := filepath.Dir(oldpath); d != filepath.Dir(newpath) {
		return SyncDir(d)
	}
	return nil
}

// SyncDir flushes a directory's entries (new, renamed or removed files)
// to disk. A filesystem that can't sync a directory (some network and FUSE
// ones) isn't an error: there's nothing more to do there.
func SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTSUP) {
		return err
	}
	return nil
}
