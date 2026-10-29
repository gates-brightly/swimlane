package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// as runs swim as session sess.
func (r *repo) as(sess string, args ...string) (string, int) {
	r.t.Helper()
	saved := r.env
	r.env = append(append([]string(nil), r.env...), "SWIM_SESSION="+sess)
	defer func() { r.env = saved }()
	return r.swim(args...)
}

// Lane claims: swim new takes one; another session's new, stub and
// archive refuse the lane, naming the holder; --force takes it over and
// logs claim-broken; swim claim --break gives a reason; an expired claim
// is no bar; swim run releases it (and warns, J1: it runs anyway).
func TestLaneClaims(t *testing.T) {
	r := newRepo(t, "")
	if out, code := r.as("a", "new", "3", "drop the orders index"); code != 0 {
		t.Fatalf("a's new: %s", out)
	}
	out, code := r.as("b", "new", "3", "something else")
	if code == 0 || !strings.Contains(out, "swim 3 is claimed by") || !strings.Contains(out, "session a") || !strings.Contains(out, "SWIM_SESSION") {
		t.Fatalf("b's new over a's claim: exit %d\n%s", code, out)
	}
	st, _ := r.as("b", "status", "3")
	contains(t, "status", st, "claimed by", "(a) until")
	y, _ := r.as("b", "status", "--yaml")
	contains(t, "status --yaml", y, "claims:", "session: a")
	if out, code := r.as("b", "stub", "3", "nothing"); code == 0 || !strings.Contains(out, "claimed by") {
		t.Errorf("b's stub: exit %d\n%s", code, out)
	}
	// a's own new and lint refresh it.
	if out, code := r.as("a", "new", "3", "drop the orders index, again", "--force"); code != 0 {
		t.Fatalf("a's own new --force: %s", out)
	}
	r.as("a", "lint", "3")

	// b forces: it's b's now, and .swim.log says whose it was.
	if out, code := r.as("b", "new", "3", "something else", "--force"); code != 0 {
		t.Fatalf("b's new --force: %s", out)
	}
	contains(t, "claim-broken", r.mustSwim("log"), "claim-broken", "from=a by=b via=new")
	st, _ = r.as("a", "status", "3")
	contains(t, "now b's", st, "(b) until")

	// --break with a reason.
	if out, code := r.as("a", "claim", "3"); code == 0 || !strings.Contains(out, "--break") {
		t.Errorf("a's claim over b's: exit %d\n%s", code, out)
	}
	if out, code := r.as("a", "claim", "3", "--break", "session b died"); code != 0 {
		t.Fatalf("--break: %s", out)
	}
	contains(t, "break reason", r.mustSwim("log"), "from=b by=a via=claim --break  session b died")

	// An expired claim is no bar, and nothing is logged as broken.
	if out, code := r.as("a", "claim", "5", "--for", "1s"); code != 0 {
		t.Fatalf("claim --for: %s", out)
	}
	// Expired: its lease ran out (written as if an hour had passed).
	claim := filepath.Join(r.root, ".swim", "claims", "lane.5.yml")
	data, err := os.ReadFile(claim)
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	os.WriteFile(claim, regexp.MustCompile(`(?m)^expires: .*$`).ReplaceAll(data, []byte("expires: "+past)), 0o644)
	if out, code := r.as("b", "new", "5", "after the lease"); code != 0 {
		t.Fatalf("b's new after a's claim expired: %s", out)
	}
	if n := strings.Count(r.mustSwim("log"), "claim-broken"); n != 2 {
		t.Errorf("claim-broken lines: %d, want 2", n)
	}

	// swim run releases a claim, another session's too, and says so.
	r.script(5, "after the lease", `run "x" true`)
	out, _ = r.as("a", "run", "5", "--plain")
	contains(t, "run warns", out, "was claimed by", "running it released the claim")
	if _, err := os.Stat(filepath.Join(r.root, ".swim", "claims", "lane.5.yml")); err == nil {
		t.Error("the claim survived the run")
	}

	// archive releases its own; release --force takes another's.
	if out, code := r.as("b", "claim", "3", "--release"); code == 0 || !strings.Contains(out, "claimed by") {
		t.Errorf("b released a's claim: exit %d\n%s", code, out)
	}
	if out, code := r.as("b", "claim", "3", "--release", "--force"); code != 0 {
		t.Fatalf("release --force: %s", out)
	}
	contains(t, "release", r.mustSwim("log"), "claim-release")
}

// Without SWIM_SESSION, two shells of one user on one machine are one
// session: the claim doesn't stop the second swim new (the pending-round
// check still does).
func TestLaneClaimsSameSession(t *testing.T) {
	r := newRepo(t, "")
	r.mustSwim("new", "2", "first")
	out, code := r.swim("new", "2", "second")
	if code == 0 || strings.Contains(out, "claimed by") || !strings.Contains(out, "pending") {
		t.Errorf("exit %d\n%s", code, out)
	}
}
