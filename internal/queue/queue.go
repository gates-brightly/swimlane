// Package queue holds batches of rounds waiting to run: `swim queue add`
// writes one entry per batch under .swim/queue/, with its lanes pinned to
// the jobs they held when queued and the approvals the operator gave up
// front; `swim watch` runs the oldest entry as soon as swim is idle and
// consumes it (moves it to .swim/queue/done/ with its result).
//
// Entries move by rename, which is atomic, so the file name is the state:
//
//	0007-q-...-ab12.queue     waiting
//	0007-q-...-ab12.running   claimed by a worker (Worker says which)
//	done/0007-q-...-ab12.yml  consumed, with Result
//
// Claiming by rename is the mutex: of two workers racing for an entry,
// exactly one rename succeeds.
package queue

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// Schema identifies an entry file.
const Schema = "swim.queue/v1"

// Lane is one round in a batch, pinned to the job it held when queued.
type Lane struct {
	Lane  int    `yaml:"lane"`
	Job   string `yaml:"job,omitempty"`
	Round string `yaml:"round"`
}

// Worker is who claimed an entry.
type Worker struct {
	Host    string `yaml:"host"`
	PID     int    `yaml:"pid"`
	User    string `yaml:"user"`
	Claimed string `yaml:"claimed"`
}

// LaneResult is how one lane of a consumed entry ended.
type LaneResult struct {
	Lane   int    `yaml:"lane"`
	Result string `yaml:"result"`
	Exit   *int   `yaml:"exit,omitempty"`
	Reason string `yaml:"reason,omitempty"`
}

// Result is filled in when the entry is consumed.
type Result struct {
	Run      string `yaml:"run,omitempty"`
	Started  string `yaml:"started"`
	Finished string `yaml:"finished"`
	Exit     int    `yaml:"exit"`
	Result   string `yaml:"result"` // passed | failed | interrupted | error
	Error    string `yaml:"error,omitempty"`
	// ApprovedBy is set when the worker approved the guards the batch
	// didn't (swim watch -y): "swim watch -y (<user>)".
	ApprovedBy string       `yaml:"approved_by,omitempty"`
	Lanes      []LaneResult `yaml:"lanes,omitempty"`
}

// Entry is one queued batch.
type Entry struct {
	Schema     string   `yaml:"schema"`
	ID         string   `yaml:"id"`
	Seq        int      `yaml:"seq"`
	Created    string   `yaml:"created"`
	By         string   `yaml:"by"`
	Note       string   `yaml:"note,omitempty"`
	Lanes      []Lane   `yaml:"lanes"`
	Approve    []string `yaml:"approve,omitempty"`     // guard flags approved for this batch
	ApproveAll bool     `yaml:"approve_all,omitempty"` // -y: every guard
	Parallel   *int     `yaml:"parallel,omitempty"`
	Worker     *Worker  `yaml:"worker,omitempty"`
	Result     *Result  `yaml:"result,omitempty"`

	// State and Path are where the entry is now; not stored.
	State string `yaml:"-"` // queued | running | orphaned | done
	Path  string `yaml:"-"`
}

// States.
const (
	Queued   = "queued"
	Running  = "running"
	Orphaned = "orphaned" // claimed, but its worker is gone
	Done     = "done"
)

// Dir and DoneDir hold the entries.
func Dir(root string) string     { return filepath.Join(root, ".swim", "queue") }
func DoneDir(root string) string { return filepath.Join(Dir(root), "done") }

// NewID is q-<utc>-<4 hex>.
func NewID() string {
	var b [2]byte
	rand.Read(b[:])
	return fmt.Sprintf("q-%s-%x", time.Now().UTC().Format("20060102T150405Z"), b)
}

func base(e *Entry) string { return fmt.Sprintf("%04d-%s", e.Seq, e.ID) }

// Add writes a new entry at the back of the queue.
func Add(root string, e *Entry) error {
	if err := os.MkdirAll(Dir(root), 0o755); err != nil {
		return err
	}
	e.Schema = Schema
	if e.ID == "" {
		e.ID = NewID()
	}
	if e.Created == "" {
		e.Created = time.Now().UTC().Format(time.RFC3339)
	}
	for {
		e.Seq = nextSeq(root)
		e.State, e.Path = Queued, filepath.Join(Dir(root), base(e)+".queue")
		data, err := yaml.Marshal(e)
		if err != nil {
			return err
		}
		// O_EXCL: two adds racing for a sequence number never overwrite.
		f, err := os.OpenFile(e.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		_, werr := f.Write(data)
		cerr := f.Close()
		if werr != nil {
			return werr
		}
		return cerr
	}
}

func nextSeq(root string) int {
	top := 0
	for _, dir := range []string{Dir(root), DoneDir(root)} {
		files, _ := os.ReadDir(dir)
		for _, f := range files {
			if n, err := strconv.Atoi(strings.SplitN(f.Name(), "-", 2)[0]); err == nil && n > top {
				top = n
			}
		}
	}
	return top + 1
}

func load(path string) (*Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var e Entry
	if err := yaml.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	e.Path = path
	return &e, nil
}

func write(e *Entry) error {
	data, err := yaml.Marshal(e)
	if err != nil {
		return err
	}
	tmp := e.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, e.Path)
}

// List returns the entries waiting or running (oldest first), and with
// done the consumed ones too.
func List(root string, done bool) ([]*Entry, error) {
	var out []*Entry
	files, _ := filepath.Glob(filepath.Join(Dir(root), "*.queue"))
	running, _ := filepath.Glob(filepath.Join(Dir(root), "*.running"))
	for _, p := range append(files, running...) {
		e, err := load(p)
		if err != nil {
			return nil, err
		}
		e.State = Queued
		if strings.HasSuffix(p, ".running") {
			e.State = Running
			if e.Worker == nil || !workerAlive(e.Worker) {
				e.State = Orphaned
			}
		}
		out = append(out, e)
	}
	if done {
		files, _ := filepath.Glob(filepath.Join(DoneDir(root), "*.yml"))
		for _, p := range files {
			e, err := load(p)
			if err != nil {
				return nil, err
			}
			e.State = Done
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Seq < out[b].Seq })
	return out, nil
}

// Find returns the entry whose id (or seq, or id prefix) matches ref.
func Find(root, ref string) (*Entry, error) {
	all, err := List(root, true)
	if err != nil {
		return nil, err
	}
	var hits []*Entry
	for _, e := range all {
		if e.ID == ref || strings.HasPrefix(e.ID, ref) && len(ref) >= 4 || strconv.Itoa(e.Seq) == strings.TrimLeft(ref, "0") {
			hits = append(hits, e)
		}
	}
	switch len(hits) {
	case 0:
		return nil, fmt.Errorf("no queue entry %q (swim queue lists them)", ref)
	case 1:
		return hits[0], nil
	}
	return nil, fmt.Errorf("%q matches %d queue entries; give more of the id", ref, len(hits))
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

func workerAlive(w *Worker) bool {
	if w.Host != hostname() {
		return true // another machine's worker: can't tell, assume alive
	}
	if w.PID <= 0 {
		return false
	}
	err := syscall.Kill(w.PID, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// ClaimNext claims the oldest waiting entry for this process, by renaming
// it to .running; it returns nil when nothing waits. A lost race (another
// worker renamed it first) moves on to the next entry.
func ClaimNext(root, user string) (*Entry, error) {
	files, _ := filepath.Glob(filepath.Join(Dir(root), "*.queue"))
	sort.Strings(files)
	for _, p := range files {
		running := strings.TrimSuffix(p, ".queue") + ".running"
		if err := os.Rename(p, running); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue // claimed by another worker
			}
			return nil, err
		}
		e, err := load(running)
		if err != nil {
			return nil, err
		}
		e.State = Running
		e.Worker = &Worker{Host: hostname(), PID: os.Getpid(), User: user, Claimed: time.Now().UTC().Format(time.RFC3339)}
		if err := write(e); err != nil {
			return nil, err
		}
		return e, nil
	}
	return nil, nil
}

// Consume records the result and moves a claimed entry to done/.
func Consume(root string, e *Entry, r Result) error {
	e.Result = &r
	if err := write(e); err != nil {
		return err
	}
	if err := os.MkdirAll(DoneDir(root), 0o755); err != nil {
		return err
	}
	dst := filepath.Join(DoneDir(root), base(e)+".yml")
	if err := os.Rename(e.Path, dst); err != nil {
		return err
	}
	e.Path, e.State = dst, Done
	return nil
}

// Requeue puts an orphaned or done entry back at the back of the queue (a
// new sequence number), its worker and result cleared.
func Requeue(root string, e *Entry) error {
	if e.State == Running {
		return fmt.Errorf("%s is running (worker pid %d on %s); stop that worker first", e.ID, e.Worker.PID, e.Worker.Host)
	}
	old := e.Path
	e.Worker, e.Result = nil, nil
	if err := Add(root, e); err != nil {
		return err
	}
	if old != e.Path {
		return os.Remove(old)
	}
	return nil
}

// Remove deletes a waiting, orphaned or done entry.
func Remove(e *Entry) error {
	if e.State == Running {
		return fmt.Errorf("%s is running (worker pid %d on %s); stop that worker first", e.ID, e.Worker.PID, e.Worker.Host)
	}
	return os.Remove(e.Path)
}

// LockWorker takes the repo's worker lock without waiting: one worker runs
// a batch at a time (lanes share the repo's files). The lock is released by
// the returned func, or when the process dies. ok is false if another
// worker holds it.
func LockWorker(root string) (unlock func(), ok bool, err error) {
	if err := os.MkdirAll(Dir(root), 0o755); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(filepath.Join(Dir(root), ".worker.lock"), os.O_CREATE|os.O_RDWR, 0o644)
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
	f.Truncate(0)
	fmt.Fprintf(f, "%s pid %d\n", hostname(), os.Getpid())
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, true, nil
}
