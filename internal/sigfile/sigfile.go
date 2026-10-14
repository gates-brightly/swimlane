// Package sigfile is signed approval, kept in committed files under
// approvals/ so approvers anywhere can add to them. A file is an
// append-only, hash-linked log of approve and revoke events, each SSH-signed
// over the file's statement and the hash of the event before it: like
// commits, the order of approvals and revocations is the attestation. A
// signer's approval counts while their latest event is an approve.
//
// Two kinds:
//
//	approvals/lane.3.<sha12>.m<man8>.signature
//	                                    this exact lane script (its SHA-256)
//	                                    and execution manifest (internal/
//	                                    manifest): swim approve 3 --guards FLAG
//	approvals/auto.alice.signature      standing approval of any job alice
//	                                    queues (never consumed):
//	                                    swim approve --auto alice
//
// A guard counts the approvals for its manifest (or, from before
// manifests, for its script hash alone), plus the standing approvals
// for whoever signed the queue entry it runs from, and approves when the
// signers, named by lookup.yml, meet lookup.yml's rule for it.
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
	"github.com/gates-brightly/swimlane/internal/manifest"
	"github.com/gates-brightly/swimlane/internal/sign"
)

// Schema identifies an approvals file.
const Schema = "swim.approval/v1"

// Kinds.
const (
	Script   = "script"   // one exact lane script
	Standing = "standing" // any job queued by one user
)

// Event types.
const (
	Approve = "approve"
	Revoke  = "revoke"
)

// Event is one signed approve or revoke.
type Event struct {
	Type string `yaml:"type"`
	Name string `yaml:"name,omitempty"` // informational; trust comes from lookup.yml
	Key  string `yaml:"key"`
	At   string `yaml:"at"`   // informational: the order is what attests
	Prev string `yaml:"prev"` // hash of the event before (the statement's, for the first)
	Sig  string `yaml:"sig"`
}

// File is one approvals file.
type File struct {
	Schema string `yaml:"schema"`
	Kind   string `yaml:"kind"`

	// Script: the exact script approved.
	Script string `yaml:"script,omitempty"` // lane.N.sh
	SHA256 string `yaml:"script_sha256,omitempty"`
	Lane   int    `yaml:"lane,omitempty"`
	Job    string `yaml:"job,omitempty"`
	Round  string `yaml:"round,omitempty"`

	// Standing: whose queued jobs are approved.
	For string `yaml:"for,omitempty"` // a user in lookup.yml

	// Cells limits a script approval to these cells of a matrix round
	// (empty: every cell).
	Cells []string `yaml:"cells,omitempty"`
	// Evidence is the round's evidence hash this approval covers (the
	// output of its evidence steps, e.g. terraform plan); "" for a
	// script-only approval. A guard counts it only while the run's evidence
	// still matches.
	Evidence string `yaml:"evidence_sha256,omitempty"`
	// Manifest is the execution manifest approved (manifest_sha256): the
	// script and everything pinned with it. Pinned lists those entries so a
	// reviewer sees what they approve, and swim can say why an approval went
	// stale. A file without one (from before manifests) approves the script
	// hash alone.
	Manifest string           `yaml:"manifest_sha256,omitempty"`
	Pinned   *manifest.Pinned `yaml:"pinned,omitempty"`

	Approves []string `yaml:"approves,omitempty"` // guard flags
	All      bool     `yaml:"approves_all,omitempty"`
	Expires  string   `yaml:"expires,omitempty"`
	Note     string   `yaml:"note,omitempty"`
	Events   []Event  `yaml:"events"`

	Path string `yaml:"-"`
}

// Dir holds approvals files (committed, so approvers anywhere can add).
func Dir(root string) string { return filepath.Join(root, "approvals") }

// ScriptPath is the file approving lane n's script with hash sha and
// execution manifest man ("" for none).
func ScriptPath(root string, n int, sha, man string) string {
	return filepath.Join(Dir(root), fmt.Sprintf("lane.%d.%s%s.signature", n, sha[:12], manPart(man)))
}

// EvidencePath is the file approving lane n's script with hash sha and
// manifest man together with evidence ev (and, for a matrix round, one
// cell or "").
func EvidencePath(root string, n int, sha, man, ev, cell string) string {
	name := fmt.Sprintf("lane.%d.%s%s.e%s", n, sha[:12], manPart(man), ev[:8])
	if cell != "" {
		name += "." + strings.NewReplacer("=", "-", ",", ".", "/", "_").Replace(cell)
	}
	return filepath.Join(Dir(root), name+".signature")
}

// CellPath is the file approving one cell of lane n's matrix round.
func CellPath(root string, n int, sha, man, cell string) string {
	slug := strings.NewReplacer("=", "-", ",", ".", "/", "_").Replace(cell)
	return filepath.Join(Dir(root), fmt.Sprintf("lane.%d.%s%s.%s.signature", n, sha[:12], manPart(man), slug))
}

// manPart names the manifest in a file name: ".m<8 hex>", or nothing.
func manPart(man string) string {
	if len(man) < 8 {
		return ""
	}
	return ".m" + man[:8]
}

// ForCell keeps the files that cover cell (those limited to other cells
// go). With no cell (a plain lane), cell-limited files go.
func ForCell(files []*File, cell string) []*File {
	var out []*File
	for _, f := range files {
		if len(f.Cells) == 0 {
			out = append(out, f)
			continue
		}
		for _, c := range f.Cells {
			if c == cell {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

// StandingPath is the file of standing approvals for user's queued jobs.
func StandingPath(root, user string) string {
	return filepath.Join(Dir(root), "auto."+user+".signature")
}

// Statement is what every event signs, with its own type, prev, signer
// and time.
func (f *File) Statement() string {
	appr := strings.Join(f.Approves, ",")
	if f.All {
		appr = "all"
	}
	exp := f.Expires
	if exp == "" {
		exp = "never"
	}
	if f.Kind == Standing {
		return fmt.Sprintf("swim standing approval v1\nfor: %s\napproves: %s\nexpires: %s\n", f.For, appr, exp)
	}
	cells := ""
	if len(f.Cells) > 0 {
		cells = "cells: " + strings.Join(f.Cells, ";") + "\n" // only when limited, so older statements read the same
	}
	if f.Evidence != "" {
		cells += "evidence-sha256: " + f.Evidence + "\n"
	}
	if f.Manifest != "" {
		cells += "manifest-sha256: " + f.Manifest + "\n" // only when bound, so older statements read the same
	}
	return fmt.Sprintf("swim approval v1\nscript: %s\nscript-sha256: %s\nlane: %d\njob: %s\nround: %s\n%sapproves: %s\nexpires: %s\n",
		f.Script, f.SHA256, f.Lane, f.Job, f.Round, cells, appr, exp)
}

// StatementHash identifies the approval (one statement, many events).
func (f *File) StatementHash() string { return chain.Hash([]byte(f.Statement())) }

// EventPayload is what an event's signature covers.
func EventPayload(statement, typ, prev, key, at string) []byte {
	return []byte(statement + "event: " + typ + "\nprev: " + prev + "\nsigner: " + sign.KeyOnly(key) + "\nat: " + at + "\n")
}

func eventHash(statement string, e Event) string {
	return chain.Hash(append(EventPayload(statement, e.Type, e.Prev, e.Key, e.At), []byte(e.Sig)...))
}

// Add appends a signed event by k.
func (f *File) Add(k *sign.Key, typ, name string) error {
	st := f.Statement()
	prev := f.StatementHash()
	if n := len(f.Events); n > 0 {
		prev = eventHash(st, f.Events[n-1])
	}
	at := time.Now().UTC().Format(time.RFC3339)
	sig, err := sign.Sign(k, EventPayload(st, typ, prev, k.Public, at))
	if err != nil {
		return err
	}
	f.Events = append(f.Events, Event{Type: typ, Name: name, Key: k.Public, At: at, Prev: prev, Sig: sig})
	return nil
}

// Save writes the file.
func (f *File) Save() error {
	f.Schema = Schema
	data, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(f.Path, data, 0o644)
}

// Load reads an approvals file.
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
		return nil, fmt.Errorf("%s: not a swim approvals file (schema %q)", path, f.Schema)
	}
	f.Path = path
	return &f, nil
}

// All loads every approvals file; ones that don't parse are errors.
func All(root string) ([]*File, []error) {
	paths, _ := filepath.Glob(filepath.Join(Dir(root), "*.signature"))
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

// ForScript returns the script approvals for a script hash.
func ForScript(root, sha string) []*File {
	all, _ := All(root)
	var out []*File
	for _, f := range all {
		if f.Kind == Script && f.SHA256 == sha {
			out = append(out, f)
		}
	}
	return out
}

// ForManifest keeps the script approvals that still apply to manifest m:
// those bound to m, and those from before manifests (bound to the script
// hash alone). For each one bound to another manifest, stale says why:
// "lane.3.....signature: stale: config policy changed (swim.yml: ...)".
func ForManifest(files []*File, m *manifest.Manifest) (out []*File, stale []string) {
	for _, f := range files {
		if f.Kind != Script || f.Manifest == "" || m == nil || f.Manifest == m.SHA256 {
			out = append(out, f)
			continue
		}
		stale = append(stale, filepath.Base(f.Path)+": stale: "+f.StaleReason(m))
	}
	return out, stale
}

// StaleReason says how m differs from the manifest f approves.
func (f *File) StaleReason(m *manifest.Manifest) string {
	if f.Pinned == nil {
		return "the manifest changed since"
	}
	if why := manifest.Diff(*f.Pinned, m.Pinned); len(why) > 0 {
		return strings.Join(why, "; ")
	}
	return "the manifest changed since"
}

// StandingFor returns the standing approvals for user's queued jobs.
func StandingFor(root, user string) []*File {
	all, _ := All(root)
	var out []*File
	for _, f := range all {
		if f.Kind == Standing && f.For == user {
			out = append(out, f)
		}
	}
	return out
}

// Signer is one signer whose approval currently stands.
type Signer struct {
	Key  string
	User string // from lookup.yml; "" if the key isn't listed
	// Events are this signer's events (approve, revoke, approve, ...), for
	// the chain of custody.
	Events []Event
}

// Standing walks the events in order and returns the signers whose latest
// event is an approve, and the problems: a broken link or a bad signature
// invalidates that event and everything after it (the order can't be
// trusted past it).
func (f *File) Standing(l *lookup.Lookup) ([]Signer, []string) {
	st := f.Statement()
	prev := f.StatementHash()
	latest := map[string]string{}
	events := map[string][]Event{}
	var order []string
	var bad []string
	for i, e := range f.Events {
		if e.Prev != prev {
			bad = append(bad, fmt.Sprintf("event %d breaks the chain (an event before it was changed, removed or reordered); it and later events don't count", i+1))
			break
		}
		if err := sign.Verify(e.Key, EventPayload(st, e.Type, e.Prev, e.Key, e.At), e.Sig); err != nil {
			bad = append(bad, fmt.Sprintf("event %d (%s by %s): %v; it and later events don't count", i+1, e.Type, or(e.Name, sign.KeyOnly(e.Key)), err))
			break
		}
		k := sign.KeyOnly(e.Key)
		if _, seen := latest[k]; !seen {
			order = append(order, k)
		}
		latest[k] = e.Type
		events[k] = append(events[k], e)
		prev = eventHash(st, e)
	}
	var out []Signer
	for _, k := range order {
		if latest[k] != Approve {
			continue
		}
		user, _ := l.Who(k)
		out = append(out, Signer{Key: events[k][len(events[k])-1].Key, User: user, Events: events[k]})
	}
	return out, bad
}

// HasApproved reports whether key's latest event is an approve.
func (f *File) HasApproved(l *lookup.Lookup, key string) bool {
	signers, _ := f.Standing(l)
	for _, s := range signers {
		if sign.KeyOnly(s.Key) == sign.KeyOnly(key) {
			return true
		}
	}
	return false
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

// Grant is one file's standing signers for a guard, ready to count.
type Grant struct {
	File    *File
	Signers []Signer // known to lookup.yml, approval standing
}

// Collect returns, for flag, each file's standing known signers and why
// the others don't count (expired, not covering it, unknown keys, broken).
func Collect(files []*File, l *lookup.Lookup, flag string, now time.Time) ([]Grant, []string) {
	var grants []Grant
	var why []string
	for _, f := range files {
		name := filepath.Base(f.Path)
		switch {
		case !f.Covers(flag):
			continue
		case f.Expired(now):
			why = append(why, name+" expired")
			continue
		}
		signers, bad := f.Standing(l)
		for _, b := range bad {
			why = append(why, name+": "+b)
		}
		var known []Signer
		unknown := 0
		for _, s := range signers {
			if s.User == "" {
				unknown++
				continue
			}
			known = append(known, s)
		}
		if unknown > 0 {
			why = append(why, fmt.Sprintf("%s: %d signer(s) not in lookup.yml", name, unknown))
		}
		if len(known) > 0 {
			grants = append(grants, Grant{File: f, Signers: known})
		}
	}
	return grants, why
}

// Users are the distinct users behind grants.
func Users(grants []Grant) []string {
	seen := map[string]bool{}
	var out []string
	for _, g := range grants {
		for _, s := range g.Signers {
			if !seen[s.User] {
				seen[s.User] = true
				out = append(out, s.User)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Decision is whether a set of approvals approves one guard.
type Decision struct {
	OK     bool
	Users  []string
	Grants []Grant
	Why    string
}

// Decide counts the standing approvals in files for flag against
// lookup.yml's rule for it.
func Decide(files []*File, l *lookup.Lookup, flag string, now time.Time) Decision {
	if l.Path == "" {
		return Decision{Why: "no lookup.yml: approvals need their signers' keys listed there to count"}
	}
	grants, why := Collect(files, l, flag, now)
	users := Users(grants)
	if ok, need := l.Satisfied(l.RuleFor(flag), users); !ok {
		msg := "approval " + need
		if len(why) > 0 {
			msg += " (" + strings.Join(why, "; ") + ")"
		}
		return Decision{Why: msg, Users: users, Grants: grants}
	}
	return Decision{OK: true, Users: users, Grants: grants}
}

// VerifyEvent checks one event's signature, as recorded in the chain.
func VerifyEvent(statement string, e Event) error {
	return sign.Verify(e.Key, EventPayload(statement, e.Type, e.Prev, e.Key, e.At), e.Sig)
}

// ErrNoScript is returned when the script to approve doesn't exist.
var ErrNoScript = errors.New("no such lane script")

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
