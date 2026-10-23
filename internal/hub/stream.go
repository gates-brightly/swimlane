package hub

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/gates-brightly/swimlane/internal/chars"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// Streamed logs (PUT /api/workers/{id}/stream/{run}/{lane}): a worker in a
// repo whose swim.yml says hub.logs: stream sends each round's log as it
// grows, complete lines at a time, masked. The hub appends it to
// runs/<worker>/<run>.lane.N.log (sanitized line by line), beside the
// run's report, and drops it with the run (runs.go retention).
//
// ?offset=N is how many bytes the worker sent before this request; the
// hub checks it against what it received (before sanitizing, which can
// change lengths) and answers 409, with its count in StreamOffsetHeader,
// when they differ. offset=0 replaces the log: a worker that restarted,
// or whose masking changed what it had sent, sends it all again.
// &final=1 marks the round's end. Each append is a log event.

// StreamOffsetHeader carries the hub's count on a 409.
const StreamOffsetHeader = "Swim-Log-Offset"

type streamState struct {
	mu       sync.Mutex
	received map[string]int // log path -> bytes received (not stored: sanitizing changes lengths)
}

func (rs *runStore) streamPath(worker, run string, lane int) string {
	return filepath.Join(rs.dir, worker, fmt.Sprintf("%s.lane.%d.log", run, lane))
}

func (s *Server) putStream(w http.ResponseWriter, r *http.Request) {
	id, run := r.PathValue("id"), r.PathValue("run")
	lane, err := strconv.Atoi(r.PathValue("lane"))
	s.mu.Lock()
	ok := s.isWorker(id, bearer(r.Header))
	s.mu.Unlock()
	switch {
	case !ok:
		http.Error(w, "needs this swimmer's own token", http.StatusUnauthorized)
		return
	case !chars.Name(run, 128) || err != nil || lane < 1:
		http.Error(w, "a run id and a lane number", http.StatusBadRequest)
		return
	}
	offset, err := strconv.Atoi(r.URL.Query().Get("offset"))
	if err != nil || offset < 0 {
		http.Error(w, "?offset=N: the bytes sent before this", http.StatusBadRequest)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, LogMax))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "a streamed log is at most 8 MiB", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.runs.mu.Lock()
	dir := s.runs.dir
	s.runs.mu.Unlock()
	if dir == "" {
		http.Error(w, errNoState.Error(), http.StatusServiceUnavailable)
		return
	}
	path := s.runs.streamPath(id, run, lane)
	st := &s.streams
	st.mu.Lock()
	defer st.mu.Unlock()
	have := st.received[path]
	if offset != 0 && offset != have {
		w.Header().Set(StreamOffsetHeader, strconv.Itoa(have))
		http.Error(w, fmt.Sprintf("the hub has %d bytes of this log, not %d: send it from 0", have, offset), http.StatusConflict)
		return
	}
	if offset+len(data) > LogMax {
		http.Error(w, "a streamed log is at most 8 MiB", http.StatusRequestEntityTooLarge)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if offset == 0 {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, werr := f.Write([]byte(ui.SafeLines(string(data))))
	fi, _ := f.Stat()
	f.Close()
	if werr != nil {
		http.Error(w, werr.Error(), http.StatusInternalServerError)
		return
	}
	st.received[path] = offset + len(data)
	size := 0
	if fi != nil {
		size = int(fi.Size())
	}
	s.events.publish("log", id, LogEvent{Enrollment: id, Run: run, Lane: lane, Bytes: size, Final: r.URL.Query().Get("final") == "1"})
	w.WriteHeader(http.StatusNoContent)
}

// streamedLog opens worker id's streamed log of a lane in run, if there is one.
func (s *Server) streamedLog(id, run string, lane int) (*os.File, os.FileInfo, bool) {
	s.runs.mu.Lock()
	dir := s.runs.dir
	s.runs.mu.Unlock()
	if dir == "" || !chars.Name(run, 128) || !chars.Name(id, 64) {
		return nil, nil, false
	}
	f, err := os.Open(s.runs.streamPath(id, run, lane))
	if err != nil {
		return nil, nil, false
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, false
	}
	return f, fi, true
}
