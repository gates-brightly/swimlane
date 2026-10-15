package lane

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Taking, refreshing, refusing, breaking, expiring and releasing a claim.
func TestClaims(t *testing.T) {
	root := t.TempDir()
	if _, err := TakeClaim(root, 3, "a", time.Hour, "job-a", false); err != nil {
		t.Fatal(err)
	}
	c := ReadClaim(root, 3)
	if c == nil || c.Session != "a" || c.Job != "job-a" {
		t.Fatalf("claim: %+v", c)
	}
	// Another session is refused, naming the holder.
	_, err := TakeClaim(root, 3, "b", time.Hour, "job-b", false)
	var ec ErrClaimed
	if !errors.As(err, &ec) || ec.C.Session != "a" || !strings.Contains(err.Error(), "session a") {
		t.Fatalf("b took a's claim: %v", err)
	}
	if CheckClaim(root, 3, "a") != nil || CheckClaim(root, 3, "b") == nil {
		t.Error("CheckClaim")
	}
	// The holder refreshes; another session can't, and lint never creates.
	before := ReadClaim(root, 3).Expires
	time.Sleep(1100 * time.Millisecond)
	if !RefreshClaim(root, 3, "a", 2*time.Hour) || ReadClaim(root, 3).Expires == before {
		t.Error("the holder's refresh didn't move the expiry")
	}
	if RefreshClaim(root, 3, "b", time.Hour) || RefreshClaim(root, 4, "a", time.Hour) || ReadClaim(root, 4) != nil {
		t.Error("a refresh by another session, or of no claim, did something")
	}
	// Force takes it over and returns what it broke.
	broke, err := TakeClaim(root, 3, "b", time.Hour, "job-b", true)
	if err != nil || broke == nil || broke.Session != "a" || ReadClaim(root, 3).Session != "b" {
		t.Fatalf("force: %v %+v", err, broke)
	}
	// Release: another's needs force; one's own doesn't.
	if _, err := ReleaseClaim(root, 3, "a", false); err == nil {
		t.Error("a released b's claim")
	}
	if c, err := ReleaseClaim(root, 3, "b", false); err != nil || c == nil || ReadClaim(root, 3) != nil {
		t.Errorf("release: %v %+v", err, c)
	}
	// An expired claim reads as none and is removed; anyone takes the lane.
	TakeClaim(root, 5, "a", time.Millisecond, "", false)
	time.Sleep(10 * time.Millisecond)
	if ReadClaim(root, 5) != nil {
		t.Error("an expired claim still reads")
	}
	if _, err := os.Stat(filepath.Join(ClaimDir(root), "lane.5.yml")); err == nil {
		t.Error("the expired claim's file wasn't removed")
	}
	if broke, err := TakeClaim(root, 5, "b", time.Hour, "", false); err != nil || broke != nil {
		t.Errorf("taking an expired claim: %v %+v", err, broke)
	}
	// The lock isn't a resource lock's path.
	if _, err := os.Stat(filepath.Join(ClaimDir(root), ".lock")); err != nil {
		t.Error("no .swim/claims/.lock")
	}
	if _, err := os.Stat(filepath.Join(root, ".swim", "locks", "claims.lock")); err == nil {
		t.Error("the claims lock collides with a resource lock named claims")
	}
}

// Sessions racing for one unclaimed lane: exactly one wins.
func TestClaimLeaseRace(t *testing.T) {
	root := t.TempDir()
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := TakeClaim(root, 1, "s"+string(rune('a'+i)), time.Hour, "", false); err == nil {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if won != 1 {
		t.Fatalf("%d sessions took the claim, want 1", won)
	}
}

func TestSessionID(t *testing.T) {
	t.Setenv("SWIM_SESSION", "planner-7f2c")
	if SessionID() != "planner-7f2c" {
		t.Error("SWIM_SESSION doesn't win")
	}
	t.Setenv("SWIM_SESSION", "")
	t.Setenv("SWIM_USER", "zach")
	h, _ := os.Hostname()
	if SessionID() != "zach@"+h {
		t.Errorf("SessionID() = %q", SessionID())
	}
}
