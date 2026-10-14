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
//
// A claimed entry names its worker: host, pid and the process's start
// time, and a heartbeat the worker renews every third of its lease while
// it runs the batch. On the worker's own host, the process tells whether
// it lives; from another host (the queue on a shared filesystem), a
// heartbeat older than the lease means the worker is gone.
package queue

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/failpoint"
	"github.com/gates-brightly/swimlane/internal/fsutil"
	"github.com/gates-brightly/swimlane/internal/proc"
)

// Schema identifies an entry file.
const Schema = "swim.queue/v1"

// Lane is one round in a batch, pinned to the job it held when queued.
type Lane struct {
	Lane   int    `yaml:"lane"`
	Job    string `yaml:"job,omitempty"`
	Round  string `yaml:"round"`
	SHA256 string `yaml:"script_sha256,omitempty"` // the exact script queued
	// EnvSHA256 is the hash of its env from env files when queued ("" for
	// none); nil in entries from before env files.
	EnvSHA256 *string `yaml:"env_sha256,omitempty"`
	// Manifest is its execution manifest when queued (manifest_sha256): the
	// script and everything pinned with it. "" in entries from before
	// manifests, which pin the script and env hashes alone.
	Manifest string `yaml:"manifest_sha256,omitempty"`
}

// Worker is who claimed an entry.
type Worker struct {
	Host    string `yaml:"host"`
	PID     int    `yaml:"pid"`
	User    string `yaml:"user"`
	Claimed string `yaml:"claimed"`
	// Start is the worker process's start time (see the proc package): a
	// pid handed to another process since isn't the worker. "" in entries
	// from before it was recorded.
	Start string `yaml:"start,omitempty"`
	// Heartbeat is when the worker last said it was alive (RFC 3339), and
	// Lease how long that holds (a duration, 2m by default). An entry
	// without them (an older worker) is alive as far as another host can
	// tell.
	Heartbeat string `yaml:"heartbeat,omitempty"`
	Lease     string `yaml:"lease,omitempty"`
}

// DefaultLease is how long a worker's heartbeat holds when swim.yml or
// the config doesn't set queue_lease.
const DefaultLease = 2 * time.Minute

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
	Signature  string   `yaml:"signature,omitempty"` // queued by swim watch --approved: the approval's statement hash
	// The queuer's SSH signature over Statement: it proves who queued the
	// batch (standing approvals, swim approve --auto, apply to their jobs).
	QueuedKey string  `yaml:"queued_key,omitempty"`
	QueueSig  string  `yaml:"queue_sig,omitempty"`
	Worker    *Worker `yaml:"worker,omitempty"`
	Result    *Result `yaml:"result,omitempty"`

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
		if werr == nil {
			werr = f.Sync()
		}
		cerr := f.Close()
		if werr != nil {
			os.Remove(e.Path)
			return werr
		}
		if cerr != nil {
			os.Remove(e.Path)
			return cerr
		}
		fsutil.WriteAtomic(filepath.Join(Dir(root), ".seq"), []byte(strconv.Itoa(e.Seq)+"\n"), 0o644)
		return fsutil.SyncDir(Dir(root))
	}
}

// nextSeq is one past the highest sequence number ever used: the entries
// present, and the high-water mark in .seq (so a removed entry's number is
// never handed out again).
func nextSeq(root string) int {
	top := 0
	if data, err := os.ReadFile(filepath.Join(Dir(root), ".seq")); err == nil {
		top, _ = strconv.Atoi(strings.TrimSpace(string(data)))
	}
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
	return fsutil.WriteAtomic(e.Path, data, 0o644)
}

// List returns the entries waiting or running (oldest first), and with
// done the consumed ones too. An entry a worker moves on (claims or
// consumes) while it is being listed is skipped in its old place.
func List(root string, done bool) ([]*Entry, error) {
	var out []*Entry
	files, _ := filepath.Glob(filepath.Join(Dir(root), "*.queue"))
	running, _ := filepath.Glob(filepath.Join(Dir(root), "*.running"))
	for _, p := range append(files, running...) {
		e, err := load(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
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

// Busy says what the queue still has to run ("" when nothing): a running
// batch, or batches waiting for a worker. Orphaned entries never run, so
// they don't count.
func Busy(root string) (string, error) {
	entries, err := List(root, false)
	if err != nil {
		return "", err
	}
	queued := 0
	for _, e := range entries {
		switch e.State {
		case Running:
			return fmt.Sprintf("batch %s (#%d) is running (pid %d)", e.ID, e.Seq, e.Worker.PID), nil
		case Queued:
			queued++
		}
	}
	if queued > 0 {
		return fmt.Sprintf("%d queued batch(es) not yet taken by a worker (swim watch)", queued), nil
	}
	return "", nil
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

// hostname and now are variables so tests can play another host and move
// the clock.
var (
	hostname = func() string {
		h, _ := os.Hostname()
		return h
	}
	now = time.Now
)

func workerAlive(w *Worker) bool {
	if w.Host == hostname() {
		return w.PID > 0 && proc.Same(w.PID, w.Start)
	}
	// Another machine's worker: alive while its heartbeat is fresh.
	if w.Heartbeat == "" {
		return true // an older worker with no heartbeat: can't tell, assume alive
	}
	beat, err := time.Parse(time.RFC3339, w.Heartbeat)
	if err != nil {
		return true
	}
	return now().Sub(beat) <= w.LeaseFor()
}

// LeaseFor is how long the worker's heartbeat holds.
func (w *Worker) LeaseFor() time.Duration {
	if d, err := time.ParseDuration(w.Lease); err == nil && d > 0 {
		return d
	}
	return DefaultLease
}

// Remote reports whether the worker runs on another host.
func (w *Worker) Remote() bool { return w.Host != hostname() }

// Touch renews a claimed entry's heartbeat: its worker is alive. It fails
// once the entry isn't this worker's claim any more (it was requeued or
// removed from another host after the lease ran out).
func Touch(e *Entry) error {
	cur, err := load(e.Path)
	if err != nil {
		return err
	}
	if cur.Worker == nil || e.Worker == nil || cur.Worker.Host != e.Worker.Host || cur.Worker.PID != e.Worker.PID {
		return fmt.Errorf("%s isn't this worker's claim any more", e.ID)
	}
	cur.Worker.Heartbeat = now().UTC().Format(time.RFC3339)
	return write(cur)
}

// ClaimNext claims the oldest waiting entry for this process, by renaming
// it to .running; it returns nil when nothing waits. A lost race (another
// worker renamed it first) moves on to the next entry. lease is how long
// the claim's heartbeat holds (0: DefaultLease); the worker renews it with
// Touch.
func ClaimNext(root, user string, lease time.Duration) (*Entry, error) {
	files, _ := filepath.Glob(filepath.Join(Dir(root), "*.queue"))
	sort.Strings(files)
	for _, p := range files {
		e, err := claim(p, user, lease)
		if err != nil {
			return nil, err
		}
		if e != nil {
			return e, nil
		}
	}
	return nil, nil
}

// Claim claims one waiting entry, out of turn (swim watch: r on a queued
// batch). It errors if the entry isn't waiting any more: another worker
// claimed it, or it was removed.
func Claim(root string, e *Entry, user string, lease time.Duration) (*Entry, error) {
	if e.State != Queued || !strings.HasSuffix(e.Path, ".queue") {
		return nil, fmt.Errorf("%s is %s, not waiting", e.ID, e.State)
	}
	got, err := claim(e.Path, user, lease)
	if err != nil {
		return nil, err
	}
	if got == nil {
		return nil, fmt.Errorf("%s was just claimed by another worker (or removed)", e.ID)
	}
	return got, nil
}

// claim renames a waiting entry to .running and records the worker; nil
// when another worker renamed it first.
func claim(p, user string, lease time.Duration) (*Entry, error) {
	running := strings.TrimSuffix(p, ".queue") + ".running"
	if err := fsutil.Rename(p, running); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil // claimed by another worker
		}
		return nil, err
	}
	// A worker that dies here leaves a .running entry naming no worker:
	// orphaned, never run again on its own.
	failpoint.Hit(failpoint.QueueClaimed)
	e, err := load(running)
	if err != nil {
		return nil, err
	}
	e.State = Running
	if lease <= 0 {
		lease = DefaultLease
	}
	at := now().UTC().Format(time.RFC3339)
	e.Worker = &Worker{Host: hostname(), PID: os.Getpid(), User: user, Claimed: at,
		Start: proc.Start(os.Getpid()), Heartbeat: at, Lease: lease.String()}
	if err := write(e); err != nil {
		return nil, err
	}
	return e, nil
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
	if err := fsutil.Rename(e.Path, dst); err != nil {
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

// Statement is what the queuer signs: the batch's id, each lane's job and
// exact script, and the approvals it carries.
func (e *Entry) Statement() string {
	var b strings.Builder
	fmt.Fprintf(&b, "swim queue v1\nid: %s\n", e.ID)
	for _, l := range e.Lanes {
		if l.Manifest != "" {
			fmt.Fprintf(&b, "lane: %d %s %s %s\n", l.Lane, l.Job, l.SHA256, l.Manifest) // only when pinned, so older entries read the same
		} else {
			fmt.Fprintf(&b, "lane: %d %s %s\n", l.Lane, l.Job, l.SHA256)
		}
	}
	fmt.Fprintf(&b, "approve: %s\napprove-all: %v\n", strings.Join(e.Approve, ","), e.ApproveAll)
	return b.String()
}

// Has reports whether the batch includes lane n with script hash sha.
func (e *Entry) Has(n int, sha string) bool {
	for _, l := range e.Lanes {
		if l.Lane == n && l.SHA256 == sha {
			return true
		}
	}
	return false
}

// Load reads one entry file.
func Load(path string) (*Entry, error) { return load(path) }
