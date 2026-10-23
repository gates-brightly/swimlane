package hub

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gates-brightly/swimlane/internal/chain"
)

// A worker's chain checkpoints (swim's chain_anchors: hub): the hub keeps
// each worker's, append-only, and refuses one whose seq isn't higher than
// the last it has, so a worker whose chain was cut back or replaced can't
// overwrite the record of how far it had got. With a state dir they're
// kept in checkpoints/<id>.jsonl, and outlive the worker leaving.
//
//	PUT /api/workers/{id}/checkpoints   the worker (its own token): one checkpoint
//	GET /api/workers/{id}/checkpoints   the worker or the admin: all of them, oldest first

func (s *Server) checkpointsPath(id string) string {
	return filepath.Join(s.dir, "checkpoints", id+".jsonl")
}

// checkpointsOf is worker id's checkpoints, loaded from the state dir the
// first time. s.mu is held.
func (s *Server) checkpointsOf(id string) ([]chain.Checkpoint, error) {
	if cps, ok := s.checkpoints[id]; ok || s.dir == "" {
		return cps, nil
	}
	f, err := os.Open(s.checkpointsPath(id))
	if errors.Is(err, os.ErrNotExist) {
		s.checkpoints[id] = nil
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var cps []chain.Checkpoint
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var cp chain.Checkpoint
		if err := json.Unmarshal(sc.Bytes(), &cp); err != nil {
			return nil, fmt.Errorf("%s: %w", s.checkpointsPath(id), err)
		}
		cps = append(cps, cp)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	s.checkpoints[id] = cps
	return cps, nil
}

func (s *Server) checkpointsAPI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()
	got := bearer(r.Header)
	if !s.isWorker(id, got) && !(r.Method == http.MethodGet && s.isAdmin(got)) {
		http.Error(w, "needs this swimmer's own token", http.StatusUnauthorized)
		return
	}
	cps, err := s.checkpointsOf(id)
	if err != nil {
		http.Error(w, "hub state: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if r.Method == http.MethodGet {
		if cps == nil {
			cps = []chain.Checkpoint{}
		}
		json.NewEncoder(w).Encode(map[string]any{"checkpoints": cps})
		return
	}
	var cp chain.Checkpoint
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&cp); err != nil || cp.Seq < 1 || cp.Head == "" {
		http.Error(w, "a checkpoint needs {seq, head}", http.StatusBadRequest)
		return
	}
	if n := len(cps); n > 0 && cp.Seq <= cps[n-1].Seq {
		http.Error(w, fmt.Sprintf("seq %d isn't past the last checkpoint (seq %d): checkpoints only go forward", cp.Seq, cps[n-1].Seq), http.StatusConflict)
		return
	}
	if s.dir != "" {
		line, _ := json.Marshal(cp)
		os.MkdirAll(filepath.Dir(s.checkpointsPath(id)), 0o700)
		f, err := os.OpenFile(s.checkpointsPath(id), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			_, err = f.Write(append(line, '\n'))
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
		if err != nil {
			http.Error(w, "hub state: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	s.checkpoints[id] = append(cps, cp)
	w.WriteHeader(http.StatusNoContent)
}

// PutCheckpoint sends a checkpoint of this worker's chain.
func (c *Client) PutCheckpoint(cp chain.Checkpoint) error {
	resp, err := c.do("PUT", "/api/workers/"+c.ID+"/checkpoints", cp)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return failure(resp)
	}
	return nil
}

// Checkpoints are the checkpoints the hub keeps for this worker, oldest
// first.
func (c *Client) Checkpoints() ([]chain.Checkpoint, error) {
	resp, err := c.do("GET", "/api/workers/"+c.ID+"/checkpoints", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, failure(resp)
	}
	var out struct{ Checkpoints []chain.Checkpoint }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("hub: checkpoints: %w", err)
	}
	return out.Checkpoints, nil
}
