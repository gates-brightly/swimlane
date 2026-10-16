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
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
	"filippo.io/age/agessh"
	"filippo.io/age/armor"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/chars"
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
	ScopeHub  = "hub" // granted to this directory by its hub
)

// ValidName reports whether name can name a secret: a letter, then letters,
// digits and underscores (so _swim.<name> ends where the name does).
func ValidName(name string) bool {
	return chars.Word(name, chars.Letter, chars.Or(chars.Alnum, chars.In("_")))
}

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

// Unprotected reports whether the private key has no passphrase: then any
// process running as the operator (a lane) can decrypt the store itself.
func Unprotected(k *Key) bool {
	pem, err := os.ReadFile(k.Path)
	if err != nil {
		return false
	}
	_, err = agessh.ParseIdentity(pem)
	return err == nil
}

// Secret is a decrypted secret.
type Secret struct {
	Name, Value string
	Scope       string // ScopeAll, ScopeRepo, or "env" (SWIM_SECRET_<NAME>)
}

// Vault is the secrets one repo sees, decrypted.
type Vault struct {
	secrets  map[string]Secret
	errs     map[string]error // secrets that wouldn't decrypt (another key)
	env      func(string) string
	envNames []string // secrets SWIM_SECRET_<NAME> supplies, for All
}

// Open decrypts every secret repo sees with id (nil when there's no key:
// only SWIM_SECRET_<NAME> resolves then). env is os.Getenv in a run.
func Open(f *File, repo string, id age.Identity, env func(string) string) *Vault {
	v := &Vault{secrets: map[string]Secret{}, errs: map[string]error{}, env: env}
	if env != nil {
		for _, kv := range os.Environ() {
			if k, _, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, EnvPrefix) {
				if n := strings.ToLower(strings.TrimPrefix(k, EnvPrefix)); ValidName(n) {
					v.envNames = append(v.envNames, n)
				}
			}
		}
	}
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
	for _, n := range v.envNames {
		if !names[strings.ToLower(n)] {
			names[n] = true
		}
	}
	for n := range names {
		if s, err := v.Get(n); err == nil {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// RepoKey is root's key in the store's repo scope: the git toplevel, or
// "" outside a git repo (only all secrets apply there).
func RepoKey(root string) string {
	c := exec.Command("git", "rev-parse", "--show-toplevel")
	c.Dir = root
	out, err := c.Output()
	if err != nil || filepath.Clean(strings.TrimSpace(string(out))) != filepath.Clean(root) {
		return ""
	}
	return root
}

// OpenStore loads the store at storePath and decrypts what repo sees with
// the key at keyPath. prompt asks a protected key's passphrase (nil: don't
// ask). The vault always comes back: when the key can't be used it resolves
// only SWIM_SECRET_<NAME>, and the error says why.
func OpenStore(storePath, repo, keyPath string, prompt func() ([]byte, error), getenv func(string) string) (*Vault, *File, error) {
	f, err := Load(storePath)
	if err != nil {
		return Open(&File{Schema: Schema}, repo, nil, getenv), nil, err
	}
	if f.Empty(repo) {
		return Open(f, repo, nil, getenv), f, nil
	}
	key, err := LoadKey(keyPath)
	if err != nil {
		return Open(f, repo, nil, getenv), f, err
	}
	id, err := key.Identity(prompt)
	if err != nil {
		return Open(f, repo, nil, getenv), f, err
	}
	return Open(f, repo, id, getenv), f, nil
}

// WorkerKey is a hub worker's own age key at path (made, private, the first
// time). Grants from the hub are encrypted to it; it has no passphrase, so
// swim watch can decrypt unattended.
func WorkerKey(path string) (*age.X25519Identity, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return age.ParseX25519Identity(strings.TrimSpace(string(data)))
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(id.String()+"\n"), 0o600); err != nil {
		return nil, err
	}
	return id, nil
}

// EncryptTo encrypts value to an age public key (a hub worker's), armored.
func EncryptTo(recipient, value string) (string, error) {
	r, err := age.ParseX25519Recipient(strings.TrimSpace(recipient))
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	aw := armor.NewWriter(&buf)
	w, err := age.Encrypt(aw, r)
	if err != nil {
		return "", err
	}
	if _, err := io.WriteString(w, value); err != nil {
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

// DecryptWith decrypts an armored age ciphertext with id.
func DecryptWith(armored string, id age.Identity) (string, error) { return decrypt(armored, id) }

// Granted is a secret a hub granted: its value, and the guard flags its
// admin set on the grant (any one releases it; none: unguarded).
type Granted struct {
	Value string
	Guard []string
}

// Values are granted's values by name, for Add.
func Values(granted map[string]Granted) map[string]string {
	out := map[string]string{}
	for n, g := range granted {
		out[n] = g.Value
	}
	return out
}

// Add puts secrets from elsewhere (a hub's grants) into the vault as scope,
// under the ones it already resolves.
func (v *Vault) Add(scope string, s map[string]string) {
	for n, val := range s {
		if _, ok := v.secrets[n]; ok {
			continue
		}
		delete(v.errs, n)
		v.secrets[n] = Secret{Name: n, Value: val, Scope: scope}
	}
}
