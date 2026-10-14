// Package chain keeps swim's chain of custody: an append-only file of
// entries, one JSON object per line, each holding the SHA-256 of the line
// before it and an SSH signature by whoever recorded it. Changing,
// removing or reordering any entry breaks every link after it, and a
// forged entry fails its signature, so tampering is provable (swim verify).
//
// Entries are approvals (who approved which guard of which exact script,
// and how), rounds (what ran: the script's hash, the hash of the round's
// log, the result, the approvals it used), recorded as they happen, and
// runs (swim run's attestation of what its lanes ran and the approvals it
// recorded).
package chain

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gates-brightly/swimlane/internal/sign"
)

// Approver is one person behind an approval: their key, and for a remote
// signature (.signature file) their own signature over the approval.
type Approver struct {
	Name string `json:"name,omitempty"` // from lookup.yml when recorded
	Key  string `json:"key"`
	// For a signed approval (approvals/*.signature): the statement and the
	// approving event (its prev, time and signature), so swim verify can
	// check it without the file.
	Statement string `json:"statement,omitempty"`
	Prev      string `json:"prev,omitempty"`
	SignedAt  string `json:"signed_at,omitempty"`
	Sig       string `json:"sig,omitempty"`
}

// Entry is one link.
type Entry struct {
	Seq  int    `json:"seq"`
	Prev string `json:"prev"` // SHA-256 of the previous line; "" for the first
	At   string `json:"at"`
	Type string `json:"type"` // approve | round | run | grant | revoke | repair | hub (a command from the hub; Method: its kind)

	Run    string `json:"run,omitempty"`
	Lane   int    `json:"lane,omitempty"`
	Job    string `json:"job,omitempty"`
	Round  string `json:"round,omitempty"`
	Script string `json:"script,omitempty"` // SHA-256 of the lane script
	// Manifest is the round's execution manifest hash (manifest_sha256):
	// the script and everything else pinned with it (internal/manifest).
	Manifest string `json:"manifest_sha256,omitempty"`

	// approve
	Guard     string     `json:"guard,omitempty"`
	Action    string     `json:"action,omitempty"`    // the guard's description
	Method    string     `json:"method,omitempty"`    // signature | prompt | flag | auto | queue
	Statement string     `json:"statement,omitempty"` // what a signature's approvers signed
	Approvers []Approver `json:"approvers,omitempty"`
	// Lookup is the SHA-256 of lookup.yml when the guard was approved, so
	// swim verify knows whether today's rules are the ones it met.
	Lookup string `json:"lookup_sha256,omitempty"`
	// Observed are the round's Observe: facts when the guard was approved.
	Observed map[string]string `json:"observed,omitempty"`

	// round
	Result    string   `json:"result,omitempty"`
	Exit      *int     `json:"exit,omitempty"`
	Log       string   `json:"log,omitempty"`       // SHA-256 of the round's section of the log
	Approvals []string `json:"approvals,omitempty"` // hashes of the approve entries it used
	// AutoApproved are the guards among them approved by -y, with no one
	// asked: on the round itself, so it shows without following the hashes.
	AutoApproved []string `json:"auto_approved,omitempty" yaml:"auto_approved,omitempty"`
	EnvSHA256    string   `json:"env_sha256,omitempty"` // the lane's env from env files (sorted KEY=VALUE)
	// Handlers are the round's outcome handlers that ran: name -> PASS | FAIL | SKIP | none.
	Handlers map[string]string `json:"handlers,omitempty"`
	// Outputs are the files (and values) the round produced: name -> SHA-256.
	Outputs map[string]string `json:"outputs,omitempty"`
	// Inputs are the outputs it read: "2:stats.json" -> SHA-256 when read,
	// and which round produced each: "2" -> "<job> <run>".
	Inputs map[string]string `json:"inputs,omitempty"`
	// Evidence is the round's evidence hash (round entries), or the
	// evidence an approval covered (approve entries).
	Evidence    string            `json:"evidence_sha256,omitempty"`
	InputRounds map[string]string `json:"input_rounds,omitempty"`

	// run: swim run's attestation, recorded once its last lane exited. The
	// manifest and script each lane ran ("3" -> SHA-256), Approvals (above)
	// every approve entry the launcher recorded, and Flags the guard flags
	// approved by swim run's own environment.
	Manifests map[string]string `json:"manifests,omitempty"`
	Scripts   map[string]string `json:"scripts,omitempty"`
	Flags     []string          `json:"flags,omitempty"`

	Signer string `json:"signer,omitempty"` // public key of whoever recorded the entry
	Sig    string `json:"sig,omitempty"`
}

// Path is the chain file.
func Path(root string) string { return filepath.Join(root, ".swim", "chain.jsonl") }

// Hash is the SHA-256 (hex) of b.
func Hash(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// HashFile is the SHA-256 of a file's content.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// payload is what the recorder signs: the entry without its signature.
func payload(e Entry) []byte {
	e.Sig = ""
	b, _ := json.Marshal(e)
	return b
}

// Append links e to the end of the chain, signs it with key (nil: an
// unsigned entry, still linked) and returns the new line's hash.
func Append(root string, e Entry, key *sign.Key) (string, error) {
	f, err := openLocked(root)
	if err != nil {
		return "", err
	}
	defer f.Close()
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	line, err := link(f, st.Size(), e, key)
	if err != nil {
		return "", err
	}
	if err := writeLine(f, st.Size(), line); err != nil {
		return "", err
	}
	return Hash(line), nil
}

// openLocked opens the chain file (creating it) and takes its exclusive
// lock; the caller unlocks and closes it.
func openLocked(root string) (*os.File, error) {
	path := Path(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// link fills in e's seq, prev hash, time and signature as the entry after
// the chain's first end bytes, and returns its line (without the newline).
func link(f *os.File, end int64, e Entry, key *sign.Key) ([]byte, error) {
	last, seq, err := lastLine(f, end)
	if err != nil {
		return nil, err
	}
	e.Seq, e.Prev = seq+1, ""
	if last != nil {
		e.Prev = Hash(last)
	}
	if e.At == "" {
		e.At = time.Now().UTC().Format(time.RFC3339)
	}
	e.Signer, e.Sig = "", ""
	if key != nil {
		e.Signer = key.Public
		sig, err := sign.Sign(key, payload(e))
		if err != nil {
			return nil, err
		}
		e.Sig = sig
	}
	return json.Marshal(e)
}

// writeLine writes line and its newline at offset at and syncs, so a
// recorded entry survives a crash right after it.
func writeLine(f *os.File, at int64, line []byte) error {
	if _, err := f.WriteAt(append(line, '\n'), at); err != nil {
		return err
	}
	return f.Sync()
}

// lastLine returns the last line of the chain's first end bytes and its
// seq (nil, 0 when empty).
func lastLine(f *os.File, end int64) ([]byte, int, error) {
	if end == 0 {
		return nil, 0, nil
	}
	nl := make([]byte, 1)
	if _, err := f.ReadAt(nl, end-1); err != nil {
		return nil, 0, err
	}
	if nl[0] != '\n' {
		return nil, 0, fmt.Errorf("%s: the last entry is incomplete (no newline: a write was cut short); swim chain repair removes it", f.Name())
	}
	start, err := lineStart(f, end-1)
	if err != nil {
		return nil, 0, err
	}
	line := make([]byte, end-1-start)
	if _, err := f.ReadAt(line, start); err != nil {
		return nil, 0, err
	}
	var e Entry
	if err := json.Unmarshal(line, &e); err != nil {
		return nil, 0, fmt.Errorf("%s: the last entry isn't valid: %v", f.Name(), err)
	}
	return line, e.Seq, nil
}

// lineStart is the offset just after the last newline before end (0 when
// there is none). It reads backwards in windows that double from 64 KiB,
// so an entry of any size is found without reading the whole chain.
func lineStart(f *os.File, end int64) (int64, error) {
	win := int64(64 * 1024)
	for hi := end; hi > 0; win *= 2 {
		lo := max(hi-win, 0)
		buf := make([]byte, hi-lo)
		if _, err := f.ReadAt(buf, lo); err != nil {
			return 0, err
		}
		if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
			return lo + int64(i) + 1, nil
		}
		hi = lo
	}
	return 0, nil
}

// Repaired is what Repair removed: the incomplete final entry's size and
// hash, and where it was kept ("" and 0 when there was nothing to repair).
type Repaired struct {
	Bytes  int64
	SHA256 string
	Backup string // .swim/chain.tail-<time>.bak
	Hash   string // the repair entry's hash
}

// Repair removes a half-written final line (a write cut short by a crash or
// a full disk): under the chain's lock it moves the bytes after the last
// newline into .swim/chain.tail-<time>.bak, truncates the chain there and
// appends a repair entry, signed with key, recording what was removed.
// Nothing changes when the chain ends with a complete line. The repair
// entry is signed before anything is touched, so a key that can't sign
// leaves the chain as it was.
func Repair(root string, key *sign.Key) (Repaired, error) {
	f, err := openLocked(root)
	if err != nil {
		return Repaired{}, err
	}
	defer f.Close()
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	st, err := f.Stat()
	if err != nil {
		return Repaired{}, err
	}
	size := st.Size()
	cut, err := lineStart(f, size)
	if err != nil || cut == size {
		return Repaired{}, err
	}
	tail := make([]byte, size-cut)
	if _, err := f.ReadAt(tail, cut); err != nil {
		return Repaired{}, err
	}
	r := Repaired{Bytes: int64(len(tail)), SHA256: Hash(tail)}
	now := time.Now().UTC()
	r.Backup = filepath.Join(filepath.Dir(Path(root)), "chain.tail-"+now.Format("20060102T150405Z")+".bak")
	// Existing fields only: an older swim re-marshals the entry to check
	// its signature, and would drop a field it doesn't know.
	e := Entry{
		Type:    "repair",
		At:      now.Format(time.RFC3339),
		Action:  fmt.Sprintf("removed %d bytes of an incomplete final entry (sha256 %s)", r.Bytes, r.SHA256),
		Outputs: map[string]string{filepath.Base(r.Backup): r.SHA256},
	}
	line, err := link(f, cut, e, key)
	if err != nil {
		return Repaired{}, err
	}
	bak, err := os.OpenFile(r.Backup, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return Repaired{}, err
	}
	_, err = bak.Write(tail)
	if err == nil {
		err = bak.Sync()
	}
	if cerr := bak.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Repaired{}, err
	}
	if err := f.Truncate(cut); err != nil {
		return Repaired{}, err
	}
	if err := writeLine(f, cut, line); err != nil {
		return Repaired{}, err
	}
	r.Hash = Hash(line)
	return r, nil
}

// Link is one entry as read back, with its line's hash.
type Link struct {
	Entry
	Hash string
	Line int // 1-based line number in the file
}

// Read returns every entry; a line that doesn't parse is an error.
func Read(root string) ([]Link, error) { return ReadFile(Path(root)) }

// ReadFile reads a chain file at path (an exported copy, say): nothing
// when there is none.
func ReadFile(path string) ([]Link, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Link
	// ReadBytes, not a Scanner: an entry has no size limit.
	rd := bufio.NewReader(f)
	for n := 1; ; n++ {
		raw, err := rd.ReadBytes('\n')
		if len(raw) == 0 && errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return out, err
		}
		if err != nil { // a last line with no newline: a write cut short
			return out, fmt.Errorf("%s line %d: an incomplete entry (no newline: a write was cut short); swim chain repair removes it", path, n)
		}
		raw = bytes.TrimSuffix(raw[:len(raw)-1], []byte("\r"))
		var e Entry
		if err := json.Unmarshal(raw, &e); err != nil {
			return out, fmt.Errorf("%s line %d: not a chain entry: %v", path, n, err)
		}
		out = append(out, Link{Entry: e, Hash: Hash(raw), Line: n})
	}
}

// Problem is a break in the chain.
type Problem struct {
	Line int
	Seq  int
	What string
}

// Check walks the chain: every entry follows the one before it (seq and
// prev hash), and every signed entry's signature is valid for its signer.
// Unsigned entries are reported separately (they are linked, but no one
// vouches for them).
func Check(links []Link) (problems []Problem, unsigned []int) {
	prev := ""
	for i, l := range links {
		if l.Seq != i+1 {
			problems = append(problems, Problem{l.Line, l.Seq, fmt.Sprintf("seq %d where %d was expected (an entry was removed or inserted)", l.Seq, i+1)})
		}
		if l.Prev != prev {
			problems = append(problems, Problem{l.Line, l.Seq, "prev hash doesn't match the entry before it (an earlier entry was changed, removed or reordered)"})
		}
		prev = l.Hash
		if l.Sig == "" {
			unsigned = append(unsigned, l.Seq)
			continue
		}
		if err := sign.Verify(l.Signer, payload(l.Entry), l.Sig); err != nil {
			problems = append(problems, Problem{l.Line, l.Seq, "signature doesn't match the entry (it was changed after signing): " + err.Error()})
		}
	}
	return problems, unsigned
}

// Untrusted is a signature by a key outside the trusted set.
type Untrusted struct {
	Seq  int
	Line int
	Key  string // the signer's public key
	Who  string // "" for the entry's signer, else the approver's name
}

// Trust checks who signed: Check proves every signature matches the key
// recorded beside it, which a chain re-signed wholesale with a new key
// also passes; Trust says which signatures, entries' and their signed
// approvals', were made by keys trusted doesn't vouch for. Unsigned
// entries are Check's to report.
func Trust(links []Link, trusted func(pub string) bool) []Untrusted {
	var out []Untrusted
	for _, l := range links {
		if l.Sig != "" && !trusted(l.Signer) {
			out = append(out, Untrusted{Seq: l.Seq, Line: l.Line, Key: l.Signer})
		}
		for _, ap := range l.Approvers {
			if ap.Sig != "" && !trusted(ap.Key) {
				out = append(out, Untrusted{Seq: l.Seq, Line: l.Line, Key: ap.Key, Who: or(ap.Name, "an approver")})
			}
		}
	}
	return out
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
