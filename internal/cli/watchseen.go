package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gates-brightly/swimlane/internal/fsutil"
	"github.com/gates-brightly/swimlane/internal/lane"
)

// watchSeen remembers, per lane, the script the operator last acted on in
// swim watch (r, q, enter, l, a, or answering its question): its hash, as
// approvals and queue pins take it (lane.ScriptBytes, so a changed
// # Includes: file counts). A lane whose script has changed since, or
// that appeared since, gets (~) on the screen until the operator acts on
// it again. It's kept in .swim/watch/seen.json, so a restarted swim watch
// still knows; the first swim watch in a repo takes every lane as seen.
// A nil watchSeen marks nothing.
type watchSeen struct {
	root    string
	seen    map[int]string // lane → script hash when last acted on
	known   bool           // seen.json existed: a lane missing from it is new
	changed map[int]bool
	checked time.Time // hashing reads every script (and its includes): at most once a second
}

// watchSeenEvery is how often the screen hashes the lanes' scripts again.
const watchSeenEvery = time.Second

func watchSeenPath(root string) string { return filepath.Join(root, ".swim", "watch", "seen.json") }

// seenFile is seen.json: {"lanes": {"3": "<sha256>"}}.
type seenFile struct {
	Lanes map[string]string `json:"lanes"`
}

func loadWatchSeen(root string) *watchSeen {
	s := &watchSeen{root: root, seen: map[int]string{}, changed: map[int]bool{}}
	data, err := os.ReadFile(watchSeenPath(root))
	if err != nil {
		return s
	}
	var f seenFile
	if json.Unmarshal(data, &f) != nil {
		return s
	}
	s.known = true
	for k, h := range f.Lanes {
		if n, err := strconv.Atoi(k); err == nil {
			s.seen[n] = h
		}
	}
	return s
}

func (s *watchSeen) save() {
	f := seenFile{Lanes: map[string]string{}}
	for n, h := range s.seen {
		f.Lanes[strconv.Itoa(n)] = h
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(watchSeenPath(s.root)), 0o755)
	if fsutil.WriteAtomic(watchSeenPath(s.root), append(data, '\n'), 0o644) == nil {
		s.known = true
	}
}

// scriptHash is lane n's script hash, "" if it has none.
func scriptHash(root string, n int) string {
	data, err := lane.ScriptBytes(root, n)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// refresh hashes lanes' scripts again (at most once a second): the first
// swim watch in a repo records them as seen; after that, one not seen
// before, or changed since, is marked.
func (s *watchSeen) refresh(lanes []int) {
	if s == nil || time.Since(s.checked) < watchSeenEvery {
		return
	}
	s.checked = time.Now()
	dirty := false
	for _, n := range lanes {
		h := scriptHash(s.root, n)
		if h == "" {
			delete(s.changed, n)
			continue
		}
		old, ok := s.seen[n]
		switch {
		case !ok && !s.known:
			s.seen[n] = h
			dirty = true
		case !ok:
			s.changed[n] = true
		default:
			s.changed[n] = old != h
		}
	}
	if dirty {
		s.save()
	}
}

// isChanged reports whether lane n gets (~).
func (s *watchSeen) isChanged(n int) bool { return s != nil && s.changed[n] }

// touch records that the operator acted on lanes: their scripts as they
// are now are seen.
func (s *watchSeen) touch(lanes ...int) {
	if s == nil {
		return
	}
	dirty := false
	for _, n := range lanes {
		if n <= 0 {
			continue
		}
		if h := scriptHash(s.root, n); h != "" {
			s.seen[n] = h
			dirty = true
		}
		delete(s.changed, n)
	}
	if dirty {
		s.save()
	}
}
