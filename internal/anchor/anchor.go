// Package anchor sends chain checkpoints (and exported entries) to
// somewhere a lane and the runner key can't rewrite, and reads them back
// for swim verify. A destination is named by a spec, as in chain_anchors:
//
//	file:/mnt/audit/swim/orders   checkpoints.jsonl and chain.jsonl in that
//	                              directory, appended to, never rewritten
//
// An anchor only helps if what's written there can't be taken back by
// whoever controls the repo: another volume mounted append-only, or held
// by someone else.
package anchor

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/gates-brightly/swimlane/internal/chain"
)

// Anchor is one destination.
type Anchor interface {
	Name() string // its spec
	// PutCheckpoint adds cp; one that doesn't go past the last is refused
	// (the same checkpoint again is a no-op).
	PutCheckpoint(cp chain.Checkpoint) error
	// Latest is the newest checkpoint (nil when there is none).
	Latest() (*chain.Checkpoint, error)
	// All is every checkpoint, oldest first: swim verify checks each one,
	// so a chain rewritten behind an old checkpoint fails whatever new
	// checkpoints were sent after it.
	All() ([]chain.Checkpoint, error)
	// Entries are the chain entries exported to it (ErrNoEntries if it
	// keeps none).
	Entries() ([]chain.Link, error)
	// AppendEntries adds lines after the first have entries it holds,
	// refusing if it holds another number by then.
	AppendEntries(have int, lines [][]byte) error
}

// ErrNoEntries: the anchor keeps checkpoints only.
var ErrNoEntries = errors.New("keeps checkpoints only, not entries")

// ErrBehind is a checkpoint that doesn't go past the anchor's last.
var ErrBehind = errors.New("not past the last checkpoint there")

// Options is what opening an anchor may need.
type Options struct {
	Root string // the repo: a file anchor inside it is refused
}

// Open opens the anchor spec names.
func Open(spec string, o Options) (Anchor, error) {
	spec = strings.TrimSpace(spec)
	switch {
	case strings.HasPrefix(spec, "file:"):
		dir := strings.TrimPrefix(spec, "file:")
		if strings.HasPrefix(dir, "~/") {
			home, _ := os.UserHomeDir()
			dir = filepath.Join(home, dir[2:])
		}
		if !filepath.IsAbs(dir) {
			return nil, fmt.Errorf("anchor %s: needs an absolute path (file:/path/outside/the/repo)", spec)
		}
		if o.Root != "" {
			if rel, err := filepath.Rel(o.Root, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return nil, fmt.Errorf("anchor %s: inside the repo, where a lane can rewrite it", spec)
			}
		}
		return &fileAnchor{spec: spec, dir: dir}, nil
	case spec == "hub":
		return nil, errors.New("anchor hub: swim no longer has a hub; use file:/path (a directory outside the repo)")
	}
	return nil, fmt.Errorf("unknown anchor %q: use file:/path", spec)
}

// fileAnchor is a directory: checkpoints.jsonl and chain.jsonl, each
// opened for appending only and locked while written.
type fileAnchor struct {
	spec, dir string
}

func (a *fileAnchor) Name() string      { return a.spec }
func (a *fileAnchor) cpPath() string    { return filepath.Join(a.dir, "checkpoints.jsonl") }
func (a *fileAnchor) chainPath() string { return filepath.Join(a.dir, "chain.jsonl") }
func (a *fileAnchor) mkdir() error      { return os.MkdirAll(a.dir, 0o755) }
func (a *fileAnchor) lockedAppend(path string, write func(f *os.File) error) error {
	if err := a.mkdir(); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	if err := write(f); err != nil {
		return err
	}
	return f.Sync()
}

func (a *fileAnchor) PutCheckpoint(cp chain.Checkpoint) error {
	return a.lockedAppend(a.cpPath(), func(f *os.File) error {
		cps, err := readCheckpoints(f)
		if err != nil {
			return err
		}
		if n := len(cps); n > 0 {
			last := cps[n-1]
			if cp.Seq == last.Seq && cp.Head == last.Head {
				return nil
			}
			if cp.Seq <= last.Seq {
				return fmt.Errorf("seq %d: %w (seq %d)", cp.Seq, ErrBehind, last.Seq)
			}
		}
		line, err := json.Marshal(cp)
		if err != nil {
			return err
		}
		_, err = f.Write(append(line, '\n'))
		return err
	})
}

func (a *fileAnchor) All() ([]chain.Checkpoint, error) {
	f, err := os.Open(a.cpPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readCheckpoints(f)
}

func (a *fileAnchor) Latest() (*chain.Checkpoint, error) {
	f, err := os.Open(a.cpPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cps, err := readCheckpoints(f)
	if err != nil || len(cps) == 0 {
		return nil, err
	}
	return &cps[len(cps)-1], nil
}

// readCheckpoints reads every checkpoint in f from its start.
func readCheckpoints(f *os.File) ([]chain.Checkpoint, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	var out []chain.Checkpoint
	rd := bufio.NewReader(f)
	for n := 1; ; n++ {
		line, err := rd.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var cp chain.Checkpoint
			if jerr := json.Unmarshal(line, &cp); jerr != nil {
				return nil, fmt.Errorf("%s line %d: %v", f.Name(), n, jerr)
			}
			out = append(out, cp)
		}
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func (a *fileAnchor) Entries() ([]chain.Link, error) { return chain.ReadFile(a.chainPath()) }

func (a *fileAnchor) AppendEntries(have int, lines [][]byte) error {
	return a.lockedAppend(a.chainPath(), func(f *os.File) error {
		n, err := countLines(f)
		if err != nil {
			return err
		}
		if n != have {
			return fmt.Errorf("%s holds %d entries now, not %d: another export ran; try again", a.chainPath(), n, have)
		}
		var b bytes.Buffer
		for _, l := range lines {
			b.Write(l)
			b.WriteByte('\n')
		}
		_, err = f.Write(b.Bytes())
		return err
	})
}

func countLines(f *os.File) (int, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	n := 0
	rd := bufio.NewReader(f)
	for {
		line, err := rd.ReadBytes('\n')
		if len(line) > 0 {
			n++
		}
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return 0, err
		}
	}
}

// Export appends the local chain's entries that a doesn't hold yet. What
// it holds must be the local chain's first entries, line for line: if
// not, the local chain was cut back or replaced since, and nothing is
// written. It returns how many entries were added.
func Export(a Anchor, chainPath string) (int, error) {
	theirs, err := a.Entries()
	if err != nil {
		return 0, fmt.Errorf("%s: %w", a.Name(), err)
	}
	lines, err := rawLines(chainPath)
	if err != nil {
		return 0, err
	}
	if err := Diverged(theirs, lines); err != nil {
		return 0, fmt.Errorf("%s: %w; nothing exported", a.Name(), err)
	}
	if len(lines) == len(theirs) {
		return 0, nil
	}
	return len(lines) - len(theirs), a.AppendEntries(len(theirs), lines[len(theirs):])
}

// Diverged says how exported entries differ from the local chain's lines
// (nil when they are its first entries).
func Diverged(exported []chain.Link, local [][]byte) error {
	for i, l := range exported {
		if i >= len(local) {
			return fmt.Errorf("it holds %d entries, the local chain only %d: the local chain lost entries from its end", len(exported), len(local))
		}
		if chain.Hash(local[i]) != l.Hash {
			return fmt.Errorf("entry #%d there isn't the local entry #%d: the local chain was replaced from there", l.Seq, i+1)
		}
	}
	return nil
}

// rawLines are a chain file's lines as written (no newlines).
func rawLines(path string) ([][]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	if data[len(data)-1] != '\n' {
		return nil, fmt.Errorf("%s: the last entry is incomplete; swim chain repair removes it", path)
	}
	var out [][]byte
	for _, l := range bytes.Split(data[:len(data)-1], []byte("\n")) {
		out = append(out, bytes.TrimSuffix(l, []byte("\r")))
	}
	return out, nil
}

// LocalLines is rawLines for the repo's chain, for Diverged.
func LocalLines(root string) ([][]byte, error) { return rawLines(chain.Path(root)) }
