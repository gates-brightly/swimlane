// Package authority decides whether a guard is approved by what swim can
// check without asking anyone: signed approvals (approvals/, counted
// against lookup.yml) and, under swim run, a guard flag set in the
// environment swim run started with. It also records approvals in the
// chain with the runner's key.
//
// Under swim run the launcher decides, from a Snapshot it takes when the
// run starts: what a lane writes to approvals/ or lookup.yml, or sets in
// its own environment, after that doesn't count. A lane run directly with
// bash decides for itself, from a snapshot taken as it asks (there is no
// one else to ask).
package authority

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/evidence"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/manifest"
	"github.com/gates-brightly/swimlane/internal/queue"
	"github.com/gates-brightly/swimlane/internal/sigfile"
	"github.com/gates-brightly/swimlane/internal/sign"
)

// The runner's signing key, loaded once per process.
var (
	keyOnce sync.Once
	keyVal  *sign.Key
	keyErr  error
)

// Key is the key this swim signs chain entries with: nil (and no error)
// when signing is off, or auto and no key loads.
func Key(cfg *config.Config) (*sign.Key, error) {
	mode := cfg.SigningMode()
	if mode == config.SigningOff {
		return nil, nil
	}
	keyOnce.Do(func() {
		if !sign.Available() {
			keyErr = fmt.Errorf("ssh-keygen isn't installed")
			return
		}
		keyVal, keyErr = sign.LoadKey(cfg.SigningKey)
	})
	if keyErr != nil {
		if mode == config.SigningRequired {
			return nil, fmt.Errorf("signing: required, but %v", keyErr)
		}
		return nil, nil
	}
	return keyVal, nil
}

// Record appends an entry to the chain, signed by the runner's key when
// there is one, and returns its hash. The chain is best-effort for the
// round (a failure is reported, not fatal) unless signing is required.
func Record(root string, cfg *config.Config, e chain.Entry) (string, error) {
	key, err := Key(cfg)
	if err != nil {
		return "", err
	}
	return RecordWith(root, cfg, key, e)
}

// RecordWith is Record signed with key (swim run's run key) instead of
// the runner's.
func RecordWith(root string, cfg *config.Config, key *sign.Key, e chain.Entry) (string, error) {
	hash, err := chain.Append(root, e, key)
	if errors.As(err, new(chain.SignError)) && cfg.SigningMode() != config.SigningRequired {
		// signing: auto and the key couldn't sign (a passphrase and no
		// agent, a hardware key that wants a PIN): keep the link, unsigned.
		fmt.Fprintln(os.Stderr, "swim: warning: chain of custody: couldn't sign ("+err.Error()+"); recorded unsigned")
		hash, err = chain.Append(root, e, nil)
	}
	if err != nil {
		if cfg.SigningMode() == config.SigningRequired {
			return "", err
		}
		fmt.Fprintln(os.Stderr, "swim: warning: chain of custody: "+err.Error())
	}
	return hash, nil
}

// Snapshot is what approves guards without a question, as it stood when
// it was taken.
type Snapshot struct {
	Lookup    *lookup.Lookup
	LookupErr error
	// LookupSHA is the SHA-256 of the lookup.yml it read (approve entries
	// record it: swim verify checks them against those rules).
	LookupSHA string
	Files     []*sigfile.File // every approvals file that parsed
	// Env is the environment: guard flags set to 1, and a queued batch's
	// SWIM_QUEUE, SWIM_QUEUE_APPROVED and SWIM_QUEUE_ENTRY.
	Env map[string]string
	// Queue is the queue entry the run came from (SWIM_QUEUE_ENTRY), if any.
	Queue *queue.Entry
}

// Take snapshots root's approvals and lookup.yml, and env.
func Take(root string, env []string) *Snapshot {
	s := &Snapshot{Env: map[string]string{}}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			s.Env[k] = v
		}
	}
	s.Lookup, s.LookupErr = lookup.Load(root)
	s.LookupSHA, _ = chain.HashFile(filepath.Join(root, lookup.FileName))
	s.Files, _ = sigfile.All(root)
	if p := s.Env["SWIM_QUEUE_ENTRY"]; p != "" && filepath.Dir(p) == queue.Dir(root) {
		s.Queue, _ = queue.Load(p)
	}
	return s
}

// Watch reports whether the run is a batch swim watch took from the queue.
func (s *Snapshot) Watch() bool { return s.Env["SWIM_QUEUE"] != "" }

// Ask is one guard to decide.
type Ask struct {
	Root     string
	Lane     int
	Flag     string
	Desc     string // the lane's description (for the evidence WARN)
	Job, Run string
	Script   string             // the script's SHA-256
	Manifest *manifest.Manifest // nil for none
}

// Result is a decision.
type Result struct {
	OK bool
	// How is the APPROVED line's detail ("approved by ... (signed)").
	How string
	// Entry is the approve entry to record (OK only): method, approvers,
	// evidence; the caller adds the lane's facts.
	Entry chain.Entry
	// History is what .swim.log says of it (OK only).
	History string
	// Why the signed approvals didn't count ("" if none apply).
	Why string
	// Warns are WARN lines for the lane's log (evidence that changed).
	Warns []approval.Warn
	// The run's evidence, and whether the guard's rule needs approvals
	// bound to it (shown with a question).
	Evidence         *evidence.Index
	EvidenceText     string
	EvidenceRequired bool
}

// FlagSet decides a guard whose flag the lane has set to 1: it approves
// only when the snapshot's environment (swim run's own) set it too, or the
// queued batch approved it.
func (s *Snapshot) FlagSet(flag string) Result {
	if s.Env[flag] != "1" {
		return Result{Why: flag + "=1 was set inside the lane, not in the environment swim run started with"}
	}
	how, method := flag+"=1", "flag"
	if strings.Contains(" "+s.Env["SWIM_QUEUE_APPROVED"]+" ", " "+flag+" ") {
		how, method = how+", approved in "+s.Env["SWIM_QUEUE"], "queue"
	}
	return Result{OK: true, How: how, Entry: chain.Entry{Method: method, Statement: how}}
}

// Signed decides a guard by the signed approvals in the snapshot: those
// of the exact manifest (or, from before manifests, of the script),
// plus the standing approvals of whoever signed the queue entry the
// run came from, counted together against lookup.yml; an approval bound
// to evidence counts only while this run's evidence matches.
func (s *Snapshot) Signed(a Ask, now time.Time) Result {
	var r Result
	// This run's evidence (its evidence steps' output so far), and whether
	// the guard's rule needs approvals bound to it. Batches run by swim
	// watch don't check evidence: unattended, a drifted plan would turn every
	// queued change into a dry run with no one to re-approve.
	if ev := evidence.Load(a.Root, a.Job); ev != nil && ev.Run == a.Run {
		r.Evidence = ev
		r.EvidenceText, _ = ev.Text(a.Root)
	}
	watch := s.Watch()
	if s.LookupErr != nil {
		r.Why = s.LookupErr.Error()
		return r
	}
	lk := s.Lookup
	r.EvidenceRequired = lk.RuleFor(a.Flag).Evidence && !watch
	var files []*sigfile.File
	for _, f := range s.Files {
		if f.Kind == sigfile.Script && f.SHA256 == a.Script {
			files = append(files, f)
		}
	}
	files = sigfile.Unlimited(files)
	files, stale := sigfile.ForManifest(files, a.Manifest)
	files, r.Warns = s.evidenceFilter(a, files, r.Evidence, r.EvidenceText, r.EvidenceRequired, watch)
	queuer := s.queuer(a.Lane, a.Script)
	if queuer != "" {
		for _, f := range s.Files {
			if f.Kind == sigfile.Standing && f.For == queuer {
				files = append(files, f)
			}
		}
	}
	relevant := false
	for _, f := range files {
		relevant = relevant || f.Covers(a.Flag)
	}
	if !relevant {
		if len(stale) > 0 {
			// Approvals of this script bound to another manifest: say why.
			r.Why = strings.Join(stale, "; ")
			return r
		}
		// An approval of another version of this script: say so.
		for _, f := range s.Files {
			if f.Kind == sigfile.Script && f.Lane == a.Lane && f.SHA256 != a.Script && f.Covers(a.Flag) {
				r.Why = fmt.Sprintf("%s approves another version of %s (approved %s, running %s)", filepath.Base(f.Path), lane.ScriptName(a.Root, a.Lane), short12(f.SHA256), short12(a.Script))
			}
		}
		return r
	}
	d := sigfile.Decide(files, lk, a.Flag, now)
	if !d.OK {
		r.Why = d.Why
		if len(stale) > 0 {
			r.Why += "; " + strings.Join(stale, "; ")
		}
		return r
	}
	var names []string
	for _, u := range d.Users {
		names = append(names, lk.Describe(u))
	}
	var approvers []chain.Approver
	methods := map[string]bool{}
	for _, g := range d.Grants {
		methods[g.File.Kind] = true
		for _, sg := range g.Signers {
			ev := sg.Events[len(sg.Events)-1]
			approvers = append(approvers, chain.Approver{Name: sg.User, Key: sg.Key, Statement: g.File.Statement(), Prev: ev.Prev, SignedAt: ev.At, Sig: ev.Sig})
		}
	}
	how := "approved by " + strings.Join(names, ", ")
	method := "signature"
	switch {
	case methods[sigfile.Standing] && methods[sigfile.Script]:
		method, how = "signature+standing", how+" (signed, and standing for jobs queued by "+queuer+")"
	case methods[sigfile.Standing]:
		method, how = "standing", how+" (standing approval for jobs queued by "+queuer+")"
	default:
		how += " (signed)"
	}
	evCovered := ""
	for _, g := range d.Grants {
		if g.File.Evidence != "" {
			evCovered = g.File.Evidence
		}
	}
	if evCovered != "" {
		how += "; evidence " + short12(evCovered)
	}
	if watch {
		for _, f := range files {
			if f.Evidence != "" {
				how += "; evidence not checked (watch)"
				break
			}
		}
	}
	r.OK, r.How = true, how
	r.Entry = chain.Entry{Method: method, Approvers: approvers, Evidence: evCovered}
	r.History = method + ": " + strings.Join(d.Users, ", ")
	return r
}

// evidenceFilter keeps the signed approvals that still apply given this
// run's evidence: one bound to evidence counts only while the evidence
// matches (a WARN and a diff in the log when it doesn't), and when the
// guard's rule requires evidence a script-only approval doesn't count. Under
// swim watch nothing is compared.
func (s *Snapshot) evidenceFilter(a Ask, files []*sigfile.File, live *evidence.Index, liveText string, required, watch bool) ([]*sigfile.File, []approval.Warn) {
	if watch {
		return files, nil
	}
	var out []*sigfile.File
	var warns []approval.Warn
	for _, f := range files {
		switch {
		case f.Evidence != "" && (live == nil || f.Evidence != live.Hash):
			now := "no evidence this run"
			if live != nil {
				now = short12(live.Hash)
			}
			w := approval.Warn{Label: "evidence changed since approval: " + a.Desc,
				Detail: fmt.Sprintf("%s approved evidence %s; this run has %s", filepath.Base(f.Path), short12(f.Evidence), now)}
			if old := evidence.Blob(a.Root, f.Evidence); old != "" && liveText != "" {
				w.Diff = evidence.Diff(old, liveText, 40)
			}
			warns = append(warns, w)
		case required && f.Evidence == "":
			// Script-only: doesn't count where the rule needs the plan approved.
		default:
			out = append(out, f)
		}
	}
	return out, warns
}

// queuer is who queued the batch this run came from, proven by their SSH
// signature on the queue entry and lookup.yml; "" if there's no such
// entry, it isn't signed by a listed user, or it doesn't hold lane n's
// exact script.
func (s *Snapshot) queuer(n int, sha string) string {
	e := s.Queue
	if e == nil || e.QueueSig == "" || !e.Has(n, sha) {
		return ""
	}
	if sign.Verify(e.QueuedKey, []byte(e.Statement()), e.QueueSig) != nil {
		return ""
	}
	user, _ := s.Lookup.Who(e.QueuedKey)
	return user
}

func short12(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
