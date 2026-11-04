package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
)

// swim claim takes, refreshes or gives up a lane's claim: the lease a
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
		return usagef(`usage: swim claim N [--for D] [--session NAME] | swim claim N --release [--force] [--session NAME] | swim claim N --break "<reason>"`)
	}
	if forSess != "" && (strings.TrimSpace(forSess) != forSess || strings.ContainsAny(forSess, " \t\r\n")) {
		return usagef("--session %q: a session name, as its SWIM_SESSION (no spaces)", forSess)
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	n, err := laneArg(cfg, rest[0])
	if err != nil {
		return err
	}
	me := lane.SessionID()
	sess, by := me, ""
	if forSess != "" && forSess != me {
		sess, by = forSess, me // assigned: the claim is the named session's
	}
	if release {
		c, err := lane.ReleaseClaim(root, n, sess, force)
		if err != nil {
			return err
		}
		if c == nil {
			fmt.Printf("swim %d isn't claimed\n", n)
			return nil
		}
		if c.Session != sess {
			claimBroken(root, n, c, me, "claim --release --force", "")
		}
		detail := "session " + c.Session
		if by != "" {
			detail += ", released by " + by
		}
		history.Log(root, history.Entry{Event: history.ClaimRelease, Lane: n, Job: c.Job, Detail: detail})
		fmt.Printf("released swim %d's claim (session %s)\n", n, c.Session)
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
	info, _ := lane.ReadScript(root, n)
	c, broke, err := lane.TakeClaimFor(root, n, sess, by, lease, info.Job, breakWhy != "")
	var ec lane.ErrClaimed
	if errors.As(err, &ec) {
		return fmt.Errorf("%s; or swim claim %d --break \"<why>\" to take it over", strings.Split(err.Error(), "\n")[0], n)
	}
	if err != nil {
		return err
	}
	if broke != nil {
		claimBroken(root, n, broke, me, "claim --break", breakWhy)
	}
	detail := fmt.Sprintf("session %s for %s", sess, lease)
	if by != "" {
		detail += ", assigned by " + by
	}
	history.Log(root, history.Entry{Event: history.Claim, Lane: n, Job: info.Job, Detail: detail})
	fmt.Printf("swim %d %s\n", n, claimText(c))
	if by != "" {
		fmt.Printf("only session %s may swim new, stub or archive it now: start that worker with SWIM_SESSION=%s; its swim new and swim lint keep the claim\n", sess, sess)
	}
	return nil
}

// claimBroken records a live claim taken over by another session.
func claimBroken(root string, n int, broke *lane.Claim, by, via, why string) {
	detail := fmt.Sprintf("from=%s by=%s via=%s", broke.Session, by, via)
	if why != "" {
		detail += "  " + why
	}
	history.Log(root, history.Entry{Event: history.ClaimBroken, Lane: n, Job: broke.Job, Detail: detail})
}

// claimText is how a lane's live claim reads in swim status and plan.
func claimText(c *lane.Claim) string {
	by := ""
	if c.AssignedBy != "" {
		by = ", assigned by " + c.AssignedBy
	}
	return fmt.Sprintf("claimed by %s (%s%s) until %s", c.Owner, c.Session, by, c.ExpiresAt().Local().Format("15:04"))
}
