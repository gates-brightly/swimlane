package hub

import (
	"container/list"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gates-brightly/swimlane/internal/ui"
)

// Logs a worker uploads because a user asked (a log command). The hub
// keeps them in memory only, bounded: LogMax per log, LogCacheMax in all
// (least recently read go first), each for LogTTL. A restart forgets them;
// asking again uploads the log again.

// Log cache bounds.
const (
	LogMax      = 8 << 20
	LogCacheMax = 64 << 20
	LogTTL      = 15 * time.Minute
)

type logCache struct {
	mu       sync.Mutex
	per, max int
	ttl      time.Duration
	now      func() time.Time
	order    *list.List // most recently used first
	items    map[string]*list.Element
	total    int
}

type cachedLog struct {
	key  string
	data []byte
	at   time.Time // uploaded
}

func newLogCache(per, max int, ttl time.Duration, now func() time.Time) *logCache {
	return &logCache{per: per, max: max, ttl: ttl, now: now, order: list.New(), items: map[string]*list.Element{}}
}

func (c *logCache) remove(e *list.Element) {
	l := e.Value.(*cachedLog)
	c.total -= len(l.data)
	delete(c.items, l.key)
	c.order.Remove(e)
}

// put keeps data under key, evicting expired logs and then the least
// recently used until it fits; false if data alone is over the limit.
func (c *logCache) put(key string, data []byte) bool {
	if len(data) > c.per || len(data) > c.max {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[key]; ok {
		c.remove(e)
	}
	now := c.now()
	for e := c.order.Back(); e != nil; {
		prev := e.Prev()
		if now.Sub(e.Value.(*cachedLog).at) > c.ttl {
			c.remove(e)
		}
		e = prev
	}
	for c.total+len(data) > c.max {
		c.remove(c.order.Back())
	}
	c.items[key] = c.order.PushFront(&cachedLog{key: key, data: data, at: now})
	c.total += len(data)
	return true
}

// get is the log under key while it's fresh, and when it was uploaded.
func (c *logCache) get(key string) ([]byte, time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok {
		return nil, time.Time{}, false
	}
	l := e.Value.(*cachedLog)
	if c.now().Sub(l.at) > c.ttl {
		c.remove(e)
		return nil, time.Time{}, false
	}
	c.order.MoveToFront(e)
	return l.data, l.at, true
}

// size is how many bytes the cache holds.
func (c *logCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}

func logKey(id, run string, lane int) string { return id + "\x00" + run + "\x00" + strconv.Itoa(lane) }

// asked reports whether a user asked worker id for this log. s.mu is held.
func (s *Server) asked(id, run string, lane int) bool {
	for _, c := range s.commands[id] {
		if c.Kind == KindLog && c.Pin.Run == run && c.Lane == lane {
			return true
		}
	}
	return false
}

// putLog takes a log the worker uploads: only one a user asked for, at
// most LogMax, made safe to show line by line.
func (s *Server) putLog(w http.ResponseWriter, r *http.Request) {
	id, run := r.PathValue("id"), r.PathValue("run")
	lane, _ := strconv.Atoi(r.PathValue("lane"))
	s.mu.Lock()
	ok, asked := s.isWorker(id, bearer(r.Header)), s.asked(id, run, lane)
	s.mu.Unlock()
	switch {
	case !ok:
		http.Error(w, "needs this worker's own token", http.StatusUnauthorized)
		return
	case !asked:
		http.Error(w, "nobody asked for that log", http.StatusConflict)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, LogMax))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "a log is at most 8 MiB: send its end", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	clean := []byte(ui.SafeLines(string(data)))
	if !s.logs.put(logKey(id, run, lane), clean) {
		http.Error(w, "a log is at most 8 MiB: send its end", http.StatusRequestEntityTooLarge)
		return
	}
	s.mu.Lock()
	s.audit("%s uploaded the log of swim %d run %s (%d bytes)", id, lane, run, len(clean))
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getLog(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireUser(w, r, RoleViewer); !ok {
		return
	}
	lane, _ := strconv.Atoi(r.PathValue("lane"))
	data, _, ok := s.logs.get(logKey(r.PathValue("id"), r.PathValue("run"), lane))
	if !ok {
		http.Error(w, "not uploaded (yet): send a log command", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(data)
}
