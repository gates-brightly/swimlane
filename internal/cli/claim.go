package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
)

// swim claim takes, refreshes or gives up a claim on a lane, or on a named
// resource (swim claim lith). A lane's is the lease a
// session holds while it writes the lane's round (swim new takes one), so
// another session's swim new, stub and archive refuse the lane.

// cmdClaim is swim claim N [--for D] [--session NAME] | --release [--force]
// | --break REASON. --session takes or releases the claim for another
// session: a planner assigning a worker its lane.
func cmdClaim(args []string) error {
	var release, force bool
	var forD, breakWhy, forSess string
	rest, err := flags{bools: map[string]*bool{"release": &release, "force": &force},
		strs: map[string]*string{"for": &forD, "break": &breakWhy, "session": &forSess}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 || release && breakWhy != "" {
		return usagef(`usage: swim claim N|RESOURCE [--for D] [--session NAME] | swim claim N|RESOURCE --release [--force] [--session NAME] | swim claim N|RESOURCE --break "<reason>"`)
	}
	if forSess != "" && (strings.TrimSpace(forSess) != forSess || strings.ContainsAny(forSess, " \t\r\n")) {
		return usagef("--session %q: a session name, as its SWIM_SESSION (no spaces)", forSess)
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	k, err := claimKeyArg(cfg, rest[0])
	if err != nil {
		return err
	}
	me := lane.SessionID()
	sess, by := me, ""
	if forSess != "" && forSess != me {
		sess, by = forSess, me // assigned: the claim is the named session's
	}
	if release {
		c, err := lane.ReleaseClaimOn(root, k, sess, force)
		if err != nil {
			return err
		}
		if c == nil {
			fmt.Printf("%s isn't claimed\n", k)
			return nil
		}
		if c.Session != sess {
			claimBrokenOn(root, k, c, me, "claim --release --force", "")
		}
		detail := "session " + c.Session
		if by != "" {
			detail += ", released by " + by
		}
		history.Log(root, history.Entry{Event: history.ClaimRelease, Lane: k.Lane, Job: c.Job, Detail: claimDetail(k, detail)})
		fmt.Printf("released %s's claim (session %s)\n", k, c.Session)
		return nil
	}
	lease := cfg.ClaimLeaseFor()
	if forD != "" {
		d, err := time.ParseDuration(forD)
		if err != nil || d <= 0 {
			return usagef("--for %q: a duration like 30m or 4h", forD)
		}
		lease = d
	}
	job := ""
	if k.Lane > 0 {
		info, _ := lane.ReadScript(root, k.Lane)
		job = info.Job
	}
	c, broke, err := lane.TakeClaimOn(root, k, sess, by, lease, job, breakWhy != "")
	var ec lane.ErrClaimed
	if errors.As(err, &ec) {
		return fmt.Errorf("%s; or swim claim %s --break \"<why>\" to take it over", strings.Split(err.Error(), "\n")[0], rest[0])
	}
	if err != nil {
		return err
	}
	if broke != nil {
		claimBrokenOn(root, k, broke, me, "claim --break", breakWhy)
	}
	detail := fmt.Sprintf("session %s for %s", sess, lease)
	if by != "" {
		detail += ", assigned by " + by
	}
	history.Log(root, history.Entry{Event: history.Claim, Lane: k.Lane, Job: job, Detail: claimDetail(k, detail)})
	fmt.Printf("%s %s\n", k, claimText(c))
	switch {
	case by != "" && k.Lane > 0:
		fmt.Printf("only session %s may swim new, stub or archive it now: start that worker with SWIM_SESSION=%s; its swim new and swim lint keep the claim\n", sess, sess)
	case k.Lane == 0:
		fmt.Printf("another session's swim claim %s is refused until it's released (swim claim %s --release) or runs out; a run whose # Locks: name %s warns, but runs\n", k.Name, k.Name, k.Name)
	}
	return nil
}

// claimKeyArg reads swim claim's argument: a lane number, or a resource's
// name (a lock name: letters, digits, . _ - /).
func claimKeyArg(cfg *config.Config, s string) (lane.ClaimKey, error) {
	if _, err := strconv.Atoi(s); err == nil {
		n, err := laneArg(cfg, s)
		return lane.LaneClaim(n), err
	}
	if !lane.ValidLockName(s) {
		return lane.ClaimKey{}, usagef("%q is neither a lane number nor a resource name (letters, digits, . _ - /)", s)
	}
	return lane.ResourceClaim(s), nil
}

// claimDetail is a claim's .swim.log detail: a resource's names it.
func claimDetail(k lane.ClaimKey, detail string) string {
	if k.Lane > 0 {
		return detail
	}
	return "resource " + k.Name + ": " + detail
}

// claimBroken records a live claim taken over by another session.
func claimBroken(root string, n int, broke *lane.Claim, by, via, why string) {
	claimBrokenOn(root, lane.LaneClaim(n), broke, by, via, why)
}

// claimBrokenOn is claimBroken on a lane's or a resource's claim.
func claimBrokenOn(root string, k lane.ClaimKey, broke *lane.Claim, by, via, why string) {
	detail := fmt.Sprintf("from=%s by=%s via=%s", broke.Session, by, via)
	if why != "" {
		detail += "  " + why
	}
	history.Log(root, history.Entry{Event: history.ClaimBroken, Lane: k.Lane, Job: broke.Job, Detail: claimDetail(k, detail)})
}

// claimText is how a lane's live claim reads in swim status and plan.
func claimText(c *lane.Claim) string {
	by := ""
	if c.AssignedBy != "" {
		by = ", assigned by " + c.AssignedBy
	}
	return fmt.Sprintf("claimed by %s (%s%s) until %s", c.Owner, c.Session, by, c.ExpiresAt().Local().Format("15:04"))
}
