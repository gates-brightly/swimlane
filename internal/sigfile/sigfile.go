// Package sigfile is remote approval: a .signature file states which guards
// of one exact lane script are approved (by the script's SHA-256), and
// carries an SSH signature from each signatory over that statement. Anyone
// with the repo can make or co-sign one (swim sign) and drop it beside the
// script (lane.N.sh.signature) or in .swim/signatures/; when the lane's
// guard runs, the signature approves it if its signers, looked up in
// lookup.yml, meet the guard's rule.
package sigfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/sign"
)

// Schema identifies a signature file.
const Schema = "swim.signature/v1"

// Signatory is one approver's signature.
type Signatory struct {
	Name     string `yaml:"name,omitempty"` // informational; trust comes from lookup.yml
	Key      string `yaml:"key"`
	SignedAt string `yaml:"signed_at"`
	Sig      string `yaml:"sig"`
}

// File is a .signature file.
type File struct {
	Schema      string      `yaml:"schema"`
	Script      string      `yaml:"script"` // lane.N.sh
	SHA256      string      `yaml:"script_sha256"`
	Lane        int         `yaml:"lane"`
	Job         string      `yaml:"job,omitempty"`
	Round       string      `yaml:"round,omitempty"`
	Approves    []string    `yaml:"approves,omitempty"` // guard flags
	All         bool        `yaml:"approves_all,omitempty"`
	Expires     string      `yaml:"expires,omitempty"` // RFC3339; empty: never
	Note        string      `yaml:"note,omitempty"`
	Signatories []Signatory `yaml:"signatories"`

	Path string `yaml:"-"`
}

// Statement is what every signatory signs (plus their key and time).
func (f *File) Statement() string {
	appr := strings.Join(f.Approves, ",")
	if f.All {
		appr = "all"
	}
	exp := f.Expires
	if exp == "" {
		exp = "never"
	}
	return fmt.Sprintf("swim approval v1\nscript: %s\nscript-sha256: %s\nlane: %d\njob: %s\nround: %s\napproves: %s\nexpires: %s\n",
		f.Script, f.SHA256, f.Lane, f.Job, f.Round, appr, exp)
}

// StatementHash identifies the statement (one approval, however many sign).
func (f *File) StatementHash() string { return chain.Hash([]byte(f.Statement())) }

func signed(statement, key, at string) []byte {
	return []byte(statement + "signer: " + sign.KeyOnly(key) + "\nsigned-at: " + at + "\n")
}

// Sign adds k's signature (replacing an earlier one by the same key).
func (f *File) Sign(k *sign.Key, name string) error {
	at := time.Now().UTC().Format(time.RFC3339)
	sig, err := sign.Sign(k, signed(f.Statement(), k.Public, at))
	if err != nil {
		return err
	}
	var keep []Signatory
	for _, s := range f.Signatories {
		if sign.KeyOnly(s.Key) != sign.KeyOnly(k.Public) {
			keep = append(keep, s)
		}
	}
	f.Signatories = append(keep, Signatory{Name: name, Key: k.Public, SignedAt: at, Sig: sig})
	return nil
}

// Save writes the file.
func (f *File) Save() error {
	f.Schema = Schema
	data, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	return os.WriteFile(f.Path, data, 0o644)
}

// Load reads a signature file.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if f.Schema != Schema {
		return nil, fmt.Errorf("%s: not a swim signature file (schema %q)", path, f.Schema)
	}
	f.Path = path
	return &f, nil
}

// Dir holds signature files dropped into the repo's state.
func Dir(root string) string { return filepath.Join(root, ".swim", "signatures") }

// All loads every signature file: *.signature at the repo root (beside the
// lane scripts) and in .swim/signatures/. Files that don't parse are
// returned as errors, not skipped silently.
func All(root string) ([]*File, []error) {
	var paths []string
	for _, dir := range []string{root, Dir(root)} {
		m, _ := filepath.Glob(filepath.Join(dir, "*.signature"))
		paths = append(paths, m...)
	}
	sort.Strings(paths)
	var out []*File
	var errs []error
	for _, p := range paths {
		f, err := Load(p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, f)
	}
	return out, errs
}

// ForScript returns the signature files for a script hash.
func ForScript(root, sha string) []*File {
	all, _ := All(root)
	var out []*File
	for _, f := range all {
		if f.SHA256 == sha {
			out = append(out, f)
		}
	}
	return out
}

// Verified is a signatory whose signature checks out.
type Verified struct {
	Signatory
	User string // from lookup.yml; "" if the key isn't listed
}

// Verify checks every signature; it returns the valid ones and the
// problems with the rest.
func (f *File) Verify(l *lookup.Lookup) ([]Verified, []string) {
	var ok []Verified
	var bad []string
	st := f.Statement()
	for _, s := range f.Signatories {
		if err := sign.Verify(s.Key, signed(st, s.Key, s.SignedAt), s.Sig); err != nil {
			bad = append(bad, fmt.Sprintf("%s: %v", or(s.Name, sign.KeyOnly(s.Key)), err))
			continue
		}
		user, _ := l.Who(s.Key)
		ok = append(ok, Verified{Signatory: s, User: user})
	}
	return ok, bad
}

// Covers reports whether the file approves flag (by name or all).
func (f *File) Covers(flag string) bool {
	if f.All {
		return true
	}
	for _, a := range f.Approves {
		if a == flag {
			return true
		}
	}
	return false
}

// Expired reports whether the approval has run out.
func (f *File) Expired(now time.Time) bool {
	if f.Expires == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, f.Expires)
	return err != nil || now.After(t)
}

// Decision is whether a signature file approves one guard.
type Decision struct {
	OK     bool
	Users  []string // distinct known signers that count
	Signed []Verified
	Why    string // when not OK
}

// Decide says whether f approves flag now, under lookup.yml's rule for it.
func (f *File) Decide(l *lookup.Lookup, flag string, now time.Time) Decision {
	if !f.Covers(flag) {
		return Decision{Why: fmt.Sprintf("%s doesn't approve %s", filepath.Base(f.Path), flag)}
	}
	if f.Expired(now) {
		return Decision{Why: fmt.Sprintf("%s expired at %s", filepath.Base(f.Path), f.Expires)}
	}
	if l.Path == "" {
		return Decision{Why: "no lookup.yml: signatures need their keys listed there to count"}
	}
	verified, bad := f.Verify(l)
	seen := map[string]bool{}
	var users []string
	var unknown []string
	for _, v := range verified {
		if v.User == "" {
			unknown = append(unknown, sign.KeyOnly(v.Key))
			continue
		}
		if !seen[v.User] {
			seen[v.User] = true
			users = append(users, v.User)
		}
	}
	sort.Strings(users)
	rule := l.RuleFor(flag)
	if ok, why := l.Satisfied(rule, users); !ok {
		if len(unknown) > 0 {
			why += fmt.Sprintf("; %d signer(s) not in lookup.yml", len(unknown))
		}
		if len(bad) > 0 {
			why += "; bad signature: " + strings.Join(bad, "; ")
		}
		return Decision{Why: filepath.Base(f.Path) + " " + why, Signed: verified}
	}
	return Decision{OK: true, Users: users, Signed: verified}
}

// ErrNoScript is returned when the script to sign doesn't exist.
var ErrNoScript = errors.New("no such lane script")

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// VerifyStatement checks one signatory's signature over a statement, as
// recorded in the chain (so verify needs no .signature file).
func VerifyStatement(statement, key, signedAt, sig string) error {
	return sign.Verify(key, signed(statement, key, signedAt), sig)
}
