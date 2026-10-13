// Package chain keeps swim's chain of custody: an append-only file of
// entries, one JSON object per line, each holding the SHA-256 of the line
// before it and an SSH signature by whoever recorded it. Changing,
// removing or reordering any entry breaks every link after it, and a
// forged entry fails its signature, so tampering is provable (swim verify).
//
// Entries are approvals (who approved which guard of which exact script,
// and how) and rounds (what ran: the script's hash, the hash of the
// round's log, the result, the approvals it used), recorded as they
// happen.
package chain

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	Type string `json:"type"` // approve | round

	Run    string `json:"run,omitempty"`
	Lane   int    `json:"lane,omitempty"`
	Job    string `json:"job,omitempty"`
	Round  string `json:"round,omitempty"`
	Script string `json:"script,omitempty"` // SHA-256 of the lane script

	// approve
	Guard     string     `json:"guard,omitempty"`
	Action    string     `json:"action,omitempty"`    // the guard's description
	Method    string     `json:"method,omitempty"`    // signature | prompt | flag | auto | queue
	Statement string     `json:"statement,omitempty"` // what a signature's approvers signed
	Approvers []Approver `json:"approvers,omitempty"`

	// round
	Result    string   `json:"result,omitempty"`
	Exit      *int     `json:"exit,omitempty"`
	Log       string   `json:"log,omitempty"`       // SHA-256 of the round's section of the log
	Approvals []string `json:"approvals,omitempty"` // hashes of the approve entries it used

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
	path := Path(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return "", err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	last, seq, err := lastLine(f)
	if err != nil {
		return "", err
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
			return "", err
		}
		e.Sig = sig
	}
	line, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return "", err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return "", err
	}
	return Hash(line), nil
}

// lastLine returns the chain's last line and its seq (nil, 0 when empty).
func lastLine(f *os.File) ([]byte, int, error) {
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return nil, 0, err
	}
	// Read back far enough to hold the last line.
	size := st.Size()
	start := size - 256*1024
	if start < 0 {
		start = 0
	}
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return nil, 0, err
	}
	if buf[len(buf)-1] != '\n' {
		return nil, 0, fmt.Errorf("%s: the last entry is incomplete (no newline); swim verify shows where", f.Name())
	}
	body := buf[:len(buf)-1]
	i := strings.LastIndexByte(string(body), '\n')
	line := body[i+1:]
	var e Entry
	if err := json.Unmarshal(line, &e); err != nil {
		return nil, 0, fmt.Errorf("%s: the last entry isn't valid: %v", f.Name(), err)
	}
	return line, e.Seq, nil
}

// Link is one entry as read back, with its line's hash.
type Link struct {
	Entry
	Hash string
	Line int // 1-based line number in the file
}

// Read returns every entry; a line that doesn't parse is an error.
func Read(root string) ([]Link, error) {
	f, err := os.Open(Path(root))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Link
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	n := 0
	for sc.Scan() {
		n++
		raw := append([]byte(nil), sc.Bytes()...)
		var e Entry
		if err := json.Unmarshal(raw, &e); err != nil {
			return out, fmt.Errorf("%s line %d: not a chain entry: %v", Path(root), n, err)
		}
		out = append(out, Link{Entry: e, Hash: Hash(raw), Line: n})
	}
	return out, sc.Err()
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
