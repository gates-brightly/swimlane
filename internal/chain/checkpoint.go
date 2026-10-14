package chain

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gates-brightly/swimlane/internal/sign"
)

// Checkpoint is the chain's head at a moment, signed by the runner and
// sent somewhere a lane can't rewrite (an anchor). Later, the chain must
// still hold an entry at Seq whose line hashes to Head: entries removed
// from the end, or a chain replaced wholesale, no longer do.
type Checkpoint struct {
	V           int    `json:"v"`
	Seq         int    `json:"seq"`
	Head        string `json:"head"` // the SHA-256 of entry Seq's line
	At          string `json:"at"`
	Host        string `json:"host,omitempty"`
	RunnerKeyFP string `json:"runner_key_fp,omitempty"`
	Signer      string `json:"signer,omitempty"`
	Sig         string `json:"sig,omitempty"`
}

// Head is the chain's last entry: its seq and line hash (0, "" when
// empty).
func Head(links []Link) (int, string) {
	if len(links) == 0 {
		return 0, ""
	}
	l := links[len(links)-1]
	return l.Seq, l.Hash
}

// NewCheckpoint is an unsigned checkpoint of links' head, now.
func NewCheckpoint(links []Link, host string) Checkpoint {
	seq, head := Head(links)
	return Checkpoint{V: 1, Seq: seq, Head: head, At: time.Now().UTC().Format(time.RFC3339), Host: host}
}

// payload is what the runner signs: the checkpoint without its signature,
// typed so it can't pass for a chain entry (or an entry for it).
func (cp Checkpoint) payload() []byte {
	cp.Sig = ""
	b, _ := json.Marshal(struct {
		Type string `json:"type"`
		Checkpoint
	}{"checkpoint", cp})
	return b
}

// SignCheckpoint signs cp with key.
func SignCheckpoint(cp *Checkpoint, key *sign.Key) error {
	cp.Signer, cp.Sig = key.Public, ""
	cp.RunnerKeyFP = sign.Fingerprint(key.Public)
	sig, err := sign.Sign(key, cp.payload())
	if err != nil {
		return err
	}
	cp.Sig = sig
	return nil
}

// ErrUnsigned is a checkpoint without a signature.
var ErrUnsigned = errors.New("unsigned checkpoint")

// VerifyCheckpoint checks cp's signature against the key recorded in it
// (who that is, is the caller's to judge).
func VerifyCheckpoint(cp Checkpoint) error {
	if cp.Sig == "" {
		return ErrUnsigned
	}
	return sign.Verify(cp.Signer, cp.payload(), cp.Sig)
}

// What CheckAgainst found.
const (
	Extends   = "extends"   // the checkpointed entry is there
	Truncated = "truncated" // the chain ends before it
	Replaced  = "replaced"  // another entry stands at its seq
)

// Against is the chain checked against a checkpoint.
type Against struct {
	Status string
	Since  int // entries after the checkpoint (Extends)
}

// CheckAgainst reports whether links still hold cp's head: the entry at
// cp.Seq, with cp.Head as its line's hash.
func CheckAgainst(links []Link, cp Checkpoint) Against {
	if cp.Seq > len(links) {
		return Against{Status: Truncated}
	}
	if cp.Seq < 1 || links[cp.Seq-1].Hash != cp.Head {
		return Against{Status: Replaced}
	}
	return Against{Status: Extends, Since: len(links) - cp.Seq}
}

// String is "seq 412 (2026-10-14T09:12:00Z)".
func (cp Checkpoint) String() string {
	return fmt.Sprintf("seq %d (%s)", cp.Seq, cp.At)
}
