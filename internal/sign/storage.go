package sign

import (
	"errors"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
)

// Where a key's private half lives, weakest first against a process that
// runs as you (a lane does).
const (
	// InFile: a private key file with no passphrase. Anything running as
	// you can read it and sign with it.
	InFile = "file"
	// InLockedFile: a passphrase-protected file, used through ssh-agent.
	// The file is useless without the passphrase; the agent isn't.
	InLockedFile = "passphrase file"
	// InAgent: only in ssh-agent, no private key file. Nothing to copy,
	// but anything running as you can ask the agent, unless it confirms
	// each use (ssh-add -c).
	InAgent = "agent"
	// OnDevice: a security key (sk-*): signing needs the device, and a
	// touch unless the key was made with no-touch-required.
	OnDevice = "security key"
)

// Storage says where k's private half lives (one of the constants above).
func Storage(k *Key) string {
	if strings.HasPrefix(k.Public, "sk-") {
		return OnDevice
	}
	data, err := os.ReadFile(strings.TrimSuffix(k.Path, ".pub"))
	if err != nil {
		return InAgent
	}
	if _, err := ssh.ParseRawPrivateKey(data); errors.As(err, new(*ssh.PassphraseMissingError)) {
		return InLockedFile
	}
	return InFile
}
