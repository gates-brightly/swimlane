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

// cmdClaim is swim claim N [--for D] | --release [--force] | --break REASON.
func cmdClaim(args []string) error {
	var release, force bool
	var forD, breakWhy string
	rest, err := flags{bools: map[string]*bool{"release": &release, "force": &force},
		strs: map[string]*string{"for": &forD, "break": &breakWhy}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 || release && breakWhy != "" {
		return usagef(`usage: swim claim N [--for D] | swim claim N --release [--force] | swim claim N --break "<reason>"`)
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	n, err := laneArg(cfg, rest[0])
	if err != nil {
		return err
	}
	if _, group := lane.GroupOf(root, n); group {
		return fmt.Errorf("swim %d is a group file's lane: group files are committed, standing work, not a round a session writes", n)
	}
	sess := lane.SessionID()
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
			claimBroken(root, n, c, sess, "claim --release --force", "")
		}
		history.Log(root, history.Entry{Event: history.ClaimRelease, Lane: n, Job: c.Job, Detail: "session " + c.Session})
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
	broke, err := lane.TakeClaim(root, n, sess, lease, info.Job, breakWhy != "")
	var ec lane.ErrClaimed
	if errors.As(err, &ec) {
		return fmt.Errorf("%s; or swim claim %d --break \"<why>\" to take it over", strings.Split(err.Error(), "\n")[0], n)
	}
	if err != nil {
		return err
	}
	if broke != nil {
		claimBroken(root, n, broke, sess, "claim --break", breakWhy)
	}
	history.Log(root, history.Entry{Event: history.Claim, Lane: n, Job: info.Job, Detail: fmt.Sprintf("session %s for %s", sess, lease)})
	c := lane.ReadClaim(root, n)
	fmt.Printf("swim %d claimed by %s (session %s) until %s\n", n, c.Owner, c.Session, c.ExpiresAt().Local().Format("15:04"))
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
	return fmt.Sprintf("claimed by %s (%s) until %s", c.Owner, c.Session, c.ExpiresAt().Local().Format("15:04"))
}
