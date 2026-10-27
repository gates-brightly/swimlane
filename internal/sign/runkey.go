package sign

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// A run key signs one swim run's chain entries. It is made in the
// launcher's memory as the run starts, and is never written anywhere or
// handed to a lane; the runner key vouches for it once (a Delegation), so
// a runner key that asks for each use (a security key's touch, ssh-add -c)
// asks once per run, and a lane that wants a signature swim verify trusts
// has to ask the runner key itself.

// Delegation is the runner key (By) vouching for a run key (Key) for one
// run: By's signature (Sig) over Statement.
type Delegation struct {
	Run  string `json:"run"`
	Key  string `json:"key"`
	Host string `json:"host,omitempty"`
	At   string `json:"at"`
	By   string `json:"by"`
	Sig  string `json:"sig"`
}

// Statement is what By signs: typed, so it can't pass for a chain entry,
// a checkpoint or an approval.
func (d *Delegation) Statement() string {
	return fmt.Sprintf("swim run key v1\nrun: %s\nkey: %s\nhost: %s\nat: %s\n", d.Run, keyOnly(d.Key), d.Host, d.At)
}

// Check verifies d vouches for signer: By signed the statement, and the
// key it names is signer.
func (d *Delegation) Check(signer string) error {
	if keyOnly(d.Key) != keyOnly(signer) {
		return fmt.Errorf("its run key vouches for another key")
	}
	if err := Verify(d.By, []byte(d.Statement()), d.Sig); err != nil {
		return fmt.Errorf("its run key's delegation: %v", err)
	}
	return nil
}

// NewRunKey makes a run key for run in memory and has by vouch for it
// (one signature by by: a prompt, a touch, if by asks for each use).
func NewRunKey(by *Key, run, host string) (*Key, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	pub := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.PublicKey()))) + " swim-run-" + run
	d := &Delegation{Run: run, Key: pub, Host: host, At: time.Now().UTC().Format(time.RFC3339), By: by.Public}
	if d.Sig, err = Sign(by, []byte(d.Statement())); err != nil {
		return nil, err
	}
	return &Key{Public: pub, RunKey: d, signer: s}, nil
}

// sshsig signs data with s in the SSHSIG format ssh-keygen -Y sign writes
// (PROTOCOL.sshsig), as one line of base64.
func sshsig(s ssh.Signer, data []byte) (string, error) {
	h := sha512.Sum512(data)
	signed := ssh.Marshal(struct {
		Magic     [6]byte
		Namespace string
		Reserved  string
		Hash      string
		Digest    string
	}{sigMagic, Namespace, "", "sha512", string(h[:])})
	sig, err := s.Sign(rand.Reader, signed)
	if err != nil {
		return "", err
	}
	blob := ssh.Marshal(struct {
		Magic     [6]byte
		Version   uint32
		PublicKey string
		Namespace string
		Reserved  string
		Hash      string
		Signature string
	}{sigMagic, 1, string(s.PublicKey().Marshal()), Namespace, "", "sha512", string(ssh.Marshal(sig))})
	return base64.StdEncoding.EncodeToString(blob), nil
}

var sigMagic = [6]byte{'S', 'S', 'H', 'S', 'I', 'G'}
