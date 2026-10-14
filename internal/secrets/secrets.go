// Package secrets keeps an operator's secrets encrypted to their SSH key
// (with age, https://age-encryption.org) and resolves them for lanes.
//
// The store is one file next to swim's config, secrets.yml (0600). Names
// are readable, so swim secret ls and swim lint need no key; each value is
// its own age ciphertext, so changing one never touches the others:
//
//	schema: swim.secrets/v1
//	all:                       # every repo
//	  datadog_api_key: {value: "-----BEGIN AGE ENCRYPTED FILE-----...", key: SHA256:..., updated: ...}
//	repos:
//	  /Users/zach/dev/orders:  # this git repo only; wins over all
//	    db_password: {...}
//
// Decrypting needs the private key on disk (ssh-agent can't decrypt). A
// lane gets a secret from its swim run, which decrypts once at start and
// serves lanes over a private socket (broker.go). In CI, where there's no
// key, SWIM_SECRET_<NAME> supplies a secret instead.
package secrets

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
	"filippo.io/age/agessh"
	"filippo.io/age/armor"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
)

// Schema identifies the store file.
const Schema = "swim.secrets/v1"

// EnvPrefix names a secret's stand-in in the environment (CI):
// SWIM_SECRET_DB_PASSWORD supplies db_password, ahead of the store.
const EnvPrefix = "SWIM_SECRET_"

// Scope names.
const (
	ScopeAll  = "all"
	ScopeRepo = "repo"
)

var nameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// ValidName reports whether name can name a secret: a letter, then letters,
// digits and underscores (so _swim.<name> ends where the name does).
func ValidName(name string) bool { return nameRE.MatchString(name) }

// EnvName is the environment variable a bare secret name exports as.
func EnvName(name string) string { return strings.ToUpper(name) }

// Entry is one encrypted secret.
type Entry struct {
	Value   string `yaml:"value"`   // age ciphertext, armored
	Key     string `yaml:"key"`     // fingerprint of the SSH key it's encrypted to
	Updated string `yaml:"updated"` // RFC 3339, UTC
}

// File is the store.
type File struct {
	Schema string                      `yaml:"schema"`
	All    map[string]Entry            `yaml:"all,omitempty"`
	Repos  map[string]map[string]Entry `yaml:"repos,omitempty"`
}

// Path is the store next to the config file at configPath.
func Path(configPath string) string { return filepath.Join(filepath.Dir(configPath), "secrets.yml") }

// Load reads the store (empty if there is none).
func Load(path string) (*File, error) {
	f := &File{Schema: Schema}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if f.Schema != Schema {
		return nil, fmt.Errorf("%s: schema %q, want %s", path, f.Schema, Schema)
	}
	return f, nil
}

// Save writes the store, private to its owner.
func (f *File) Save(path string) error {
	data, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// scope is the map holding scope's secrets (repo "" is all), made if make.
func (f *File) scope(repo string, mk bool) map[string]Entry {
	if repo == "" {
		if f.All == nil && mk {
			f.All = map[string]Entry{}
		}
		return f.All
	}
	if f.Repos == nil && mk {
		f.Repos = map[string]map[string]Entry{}
	}
	m := f.Repos[repo]
	if m == nil && mk {
		m = map[string]Entry{}
		f.Repos[repo] = m
	}
	return m
}

// Set encrypts value to key and stores it as name in repo's scope ("" for
// all).
func (f *File) Set(key *Key, repo, name, value string) error {
	if !ValidName(name) {
		return fmt.Errorf("%q isn't a secret name: use a letter, then letters, digits and _", name)
	}
	ct, err := key.encrypt([]byte(value))
	if err != nil {
		return err
	}
	f.scope(repo, true)[name] = Entry{Value: ct, Key: key.Fingerprint, Updated: time.Now().UTC().Format(time.RFC3339)}
	return nil
}

// Remove deletes name from repo's scope; false if it wasn't there.
func (f *File) Remove(repo, name string) bool {
	m := f.scope(repo, false)
	if _, ok := m[name]; !ok {
		return false
	}
	delete(m, name)
	if repo != "" && len(m) == 0 {
		delete(f.Repos, repo)
	}
	return true
}

// Listed is a secret as swim secret ls shows it: never its value.
type Listed struct {
	Name     string
	Scope    string // ScopeAll or ScopeRepo
	Key      string
	Updated  string
	Shadowed bool // an all secret the repo's own secret of that name hides
}

// List is the secrets repo sees, repo's first, by name.
func (f *File) List(repo string) []Listed {
	var out []Listed
	own := f.scope(repo, false)
	if repo != "" {
		for n, e := range own {
			out = append(out, Listed{Name: n, Scope: ScopeRepo, Key: e.Key, Updated: e.Updated})
		}
	}
	for n, e := range f.All {
		_, hidden := own[n]
		out = append(out, Listed{Name: n, Scope: ScopeAll, Key: e.Key, Updated: e.Updated, Shadowed: repo != "" && hidden})
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Name != out[b].Name {
			return out[a].Name < out[b].Name
		}
		return out[a].Scope == ScopeRepo
	})
	return out
}

// Has reports whether repo sees a secret called name (no key needed).
func (f *File) Has(repo, name string) bool {
	if _, ok := f.scope(repo, false)[name]; ok && repo != "" {
		return true
	}
	_, ok := f.All[name]
	return ok
}

// Empty reports whether repo sees no secrets at all.
func (f *File) Empty(repo string) bool {
	return len(f.All) == 0 && (repo == "" || len(f.scope(repo, false)) == 0)
}

// Key is the SSH key secrets are encrypted to.
type Key struct {
	Path        string // the private key file
	Public      string // its public key line
	Fingerprint string // SHA256:...
	recipient   age.Recipient
	pub         ssh.PublicKey
}

// LoadKey finds the key at path (the private key or its .pub; "" tries
// ~/.ssh/id_ed25519, then id_rsa). Encrypting needs only the public key.
func LoadKey(path string) (*Key, error) {
	var candidates []string
	if path != "" {
		if strings.HasPrefix(path, "~/") {
			home, _ := os.UserHomeDir()
			path = filepath.Join(home, path[2:])
		}
		candidates = []string{strings.TrimSuffix(path, ".pub")}
	} else {
		home, _ := os.UserHomeDir()
		for _, n := range []string{"id_ed25519", "id_rsa"} {
			candidates = append(candidates, filepath.Join(home, ".ssh", n))
		}
	}
	var lastErr error
	for _, p := range candidates {
		data, err := os.ReadFile(p + ".pub")
		if err != nil {
			lastErr = fmt.Errorf("secrets key %s: no public key at %s.pub", p, p)
			continue
		}
		line := strings.TrimSpace(string(data))
		r, err := agessh.ParseRecipient(line)
		if err != nil {
			return nil, fmt.Errorf("secrets key %s.pub: %v (use an ssh-ed25519 or ssh-rsa key)", p, err)
		}
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			return nil, fmt.Errorf("secrets key %s.pub: %v", p, err)
		}
		sum := sha256.Sum256(pub.Marshal())
		return &Key{Path: p, Public: line, Fingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]), recipient: r, pub: pub}, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no secrets key")
	}
	return nil, lastErr
}

func (k *Key) encrypt(plain []byte) (string, error) {
	var buf bytes.Buffer
	aw := armor.NewWriter(&buf)
	w, err := age.Encrypt(aw, k.recipient)
	if err != nil {
		return "", err
	}
	if _, err := w.Write(plain); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	if err := aw.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// ErrPassphrase means the private key is protected and no passphrase
// prompt was possible (nobody at a terminal).
var ErrPassphrase = errors.New("the secrets key needs a passphrase and nobody is at a terminal to give it")

// Identity reads the private key for decrypting. passphrase is asked only
// for a protected key; nil means don't ask (ErrPassphrase).
func (k *Key) Identity(passphrase func() ([]byte, error)) (age.Identity, error) {
	pem, err := os.ReadFile(k.Path)
	if err != nil {
		return nil, fmt.Errorf("secrets key %s: %v (decrypting needs the private key on disk; ssh-agent can't decrypt)", k.Path, err)
	}
	id, err := agessh.ParseIdentity(pem)
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		if passphrase == nil {
			return nil, ErrPassphrase
		}
		return agessh.NewEncryptedSSHIdentity(k.pub, pem, passphrase)
	}
	if err != nil {
		return nil, fmt.Errorf("secrets key %s: %v", k.Path, err)
	}
	return id, nil
}

// Secret is a decrypted secret.
type Secret struct {
	Name, Value string
	Scope       string // ScopeAll, ScopeRepo, or "env" (SWIM_SECRET_<NAME>)
}

// Vault is the secrets one repo sees, decrypted.
type Vault struct {
	secrets map[string]Secret
	errs    map[string]error // secrets that wouldn't decrypt (another key)
	env     func(string) string
}

// Open decrypts every secret repo sees with id (nil when there's no key:
// only SWIM_SECRET_<NAME> resolves then). env is os.Getenv in a run.
func Open(f *File, repo string, id age.Identity, env func(string) string) *Vault {
	v := &Vault{secrets: map[string]Secret{}, errs: map[string]error{}, env: env}
	put := func(m map[string]Entry, scope string) {
		for n, e := range m {
			if id == nil {
				v.errs[n] = errors.New("no secrets key to decrypt it")
				continue
			}
			plain, err := decrypt(e.Value, id)
			if err != nil {
				v.errs[n] = fmt.Errorf("can't decrypt %s (%s scope; encrypted to %s): %v", n, scope, e.Key, err)
				continue
			}
			delete(v.errs, n)
			v.secrets[n] = Secret{Name: n, Value: plain, Scope: scope}
		}
	}
	put(f.All, ScopeAll)
	if repo != "" {
		put(f.scope(repo, false), ScopeRepo) // the repo's own win
	}
	return v
}

func decrypt(armored string, id age.Identity) (string, error) {
	r, err := age.Decrypt(armor.NewReader(strings.NewReader(armored)), id)
	if err != nil {
		return "", err
	}
	data, err := io.ReadAll(r)
	return string(data), err
}

// Get resolves name: SWIM_SECRET_<NAME> first, then the store.
func (v *Vault) Get(name string) (Secret, error) {
	if !ValidName(name) {
		return Secret{}, fmt.Errorf("%q isn't a secret name", name)
	}
	if v.env != nil {
		if val := v.env(EnvPrefix + EnvName(name)); val != "" {
			return Secret{Name: name, Value: val, Scope: "env"}, nil
		}
	}
	if s, ok := v.secrets[name]; ok {
		return s, nil
	}
	if err := v.errs[name]; err != nil {
		return Secret{}, err
	}
	return Secret{}, fmt.Errorf("no secret %s (swim secret set %s; swim secret ls lists them)", name, name)
}

// All is every secret the vault resolves, for masking: decrypted ones and
// SWIM_SECRET_<NAME> stand-ins for them.
func (v *Vault) All() []Secret {
	var out []Secret
	names := map[string]bool{}
	for n := range v.secrets {
		names[n] = true
	}
	for n := range v.errs {
		names[n] = true
	}
	for n := range names {
		if s, err := v.Get(n); err == nil {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// Placeholder matches _swim.<name> in a step's arguments.
var Placeholder = regexp.MustCompile(`_swim\.([A-Za-z][A-Za-z0-9_]*)`)

// Expand replaces every _swim.<name> in args using get; the originals are
// what the log shows.
func Expand(args []string, get func(name string) (string, error)) ([]string, error) {
	out := make([]string, len(args))
	var firstErr error
	for i, a := range args {
		out[i] = Placeholder.ReplaceAllStringFunc(a, func(m string) string {
			val, err := get(m[len("_swim."):])
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return m
			}
			return val
		})
	}
	return out, firstErr
}
