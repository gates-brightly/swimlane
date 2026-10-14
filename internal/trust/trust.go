// Package trust says which keys swim verify trusts to sign the chain of
// custody, from somewhere a lane can't quietly change.
//
// trust.yml (repo root, committed) lists the keys: the runner's and the
// approvers'. Its key set (each key as "type base64", sorted, comments
// dropped) has a SHA-256, and that hash is pinned outside the repo, in
// the operator's ~/.config/swim/trust-pins.yml, the first time swim
// verify sees it (trust on first use) or by swim keys pin. A later
// trust.yml is trusted only if a chain of signed changes leads to it from
// the pin: trust.yml.sig (committed) records each change, signed by a key
// the set before it trusted (swim keys trust KEY --sign).
//
//	keys:
//	  - ssh-ed25519 AAAA... runner@build-1
//	  - ssh-ed25519 AAAA... zach@laptop
package trust

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/sign"
)

// File names at the repo root.
const (
	FileName = "trust.yml"
	SigName  = "trust.yml.sig"
)

// PinEnv pins trust.yml's hash for one command (CI, where there is no
// personal config to keep a pin in): the same as swim verify --trust-pin.
const PinEnv = "SWIM_TRUST_PIN"

// File is trust.yml.
type File struct {
	Keys []string `yaml:"keys"`
	Path string   `yaml:"-"`
}

// Load reads root's trust.yml: nil (and no error) when there is none.
func Load(root string) (*File, error) {
	path := filepath.Join(root, FileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for i, k := range f.Keys {
		if len(strings.Fields(k)) < 2 {
			return nil, fmt.Errorf("%s: keys[%d] isn't an SSH public key (type base64 [comment])", path, i)
		}
	}
	f.Path = path
	return &f, nil
}

// Save writes f to root's trust.yml.
func (f *File) Save(root string) error {
	data, err := marshal(struct {
		Keys []string `yaml:"keys"`
	}{f.Keys})
	if err != nil {
		return err
	}
	head := "# Keys swim verify trusts to sign the chain of custody (swim keys trust).\n" +
		"# Its hash is pinned outside the repo: change it with swim keys trust --sign.\n"
	f.Path = filepath.Join(root, FileName)
	return os.WriteFile(f.Path, append([]byte(head), data...), 0o644)
}

// Set is the canonical key set: each key as "type base64", sorted, once.
func Set(keys []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range keys {
		k = sign.KeyOnly(k)
		if k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// SHA is the hash of a key set: what gets pinned. Reordering keys or
// changing their comments doesn't change it.
func SHA(keys []string) string {
	s := sha256.Sum256([]byte(strings.Join(Set(keys), "\n") + "\n"))
	return hex.EncodeToString(s[:])
}

// SHA is the hash of f's key set.
func (f *File) SHA() string { return SHA(f.Keys) }

// Has reports whether pub is one of f's keys.
func (f *File) Has(pub string) bool {
	k := sign.KeyOnly(pub)
	for _, x := range f.Keys {
		if sign.KeyOnly(x) == k {
			return true
		}
	}
	return false
}

// Transition is one change of trust.yml, from the key set hashed prev to
// the set keys (hashed sha), signed by a key prev trusted. A record with
// no prev and no signature only says which keys a hash stands for (the
// set a first change started from); it trusts nothing by itself.
type Transition struct {
	Prev   string   `yaml:"prev,omitempty"`
	SHA    string   `yaml:"sha256"`
	Keys   []string `yaml:"keys"`
	At     string   `yaml:"at,omitempty"`
	Signer string   `yaml:"signer,omitempty"`
	Sig    string   `yaml:"sig,omitempty"`
}

// payload is what a change's signer signs: typed, so it can't pass for a
// chain entry or an approval.
func (t Transition) payload() []byte {
	b, _ := json.Marshal(struct {
		Type string `json:"type"`
		Prev string `json:"prev"`
		SHA  string `json:"sha256"`
		At   string `json:"at"`
	}{"trust", t.Prev, t.SHA, t.At})
	return b
}

// Transitions reads root's trust.yml.sig (none without one).
func Transitions(root string) ([]Transition, error) {
	path := filepath.Join(root, SigName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ts []Transition
	if err := yaml.Unmarshal(data, &ts); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return ts, nil
}

// Sign records the change of trust.yml from the key set from to the set
// to, signed with key (which from must trust), in root's trust.yml.sig.
// The first change also records from's keys, so a pin of from (which may
// hold only its hash) can be walked.
func Sign(root string, from, to []string, key *sign.Key) error {
	if !slicesContain(Set(from), sign.KeyOnly(key.Public)) {
		return fmt.Errorf("%s isn't in trust.yml: a change must be signed by a key it already trusts", sign.Fingerprint(key.Public))
	}
	ts, err := Transitions(root)
	if err != nil {
		return err
	}
	if _, ok := known(ts)[SHA(from)]; !ok {
		ts = append(ts, Transition{SHA: SHA(from), Keys: Set(from)})
	}
	t := Transition{Prev: SHA(from), SHA: SHA(to), Keys: Set(to), At: time.Now().UTC().Format(time.RFC3339), Signer: sign.KeyOnly(key.Public)}
	sig, err := sign.Sign(key, t.payload())
	if err != nil {
		return err
	}
	t.Sig = sig
	ts = append(ts, t)
	data, err := marshal(ts)
	if err != nil {
		return err
	}
	head := "# Signed changes of trust.yml (swim keys trust --sign): each from the key\n" +
		"# set hashed prev, signed by a key that set trusted. Append-only.\n"
	return os.WriteFile(filepath.Join(root, SigName), append([]byte(head), data...), 0o644)
}

// marshal is YAML indented by two, as people write it.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), enc.Close()
}

func slicesContain(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// known maps each hash the records spell out to its keys (only records
// whose keys do hash to it).
func known(ts []Transition) map[string][]string {
	m := map[string][]string{}
	for _, t := range ts {
		if SHA(t.Keys) == t.SHA {
			m[t.SHA] = Set(t.Keys)
		}
	}
	return m
}

// Pin is trust.yml's key set as the operator last trusted it.
type Pin struct {
	SHA  string   `yaml:"sha256"`
	Keys []string `yaml:"keys,omitempty"`
	At   string   `yaml:"at,omitempty"`
	// Retired are keys earlier pinned sets trusted that this one dropped:
	// the entries they signed before still check. swim keys pin forgets
	// them (a key that was compromised).
	Retired []string `yaml:"retired,omitempty"`
	// From says where an explicit pin came from (--trust-pin, SWIM_TRUST_PIN).
	From string `yaml:"-"`
}

// PinsPath is the operator's pins file, next to config.yml: outside every
// repo, so a lane can't change it through the repo (it can as the same
// user; see docs/trust-model.md).
func PinsPath() string { return filepath.Join(filepath.Dir(config.Path()), "trust-pins.yml") }

type pinsFile struct {
	Repos map[string]Pin `yaml:"repos"`
}

func readPins() (pinsFile, error) {
	var p pinsFile
	data, err := os.ReadFile(PinsPath())
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if err := yaml.Unmarshal(data, &p); err != nil {
		return p, fmt.Errorf("%s: %w", PinsPath(), err)
	}
	return p, nil
}

// LoadPin is root's pin (nil when it has none).
func LoadPin(root string) (*Pin, error) {
	p, err := readPins()
	if err != nil {
		return nil, err
	}
	for k, pin := range p.Repos {
		if config.SameRepo(k, root) {
			return &pin, nil
		}
	}
	return nil, nil
}

// SavePin pins root's trust.yml at the key set keys, still trusting
// retired for what they signed before.
func SavePin(root string, keys, retired []string) (Pin, error) {
	p, err := readPins()
	if err != nil {
		return Pin{}, err
	}
	if p.Repos == nil {
		p.Repos = map[string]Pin{}
	}
	for k := range p.Repos {
		if config.SameRepo(k, root) {
			delete(p.Repos, k)
		}
	}
	pin := Pin{SHA: SHA(keys), Keys: Set(keys), At: time.Now().UTC().Format(time.RFC3339)}
	cur := map[string]bool{}
	for _, k := range pin.Keys {
		cur[k] = true
	}
	for _, k := range Set(retired) {
		if !cur[k] {
			pin.Retired = append(pin.Retired, k)
		}
	}
	p.Repos[root] = pin
	data, err := marshal(p)
	if err != nil {
		return Pin{}, err
	}
	if err := os.MkdirAll(filepath.Dir(PinsPath()), 0o755); err != nil {
		return Pin{}, err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", PinsPath(), os.Getpid())
	head := "# trust.yml pins, by repo (swim verify, swim keys pin): the key set each\n# repo's chain must be signed by. Kept outside the repos on purpose.\n"
	if err := os.WriteFile(tmp, append([]byte(head), data...), 0o600); err != nil {
		return Pin{}, err
	}
	return pin, os.Rename(tmp, PinsPath())
}

// Status is how trust.yml stands against the pin.
type Status string

const (
	TOFU      Status = "tofu"             // no pin yet: trusted as first seen
	Match     Status = "match"            // trust.yml is the pinned set
	Advanced  Status = "advanced"         // signed changes lead from the pin to it
	Unsigned  Status = "changed-unsigned" // it changed since the pin, and no signed changes lead to it
	Malformed Status = "malformed"        // trust.yml.sig doesn't read
)

// Result is trust.yml checked against a pin.
type Result struct {
	Status Status
	SHA    string          // trust.yml's
	Pin    *Pin            // the pin checked against (nil for TOFU)
	Steps  int             // signed changes walked (Advanced)
	Keys   map[string]bool // the trusted keys ("type base64")
	Why    string          // for Unsigned and Malformed
}

// Trusted reports whether pub is one of the trusted keys.
func (r Result) Trusted(pub string) bool { return r.Keys[sign.KeyOnly(pub)] }

// KeyList is the trusted keys, sorted.
func (r Result) KeyList() []string {
	var out []string
	for k := range r.Keys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Check checks trust.yml f against pin (nil: none yet). With a pin it
// matches, or a chain of signed changes in trust.yml.sig leads from it to
// f; otherwise the keys trusted stay the pinned ones. The keys trusted
// are those of every set on the way (and the pin's retired keys), so
// entries signed before a key was removed still check.
func Check(root string, f *File, pin *Pin) Result {
	r := Result{SHA: f.SHA(), Pin: pin, Keys: map[string]bool{}}
	add := func(keys []string) {
		for _, k := range Set(keys) {
			r.Keys[k] = true
		}
	}
	if pin == nil {
		r.Status = TOFU
		add(f.Keys)
		return r
	}
	add(pin.Retired)
	if pin.SHA == r.SHA {
		r.Status = Match
		add(f.Keys)
		return r
	}
	ts, err := Transitions(root)
	if err != nil {
		r.Status, r.Why = Malformed, err.Error()
		return r
	}
	sets := known(ts)
	if SHA(pin.Keys) == pin.SHA {
		sets[pin.SHA] = Set(pin.Keys)
	}
	sets[r.SHA] = Set(f.Keys)
	start, ok := sets[pin.SHA]
	if !ok {
		r.Status, r.Why = Unsigned, "trust.yml changed since it was pinned, and the pinned key set isn't recorded (trust.yml.sig)"
		return r
	}
	add(start)
	// Breadth first from the pin over valid signed changes.
	type step struct {
		sha  string
		path []string
	}
	queue := []step{{pin.SHA, nil}}
	seen := map[string]bool{pin.SHA: true}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.sha == r.SHA {
			r.Status, r.Steps = Advanced, len(cur.path)
			for _, s := range cur.path {
				add(sets[s])
			}
			return r
		}
		for _, t := range ts {
			if t.Prev != cur.sha || seen[t.SHA] || t.Sig == "" || SHA(t.Keys) != t.SHA {
				continue
			}
			if !slicesContain(sets[cur.sha], sign.KeyOnly(t.Signer)) || sign.Verify(t.Signer, t.payload(), t.Sig) != nil {
				continue
			}
			seen[t.SHA] = true
			queue = append(queue, step{t.SHA, append(append([]string(nil), cur.path...), t.SHA)})
		}
	}
	r.Status, r.Why = Unsigned, "trust.yml changed since it was pinned, and no change signed by a pinned key leads to it"
	return r
}
