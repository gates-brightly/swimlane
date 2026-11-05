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

// Lane claims: swim new takes one, silently: its own session's swim
// status shows no claim and no owner line. Another session's new and
// archive refuse the lane, naming the holder, and its status shows the
// claim; --force takes it over and logs claim-broken; an expired claim is
// no bar; swim run releases it (and warns, J1: it runs anyway).
func TestLaneClaims(t *testing.T) {
	r := newRepo(t)
	r.env = append(r.env, "USER=alice", "SWIM_USER=")
	if out, code := r.as("a", "new", "3", "drop the orders index"); code != 0 {
		t.Fatalf("a's new: %s", out)
	}
	out, code := r.as("b", "new", "3", "something else")
	if code == 0 || !strings.Contains(out, "lane 3 is claimed by") || !strings.Contains(out, "session a") || !strings.Contains(out, "SWIM_SESSION") {
		t.Fatalf("b's new over a's claim: exit %d\n%s", code, out)
	}
	st, _ := r.as("b", "status", "3")
	contains(t, "status", st, "claimed by alice", "(a) until")
	st, _ = r.as("a", "status", "3")
	if strings.Contains(st, "claimed by") || strings.Contains(st, "owner alice") {
		t.Errorf("a's own status shows its own claim or name:\n%s", st)
	}
	y, _ := r.as("b", "status", "--yaml")
	contains(t, "status --yaml", y, "claims:", "session: a")
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
	// Someone else's name is news: carol sees the owner line.
	saved := r.env
	r.env = append(append([]string(nil), r.env...), "USER=carol")
	st, _ = r.as("c", "status", "3")
	r.env = saved
	contains(t, "carol's status", st, "owner alice")

	// An expired claim is no bar, and nothing is logged as broken.
	if out, code := r.as("a", "new", "5", "before the lease ran out"); code != 0 {
		t.Fatalf("a's new 5: %s", out)
	}
	// Expired: its lease ran out (written as if an hour had passed).
	claim := filepath.Join(r.root, ".swim", "claims", "lane.5.yml")
	data, err := os.ReadFile(claim)
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	os.WriteFile(claim, regexp.MustCompile(`(?m)^expires: .*$`).ReplaceAll(data, []byte("expires: "+past)), 0o644)
	if out, code := r.as("b", "new", "5", "after the lease", "--force"); code != 0 {
		t.Fatalf("b's new after a's claim expired: %s", out)
	}
	if n := strings.Count(r.mustSwim("log"), "claim-broken"); n != 1 {
		t.Errorf("claim-broken lines: %d, want 1", n)
	}

	// swim run releases a claim, another session's too, and says so.
	r.script(5, "after the lease", `step "x" true`)
	out, _ = r.as("a", "run", "5", "--plain")
	contains(t, "run warns", out, "was claimed by", "running it released the claim")
	if _, err := os.Stat(filepath.Join(r.root, ".swim", "claims", "lane.5.yml")); err == nil {
		t.Error("the claim survived the run")
	}

	// archive refuses another session's claim; --force takes it over.
	if out, code := r.as("b", "new", "5", "the next round", "--force"); code != 0 {
		t.Fatalf("b's next round: %s", out)
	}
	if out, code := r.as("a", "archive", "5"); code == 0 || !strings.Contains(out, "claimed by") {
		t.Errorf("a's archive over b's claim: exit %d\n%s", code, out)
	}
	out, code = r.as("a", "archive", "5", "--force")
	if code != 0 || !strings.Contains(out, "kept lane.5.sh: changed since its round ran") {
		t.Fatalf("a's archive --force: exit %d\n%s", code, out)
	}
	contains(t, "archive --force", r.mustSwim("log"), "from=b by=a via=archive")
	if _, err := os.Stat(claim); err == nil {
		t.Error("the claim survived the archive")
	}
}

// Without SWIM_SESSION, two shells of one user on one machine are one
// session: the claim doesn't stop the second swim new (the pending-round
// check still does).
func TestLaneClaimsSameSession(t *testing.T) {
	r := newRepo(t)
	r.mustSwim("new", "2", "first")
	out, code := r.swim("new", "2", "second")
	if code == 0 || strings.Contains(out, "claimed by") || !strings.Contains(out, "ready round") {
		t.Errorf("exit %d\n%s", code, out)
	}
}
