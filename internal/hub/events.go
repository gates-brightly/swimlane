package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Events (GET /api/events, server-sent events): what changed on the hub,
// as it happens, so a page updates without polling.
//
//	snapshot   a worker reported: its View, as GET /api/enrollments/{id} gives it
//	run        a worker sent a finished run: its RunInfo
//	log        a log reached the hub, or grew: {enrollment, run, lane, bytes, final}
//	command    a command was sent or acknowledged: {enrollment, cid}
//
// ?enrollment=ID keeps one worker's. A client that falls behind is
// dropped rather than slowing a report down: it reconnects (the stream
// says retry after 2s) and reads the state again. Nothing is replayed,
// so a client reads the state when it (re)connects; each event's id only
// orders them. A comment every EventHeartbeat keeps proxies from closing
// a quiet stream.

// EventHeartbeat is how often a quiet event stream gets a comment.
const EventHeartbeat = 15 * time.Second

// eventBuffer is how many events a client may fall behind by.
const eventBuffer = 64

// Event is one change.
type Event struct {
	ID         int64
	Kind       string // snapshot | run | log
	Enrollment string
	Data       any
}

// LogEvent says a log reached the hub.
type LogEvent struct {
	Enrollment string `json:"enrollment"`
	Run        string `json:"run"`
	Lane       int    `json:"lane"`
	Bytes      int    `json:"bytes"`
	Final      bool   `json:"final,omitempty"` // a streamed round's end
}

type eventSub struct {
	ch         chan Event
	enrollment string // "" for every swimmer
	user       string // the hub user who opened it ("" for none)
}

// MaxEventStreams is how many event streams may be open at once.
const MaxEventStreams = 64

type eventHub struct {
	mu     sync.Mutex
	next   int64
	subs   map[*eventSub]struct{}
	closed bool
	// heartbeat is EventHeartbeat; a test sets it before serving (not a
	// package var: a stream still open after the test would race its reset).
	heartbeat time.Duration
}

func newEventHub() *eventHub {
	return &eventHub{subs: map[*eventSub]struct{}{}, heartbeat: EventHeartbeat}
}

// publish sends an event to each client that wants it, dropping any that
// can't take it now.
func (e *eventHub) publish(kind, enrollment string, data any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.next++
	ev := Event{ID: e.next, Kind: kind, Enrollment: enrollment, Data: data}
	for s := range e.subs {
		if s.enrollment != "" && s.enrollment != enrollment {
			continue
		}
		select {
		case s.ch <- ev:
		default:
			delete(e.subs, s)
			close(s.ch)
		}
	}
}

// subscribe is a new client for user (nil once the hub is shutting down,
// or MaxEventStreams are open).
func (e *eventHub) subscribe(enrollment, user string) *eventSub {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || len(e.subs) >= MaxEventStreams {
		return nil
	}
	s := &eventSub{ch: make(chan Event, eventBuffer), enrollment: enrollment, user: user}
	e.subs[s] = struct{}{}
	return s
}

func (e *eventHub) unsubscribe(s *eventSub) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.subs[s]; ok {
		delete(e.subs, s)
		close(s.ch)
	}
}

// closeUser ends every stream user opened: their sessions ended (removed,
// or given a new token).
func (e *eventHub) closeUser(user string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for s := range e.subs {
		if s.user == user {
			delete(e.subs, s)
			close(s.ch)
		}
	}
}

// clients is how many streams are open.
func (e *eventHub) clients() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.subs)
}

// CloseEvents ends every event stream (the hub is shutting down: an open
// stream would hold http.Server.Shutdown until its deadline).
func (s *Server) CloseEvents() {
	s.events.mu.Lock()
	defer s.events.mu.Unlock()
	s.events.closed = true
	for sub := range s.events.subs {
		delete(s.events.subs, sub)
		close(sub.ch)
	}
}

func (s *Server) eventsAPI(w http.ResponseWriter, r *http.Request) {
	if !s.canView(r) {
		http.Error(w, "this hub is private: log in first (/login), or send a user's token", http.StatusUnauthorized)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	sub := s.events.subscribe(r.URL.Query().Get("enrollment"), s.streamUser(r))
	if sub == nil {
		http.Error(w, "the hub is shutting down, or too many event streams are open", http.StatusServiceUnavailable)
		return
	}
	defer s.events.unsubscribe(sub)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // nginx: don't buffer the stream
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 2000\n\n")
	fl.Flush()
	beat := time.NewTicker(s.events.heartbeat)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-sub.ch:
			if !ok {
				return // fell behind, or the hub is shutting down
			}
			data, err := json.Marshal(ev.Data)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.ID, ev.Kind, data)
			fl.Flush()
		case <-beat.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			fl.Flush()
		}
	}
}

// streamUser is the hub user r comes from, by token or session ("" for none).
func (s *Server) streamUser(r *http.Request) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u := s.userByToken(bearer(r.Header)); u != nil {
		return u.Name
	}
	if _, u := s.sessionOf(r); u != nil {
		return u.Name
	}
	return ""
}
