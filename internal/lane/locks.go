package lane

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Resource locks across concurrent swim runs in one repo: one file per lock
// under .swim/locks/, held with flock. The lock lives as long as some process
// holds the open file: the launcher passes it to the lane's process, so a
// killed launcher doesn't release a lock its lane still uses, and a killed
// lane never leaves a stale one. The file's text says who holds it.

// ValidLockName reports whether name can be a lock: letters, digits, '.',
// '_', '-' and '/'.
func ValidLockName(name string) bool {
	return name != "" && allOf(name, func(r rune) bool { return isLetter(r) || isDigit(r) || strings.ContainsRune("._/-", r) }) &&
		!strings.Contains(name, "..")
}

// LockDir holds the lock files.
func LockDir(root string) string { return filepath.Join(root, ".swim", "locks") }

func lockPath(root, name string) string {
	return filepath.Join(LockDir(root), strings.ReplaceAll(name, "/", "%2F")+".lock")
}

// FileLocks is a set of held lock files.
type FileLocks []*os.File

// lockNote heads every lock file. The holder's line under it is only true
// while the lock is held, and a run that's killed can't take it back, so
// the file says how to tell (a reader with cat would otherwise take a
// finished run's line for a held lock).
const lockNote = "# held only while a swim process holds this file (flock), not because it names a holder: swim status lists the locks held now\n"

// lockText is a lock file's holder line, without lockNote.
func lockText(data []byte) string {
	var keep []string
	for _, l := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(l, "#") {
			keep = append(keep, l)
		}
	}
	return strings.TrimSpace(strings.Join(keep, "\n"))
}

// Release unlocks and closes them (the lane's process may still hold its
// inherited copy until it exits). Each file then says it's free, and who
// held it last.
func (fl FileLocks) Release() {
	for _, f := range fl {
		if data, err := os.ReadFile(f.Name()); err == nil {
			last := lockText(data)
			if !strings.HasPrefix(last, "free since ") {
				f.Truncate(0)
				f.WriteAt([]byte(lockNote+"free since "+time.Now().UTC().Format(time.RFC3339)+"; last held by "+last+"\n"), 0)
			}
		}
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}
}

// TryFileLocks takes every named lock, or none: if any is held by another
// run it releases what it took and returns the name and the holder's text.
func TryFileLocks(root string, names []string, holder string) (FileLocks, string, string, error) {
	names = append([]string(nil), names...)
	sort.Strings(names)
	if err := os.MkdirAll(LockDir(root), 0o755); err != nil {
		return nil, "", "", err
	}
	var got FileLocks
	for _, n := range names {
		f, err := os.OpenFile(lockPath(root, n), os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			got.Release()
			return nil, "", "", err
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			text, _ := os.ReadFile(lockPath(root, n))
			f.Close()
			got.Release()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				return nil, n, lockText(text), nil
			}
			return nil, "", "", err
		}
		f.Truncate(0)
		f.WriteAt([]byte(lockNote+holder+"\n"), 0)
		got = append(got, f)
	}
	return got, "", "", nil
}

// WaitFileLocks polls TryFileLocks until it succeeds or stop returns true.
// busy is called when another run holds a lock (with its name and holder).
func WaitFileLocks(root string, names []string, holder string, busy func(name, by string), stop func() bool) (FileLocks, bool, error) {
	lastBy := ""
	for {
		got, name, by, err := TryFileLocks(root, names, holder)
		if err != nil || name == "" {
			return got, err == nil, err
		}
		if busy != nil && name+by != lastBy {
			busy(name, by)
			lastBy = name + by
		}
		if stop() {
			return nil, false, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// HeldLock is a lock some process holds right now.
type HeldLock struct{ Name, By string }

// HeldLocks lists the locks currently held (by any run) in root.
func HeldLocks(root string) []HeldLock {
	files, _ := filepath.Glob(filepath.Join(LockDir(root), "*.lock"))
	sort.Strings(files)
	var out []HeldLock
	for _, p := range files {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		free := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB) == nil
		if free {
			syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		}
		f.Close()
		if free {
			continue
		}
		text, _ := os.ReadFile(p)
		name := strings.ReplaceAll(strings.TrimSuffix(filepath.Base(p), ".lock"), "%2F", "/")
		out = append(out, HeldLock{Name: name, By: lockText(text)})
	}
	return out
}

// LockHolder is the text written into a lock file.
func LockHolder(n int, job, run string) string {
	return fmt.Sprintf("lane %d round-id=%s run=%s pid=%d since=%s", n, job, run, os.Getpid(), time.Now().UTC().Format(time.RFC3339))
}
