package lane

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/fsutil"
)

// Lane claims: an advisory lease a session takes on a lane while it writes
// the lane's round (swim new, swim claim), so another session's swim new,
// stub and archive refuse the lane until it's released, the round starts,
// or the lease runs out. Claims guard swim's own commands only: an editor
// writing lane.N.sh isn't stopped. They live in .swim/claims/, one file
// per lane, and don't cross machines or worktrees.

// DefaultClaimLease is how long a claim lasts unless refreshed.
const DefaultClaimLease = 2 * time.Hour

// Claim is one lane's lease.
type Claim struct {
	Owner   string `yaml:"owner"`   // the user .swim.log records
	Session string `yaml:"session"` // SWIM_SESSION, else user@host
	Host    string `yaml:"host"`
	Created string `yaml:"created"`
	Expires string `yaml:"expires"`
	Job     string `yaml:"job,omitempty"`
	// AssignedBy is the session that took the claim for Session (swim
	// claim N --session NAME): a planner assigning a worker its lane.
	// Empty when the session took it itself.
	AssignedBy string `yaml:"assigned_by,omitempty"`
}

// ExpiresAt is when the claim runs out.
func (c *Claim) ExpiresAt() time.Time {
	t, _ := time.Parse(time.RFC3339, c.Expires)
	return t
}

// expiresIn is a claim's expiry lease after now, rounded up to the second
// it is written to: rounded down, a short lease (--for 1s) could run out
// before the claim is read back.
func expiresIn(now time.Time, lease time.Duration) string {
	t := now.Add(lease)
	if r := t.Truncate(time.Second); r.Before(t) {
		t = r.Add(time.Second)
	}
	return t.UTC().Format(time.RFC3339)
}

// ClaimDir holds the claims.
func ClaimDir(root string) string { return filepath.Join(root, ".swim", "claims") }

// ClaimKey is what a claim is on: a lane (Lane > 0), or a named resource
// (swim claim lith): something sessions share outside any one lane, such
// as a local environment several agents' lanes rebuild. A resource claim
// is advisory, like a lane's: swim claim refuses another session's, and a
// run whose # Locks: name it warns, but runs.
type ClaimKey struct {
	Lane int
	Name string
}

// LaneClaim is lane n's key; ResourceClaim is a resource's.
func LaneClaim(n int) ClaimKey           { return ClaimKey{Lane: n} }
func ResourceClaim(name string) ClaimKey { return ClaimKey{Name: name} }

// String names it as messages do: "swim 3", or the resource.
func (k ClaimKey) String() string {
	if k.Lane > 0 {
		return fmt.Sprintf("swim %d", k.Lane)
	}
	return k.Name
}

const resourcePrefix = "res."

func (k ClaimKey) path(root string) string {
	if k.Lane > 0 {
		return filepath.Join(ClaimDir(root), fmt.Sprintf("lane.%d.yml", k.Lane))
	}
	return filepath.Join(ClaimDir(root), resourcePrefix+strings.ReplaceAll(k.Name, "/", "%2F")+".yml")
}

// SessionID names this session: SWIM_SESSION, else user@host. Two sessions
// under one account on one machine share it unless they set SWIM_SESSION.
func SessionID() string {
	if s := os.Getenv("SWIM_SESSION"); s != "" {
		return s
	}
	h, _ := os.Hostname()
	return ClaimOwner() + "@" + h
}

// ClaimOwner is the name .swim.log records for this user.
func ClaimOwner() string {
	for _, k := range []string{"SWIM_USER", "USER", "LOGNAME"} {
		if u := os.Getenv(k); u != "" {
			return u
		}
	}
	return "-"
}

// ErrClaimed is returned when another session holds a live claim: on lane
// Lane, or on resource Name.
type ErrClaimed struct {
	Lane int
	Name string
	C    *Claim
}

func errClaimed(k ClaimKey, c *Claim) ErrClaimed { return ErrClaimed{Lane: k.Lane, Name: k.Name, C: c} }

func (e ErrClaimed) Error() string {
	until := e.C.ExpiresAt().Local().Format("15:04")
	job := ""
	if e.C.Job != "" {
		job = fmt.Sprintf(" (job %s)", e.C.Job)
	}
	if e.Lane == 0 {
		return fmt.Sprintf("%s is claimed by %s (session %s on %s) until %s; wait, or ask that session to release it\n(set SWIM_SESSION to tell sessions apart)",
			e.Name, e.C.Owner, e.C.Session, e.C.Host, until)
	}
	return fmt.Sprintf("swim %d is claimed by %s (session %s on %s) until %s%s; pick another lane, wait, or pass --force\n(set SWIM_SESSION to tell sessions apart)",
		e.Lane, e.C.Owner, e.C.Session, e.C.Host, until, job)
}

// ReadClaim is lane n's live claim, or nil (none, or expired: an expired
// one is removed).
func ReadClaim(root string, n int) *Claim { return ReadClaimOn(root, LaneClaim(n)) }

// ReadClaimOn is k's live claim, or nil.
func ReadClaimOn(root string, k ClaimKey) *Claim {
	p := k.path(root)
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var c Claim
	if yaml.Unmarshal(data, &c) != nil || c.Session == "" {
		return nil
	}
	if !time.Now().Before(c.ExpiresAt()) {
		os.Remove(p)
		return nil
	}
	return &c
}

// NamedClaim is a resource's live claim.
type NamedClaim struct {
	Name string
	*Claim
}

// ResourceClaims are the live resource claims, by name.
func ResourceClaims(root string) []NamedClaim {
	files, _ := filepath.Glob(filepath.Join(ClaimDir(root), resourcePrefix+"*.yml"))
	sort.Strings(files)
	var out []NamedClaim
	for _, p := range files {
		name := strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), resourcePrefix), ".yml"), "%2F", "/")
		if c := ReadClaimOn(root, ResourceClaim(name)); c != nil {
			out = append(out, NamedClaim{name, c})
		}
	}
	return out
}

// CheckClaim refuses (ErrClaimed) when a session other than sess holds
// lane n's live claim.
func CheckClaim(root string, n int, sess string) error {
	if c := ReadClaim(root, n); c != nil && c.Session != sess {
		return errClaimed(LaneClaim(n), c)
	}
	return nil
}

// claimsLock serialises claim changes: .swim/claims/.lock, not under
// .swim/locks/, where a resource lock named "claims" would share it.
func claimsLock(root string) (func(), error) {
	if err := os.MkdirAll(ClaimDir(root), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(ClaimDir(root), ".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

func writeClaim(root string, k ClaimKey, c Claim) error {
	data, _ := yaml.Marshal(c)
	return fsutil.WriteAtomic(k.path(root), data, 0o644)
}

// TakeClaim takes lane n's claim for session sess, for lease, or refreshes
// it if sess holds it already. Another session's live claim refuses it
// (ErrClaimed) unless force, which takes it over and returns the claim it
// broke. It returns the claim it wrote, too.
func TakeClaim(root string, n int, sess string, lease time.Duration, job string, force bool) (taken, broke *Claim, err error) {
	return TakeClaimFor(root, n, sess, "", lease, job, force)
}

// TakeClaimFor is TakeClaim, taken by session by on sess's behalf (by ""
// for sess itself). A refresh by sess keeps who assigned it.
func TakeClaimFor(root string, n int, sess, by string, lease time.Duration, job string, force bool) (taken, broke *Claim, err error) {
	return TakeClaimOn(root, LaneClaim(n), sess, by, lease, job, force)
}

// TakeClaimOn is TakeClaimFor on any key, a lane's or a resource's.
func TakeClaimOn(root string, k ClaimKey, sess, by string, lease time.Duration, job string, force bool) (taken, broke *Claim, err error) {
	unlock, err := claimsLock(root)
	if err != nil {
		return nil, nil, err
	}
	defer unlock()
	cur := ReadClaimOn(root, k)
	if cur != nil && cur.Session != sess {
		if !force {
			return nil, nil, errClaimed(k, cur)
		}
		broke = cur
	}
	now := time.Now().UTC()
	c := Claim{Owner: ClaimOwner(), Session: sess, Created: now.Format(time.RFC3339), Expires: expiresIn(now, lease), Job: job, AssignedBy: by}
	c.Host, _ = os.Hostname()
	if cur != nil && cur.Session == sess {
		c.Created = cur.Created // a refresh keeps when it was taken
		if job == "" {
			c.Job = cur.Job
		}
		if by == "" {
			c.AssignedBy = cur.AssignedBy // and who assigned it
		}
	}
	if err := writeClaim(root, k, c); err != nil {
		return nil, broke, err
	}
	return &c, broke, nil
}

// RefreshClaim moves lane n's claim's expiry out by lease, only if session
// sess holds it; it never creates one. It reports whether it refreshed.
func RefreshClaim(root string, n int, sess string, lease time.Duration) bool {
	if c := ReadClaim(root, n); c == nil || c.Session != sess {
		return false
	}
	unlock, err := claimsLock(root)
	if err != nil {
		return false
	}
	defer unlock()
	c := ReadClaim(root, n)
	if c == nil || c.Session != sess {
		return false
	}
	c.Expires = expiresIn(time.Now().UTC(), lease)
	return writeClaim(root, LaneClaim(n), *c) == nil
}

// ReleaseClaim gives up lane n's claim: session sess's own, or another's
// with force (returned, so it can be recorded). No claim is no error.
func ReleaseClaim(root string, n int, sess string, force bool) (*Claim, error) {
	return ReleaseClaimOn(root, LaneClaim(n), sess, force)
}

// ReleaseClaimOn is ReleaseClaim on any key.
func ReleaseClaimOn(root string, k ClaimKey, sess string, force bool) (*Claim, error) {
	unlock, err := claimsLock(root)
	if err != nil {
		return nil, err
	}
	defer unlock()
	c := ReadClaimOn(root, k)
	if c == nil {
		return nil, nil
	}
	if c.Session != sess && !force {
		return nil, errClaimed(k, c)
	}
	if err := os.Remove(k.path(root)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return c, nil
}

// DropClaim removes lane n's claim, whoever holds it: the round started
// (running-lane protection takes over from here), or it was archived or
// stubbed after the claim was checked.
func DropClaim(root string, n int) { os.Remove(LaneClaim(n).path(root)) }
