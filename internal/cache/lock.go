package cache

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
)

// CheckDir refuses a cache directory that is a link, isn't a directory,
// isn't owned by this user, or that its group or others can write: anyone
// who can write a cache can plant what a lane runs.
func CheckDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("cache directory %s is a symlink; swim uses only a directory of your own", dir)
	case !fi.IsDir():
		return fmt.Errorf("cache directory %s isn't a directory", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("cache directory %s belongs to %s, not you; remove it or set cache_dir elsewhere", dir, owner(st.Uid))
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("cache directory %s is writable by others (mode %o); chmod 700 it", dir, fi.Mode().Perm())
	}
	return nil
}

func owner(uid uint32) string {
	if u, err := user.LookupId(strconv.Itoa(int(uid))); err == nil {
		return u.Username
	}
	return "uid " + strconv.Itoa(int(uid))
}

func (r *Repo) lockPath(name, kind string) string {
	return filepath.Join(r.Dir, ".locks", name+"."+kind)
}

func openLock(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
}

// Use takes a shared hold on preset name for a lane running with it. The
// caller passes the file to the lane's process (exec.Cmd.ExtraFiles), so
// the hold lasts as long as the lane runs, even if the launcher dies; it
// closes its own copy once the lane has started. Eviction and swim cache
// rm skip a cache someone holds.
func (r *Repo) Use(name string) (*os.File, error) {
	f, err := openLock(r.lockPath(name, "use"))
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// InUse reports whether a running lane holds preset name.
func (r *Repo) InUse(name string) bool {
	unlock, ok, err := r.tryExclusiveUse(name)
	if err != nil || !ok {
		return true // can't tell: as good as held, so nothing removes it
	}
	unlock()
	return false
}

func (r *Repo) tryExclusiveUse(name string) (func(), bool, error) {
	f, err := openLock(r.lockPath(name, "use"))
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, true, nil
}

// Write takes preset name's write lock, for a step that writes a cache
// that isn't safe for two writers (Exclusive). It blocks; waiting is
// called once, first, if another step holds it.
func (r *Repo) Write(name string, waiting func()) (unlock func(), err error) {
	f, err := openLock(r.lockPath(name, "write"))
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, err
		}
		if waiting != nil {
			waiting()
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			f.Close()
			return nil, err
		}
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
