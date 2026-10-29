package testwait

import (
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

// Watcher is a Source for a directory tree: it changes when a file or
// directory under it is created, written, removed or renamed (kqueue: each
// directory and file is watched, and a changed directory is rescanned for
// new entries).
type Watcher struct {
	Signal
	kq   int
	mu   sync.Mutex
	fds  map[string]int // path -> its O_EVTONLY fd
	path map[int]string
	skip func(string) bool
	done chan struct{}
}

const (
	vnodeFlags = unix.NOTE_WRITE | unix.NOTE_EXTEND | unix.NOTE_DELETE | unix.NOTE_RENAME | unix.NOTE_ATTRIB
	wakeIdent  = 1
)

// Watch watches root and everything under it, except directories skip
// says to leave out (nil: none), and what's created later.
func Watch(root string, skip func(dir string) bool) (*Watcher, error) {
	kq, err := unix.Kqueue()
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(kq)
	w := &Watcher{kq: kq, fds: map[string]int{}, path: map[int]string{}, skip: skip, done: make(chan struct{})}
	ev := unix.Kevent_t{Ident: wakeIdent, Filter: unix.EVFILT_USER, Flags: unix.EV_ADD | unix.EV_CLEAR}
	if _, err := unix.Kevent(kq, []unix.Kevent_t{ev}, nil, nil); err != nil {
		unix.Close(kq)
		return nil, err
	}
	w.scan(root, root)
	go w.loop(root)
	return w, nil
}

// scan watches p and, for a directory, everything in it not yet watched.
func (w *Watcher) scan(root, p string) {
	filepath.WalkDir(p, func(q string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && q != root && w.skip != nil && w.skip(q) {
			return filepath.SkipDir
		}
		w.add(q)
		return nil
	})
}

func (w *Watcher) add(p string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.fds[p]; ok {
		return
	}
	fd, err := unix.Open(p, unix.O_EVTONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return
	}
	ev := unix.Kevent_t{Ident: uint64(fd), Filter: unix.EVFILT_VNODE, Flags: unix.EV_ADD | unix.EV_CLEAR, Fflags: vnodeFlags}
	if _, err := unix.Kevent(w.kq, []unix.Kevent_t{ev}, nil, nil); err != nil {
		unix.Close(fd)
		return
	}
	w.fds[p], w.path[fd] = fd, p
}

func (w *Watcher) drop(fd int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if p, ok := w.path[fd]; ok {
		delete(w.path, fd)
		delete(w.fds, p)
		unix.Close(fd)
	}
}

func (w *Watcher) loop(root string) {
	defer close(w.done)
	evs := make([]unix.Kevent_t, 64)
	for {
		n, err := unix.Kevent(w.kq, nil, evs, nil)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return
		}
		for _, ev := range evs[:n] {
			if ev.Filter == unix.EVFILT_USER {
				return // Close
			}
			fd := int(ev.Ident)
			w.mu.Lock()
			p := w.path[fd]
			w.mu.Unlock()
			switch {
			case ev.Fflags&(unix.NOTE_DELETE|unix.NOTE_RENAME) != 0:
				w.drop(fd) // gone, or renamed away: a new file there is a new entry
				if p != "" {
					w.scan(root, filepath.Dir(p))
				}
			case ev.Fflags&unix.NOTE_WRITE != 0 && p != "":
				if st, err := os.Stat(p); err == nil && st.IsDir() {
					w.scan(root, p) // an entry was added, removed or renamed
				}
			}
		}
		w.Notify()
	}
}

// Close stops watching.
func (w *Watcher) Close() {
	ev := unix.Kevent_t{Ident: wakeIdent, Filter: unix.EVFILT_USER, Fflags: unix.NOTE_TRIGGER}
	unix.Kevent(w.kq, []unix.Kevent_t{ev}, nil, nil)
	<-w.done
	w.mu.Lock()
	for fd := range w.path {
		unix.Close(fd)
	}
	w.mu.Unlock()
	unix.Close(w.kq)
}
