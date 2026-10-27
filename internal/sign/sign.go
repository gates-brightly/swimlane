// Package sign makes and checks SSH signatures (the SSHSIG format of
// `ssh-keygen -Y sign`, as git's SSH commit signing uses) by running
// ssh-keygen, so any key it handles works: files, ssh-agent (give the .pub
// path) and hardware keys. Signatures are stored as one line of base64
// (the armor's body) next to the signer's public key, and anyone can check
// them with ssh-keygen alone.
package sign

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/crypto/ssh"
)

// Namespace scopes swim's signatures: one made for swim can't be passed off
// as a git commit signature or anything else, and vice versa.
const Namespace = "swim@gates-brightly"

// Key is a signing key: the path ssh-keygen signs with (a private key, or
// a .pub whose private half is in ssh-agent) and its public key line; or a
// run key (NewRunKey), held in memory and vouched for by RunKey.
type Key struct {
	Path   string
	Public string // "ssh-ed25519 AAAA... comment"
	RunKey *Delegation

	signer ssh.Signer // a run key's private half
}

// Available reports whether ssh-keygen is installed.
func Available() bool {
	_, err := exec.LookPath("ssh-keygen")
	return err == nil
}

// LoadKey finds the signing key: path if given (a private key or .pub),
// else the first of ~/.ssh/id_ed25519, id_ecdsa, id_rsa that exists.
func LoadKey(path string) (*Key, error) {
	var candidates []string
	if path != "" {
		if strings.HasPrefix(path, "~/") {
			home, _ := os.UserHomeDir()
			path = filepath.Join(home, path[2:])
		}
		candidates = []string{path}
	} else {
		home, _ := os.UserHomeDir()
		for _, n := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
			candidates = append(candidates, filepath.Join(home, ".ssh", n))
		}
	}
	for _, p := range candidates {
		pub := p
		if !strings.HasSuffix(p, ".pub") {
			pub = p + ".pub"
		}
		data, err := os.ReadFile(pub)
		if err != nil {
			if path != "" {
				return nil, fmt.Errorf("signing key %s: no public key at %s", path, pub)
			}
			continue
		}
		return &Key{Path: p, Public: strings.TrimSpace(string(data))}, nil
	}
	if path != "" {
		return nil, fmt.Errorf("signing key %s not found", path)
	}
	return nil, errors.New("no signing key: set signing_key in ~/.config/swim/config.yml, or create ~/.ssh/id_ed25519")
}

// Sign signs data with k and returns the signature as one line of base64.
func Sign(k *Key, data []byte) (string, error) {
	if k.signer != nil {
		return sshsig(k.signer, data)
	}
	dir, err := os.MkdirTemp("", "swim-sign-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	msg := filepath.Join(dir, "msg")
	if err := os.WriteFile(msg, data, 0o600); err != nil {
		return "", err
	}
	cmd := exec.Command("ssh-keygen", "-Y", "sign", "-q", "-f", k.Path, "-n", Namespace, msg)
	cmd.Stdin = nil
	// No controlling terminal: a key with a passphrase (and no ssh-agent)
	// fails at once instead of prompting where nobody may be looking.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ssh-keygen -Y sign with %s: %v: %s (a key with a passphrase needs ssh-agent: give its .pub path)", k.Path, err, strings.TrimSpace(stderr.String()))
	}
	armor, err := os.ReadFile(msg + ".sig")
	if err != nil {
		return "", err
	}
	return unarmor(string(armor)), nil
}

// Verify checks that sig is a signature of data by the key pub.
func Verify(pub string, data []byte, sig string) error {
	dir, err := os.MkdirTemp("", "swim-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	allowed := filepath.Join(dir, "allowed")
	if err := os.WriteFile(allowed, []byte("signer "+keyOnly(pub)+"\n"), 0o600); err != nil {
		return err
	}
	sigPath := filepath.Join(dir, "sig")
	if err := os.WriteFile(sigPath, []byte(armorSig(sig)), 0o600); err != nil {
		return err
	}
	cmd := exec.Command("ssh-keygen", "-Y", "verify", "-f", allowed, "-I", "signer", "-n", Namespace, "-s", sigPath)
	cmd.Stdin = bytes.NewReader(data)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("bad signature: %s", strings.Join(strings.Fields(string(out)), " "))
	}
	return nil
}

// Fingerprint is the key's SHA256 fingerprint, as ssh-keygen -l shows it.
func Fingerprint(pub string) string {
	cmd := exec.Command("ssh-keygen", "-l", "-f", "-")
	cmd.Stdin = strings.NewReader(keyOnly(pub) + "\n")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	if f := strings.Fields(string(out)); len(f) >= 2 {
		return f[1]
	}
	return ""
}

// keyOnly is "type base64" without the comment, the form keys are
// compared and listed in.
func keyOnly(pub string) string {
	f := strings.Fields(pub)
	if len(f) >= 2 {
		return f[0] + " " + f[1]
	}
	return strings.TrimSpace(pub)
}

// KeyOnly is exported for matching keys against lookup.yml.
func KeyOnly(pub string) string { return keyOnly(pub) }

func unarmor(armor string) string {
	var b strings.Builder
	for _, l := range strings.Split(armor, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "-----") {
			continue
		}
		b.WriteString(l)
	}
	return b.String()
}

func armorSig(sig string) string {
	var b strings.Builder
	b.WriteString("-----BEGIN SSH SIGNATURE-----\n")
	for len(sig) > 70 {
		b.WriteString(sig[:70] + "\n")
		sig = sig[70:]
	}
	if sig != "" {
		b.WriteString(sig + "\n")
	}
	b.WriteString("-----END SSH SIGNATURE-----\n")
	return b.String()
}
