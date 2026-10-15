package lane

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
}

// ExpiresAt is when the claim runs out.
func (c *Claim) ExpiresAt() time.Time {
	t, _ := time.Parse(time.RFC3339, c.Expires)
	return t
}

// ClaimDir holds the claims.
func ClaimDir(root string) string { return filepath.Join(root, ".swim", "claims") }

func claimPathOf(root string, n int) string {
	return filepath.Join(ClaimDir(root), fmt.Sprintf("lane.%d.yml", n))
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

// ErrClaimed is returned when another session holds a lane's live claim.
type ErrClaimed struct {
	Lane int
	C    *Claim
}

func (e ErrClaimed) Error() string {
	until := e.C.ExpiresAt().Local().Format("15:04")
	job := ""
	if e.C.Job != "" {
		job = fmt.Sprintf(" (job %s)", e.C.Job)
	}
	return fmt.Sprintf("swim %d is claimed by %s (session %s on %s) until %s%s; pick another lane, wait, or pass --force\n(set SWIM_SESSION to tell sessions apart)",
		e.Lane, e.C.Owner, e.C.Session, e.C.Host, until, job)
}

// ReadClaim is lane n's live claim, or nil (none, or expired: an expired
// one is removed).
func ReadClaim(root string, n int) *Claim {
	p := claimPathOf(root, n)
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

// CheckClaim refuses (ErrClaimed) when a session other than sess holds
// lane n's live claim.
func CheckClaim(root string, n int, sess string) error {
	if c := ReadClaim(root, n); c != nil && c.Session != sess {
		return ErrClaimed{n, c}
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

func writeClaim(root string, n int, c Claim) error {
	data, _ := yaml.Marshal(c)
	return fsutil.WriteAtomic(claimPathOf(root, n), data, 0o644)
}

// TakeClaim takes lane n's claim for session sess, for lease, or refreshes
// it if sess holds it already. Another session's live claim refuses it
// (ErrClaimed) unless force, which takes it over and returns the claim it
// broke.
func TakeClaim(root string, n int, sess string, lease time.Duration, job string, force bool) (broke *Claim, err error) {
	unlock, err := claimsLock(root)
	if err != nil {
		return nil, err
	}
	defer unlock()
	cur := ReadClaim(root, n)
	if cur != nil && cur.Session != sess {
		if !force {
			return nil, ErrClaimed{n, cur}
		}
		broke = cur
	}
	now := time.Now().UTC()
	c := Claim{Owner: ClaimOwner(), Session: sess, Created: now.Format(time.RFC3339), Expires: now.Add(lease).Format(time.RFC3339), Job: job}
	c.Host, _ = os.Hostname()
	if cur != nil && cur.Session == sess {
		c.Created = cur.Created // a refresh keeps when it was taken
		if job == "" {
			c.Job = cur.Job
		}
	}
	return broke, writeClaim(root, n, c)
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
	c.Expires = time.Now().UTC().Add(lease).Format(time.RFC3339)
	return writeClaim(root, n, *c) == nil
}

// ReleaseClaim gives up lane n's claim: session sess's own, or another's
// with force (returned, so it can be recorded). No claim is no error.
func ReleaseClaim(root string, n int, sess string, force bool) (*Claim, error) {
	unlock, err := claimsLock(root)
	if err != nil {
		return nil, err
	}
	defer unlock()
	c := ReadClaim(root, n)
	if c == nil {
		return nil, nil
	}
	if c.Session != sess && !force {
		return nil, ErrClaimed{n, c}
	}
	if err := os.Remove(claimPathOf(root, n)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return c, nil
}

// DropClaim removes lane n's claim, whoever holds it: the round started
// (running-lane protection takes over from here), or it was archived or
// stubbed after the claim was checked.
func DropClaim(root string, n int) { os.Remove(claimPathOf(root, n)) }
