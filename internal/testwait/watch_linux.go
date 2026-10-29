package testwait

import (
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Watcher is a Source for a directory tree: it changes when a file or
// directory under it is created, written, removed or renamed (inotify).
type Watcher struct {
	Signal
	fd, wake int // the inotify fd; the pipe Close writes to
	wakeR    int
	mu       sync.Mutex
	dirs     map[int]string // watch descriptor -> directory
	skip     func(string) bool
	done     chan struct{}
}

const watchMask = unix.IN_CREATE | unix.IN_DELETE | unix.IN_MODIFY | unix.IN_CLOSE_WRITE |
	unix.IN_MOVED_FROM | unix.IN_MOVED_TO | unix.IN_ATTRIB | unix.IN_DELETE_SELF

// Watch watches root and every directory under it, except those skip
// says to leave out (nil: none), and those created later.
func Watch(root string, skip func(dir string) bool) (*Watcher, error) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return nil, err
	}
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_CLOEXEC|unix.O_NONBLOCK); err != nil {
		unix.Close(fd)
		return nil, err
	}
	w := &Watcher{fd: fd, wakeR: p[0], wake: p[1], dirs: map[int]string{}, skip: skip, done: make(chan struct{})}
	w.addTree(root)
	go w.loop()
	return w, nil
}

func (w *Watcher) addTree(root string) {
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if p != root && w.skip != nil && w.skip(p) {
			return filepath.SkipDir
		}
		if wd, err := unix.InotifyAddWatch(w.fd, p, watchMask); err == nil {
			w.mu.Lock()
			w.dirs[wd] = p
			w.mu.Unlock()
		}
		return nil
	})
}

func (w *Watcher) loop() {
	defer close(w.done)
	buf := make([]byte, 64*1024)
	fds := []unix.PollFd{{Fd: int32(w.fd), Events: unix.POLLIN}, {Fd: int32(w.wakeR), Events: unix.POLLIN}}
	for {
		if _, err := unix.Poll(fds, -1); err != nil {
			if err == unix.EINTR {
				continue
			}
			return
		}
		if fds[1].Revents != 0 {
			return // Close
		}
		n, err := unix.Read(w.fd, buf)
		if err != nil || n <= 0 {
			continue
		}
		for off := 0; off+unix.SizeofInotifyEvent <= n; {
			ev := (*unix.InotifyEvent)(unsafe.Pointer(&buf[off]))
			name := ""
			if ev.Len > 0 {
				b := buf[off+unix.SizeofInotifyEvent : off+unix.SizeofInotifyEvent+int(ev.Len)]
				for i, c := range b {
					if c == 0 {
						b = b[:i]
						break
					}
				}
				name = string(b)
			}
			if ev.Mask&unix.IN_ISDIR != 0 && ev.Mask&(unix.IN_CREATE|unix.IN_MOVED_TO) != 0 {
				w.mu.Lock()
				dir := w.dirs[int(ev.Wd)]
				w.mu.Unlock()
				if dir != "" {
					w.addTree(filepath.Join(dir, name)) // and anything already in it
				}
			}
			off += unix.SizeofInotifyEvent + int(ev.Len)
		}
		w.Notify()
	}
}

// Close stops watching.
func (w *Watcher) Close() {
	unix.Write(w.wake, []byte{0})
	<-w.done
	unix.Close(w.fd)
	unix.Close(w.wake)
	unix.Close(w.wakeR)
}
